package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/anthropics/anthropic-sdk-go"
)

// The copywriter writes what goes with each generated reel, in every
// language: the video's title, description and tags, and the texts shown on
// screen (the hook, the closing and the template's own phrases). It starts
// from a base title taken from the source's file name.
//
// Providers: Claude (Anthropic API, ANTHROPIC_API_KEY), a local model
// behind an OpenAI-compatible endpoint (Ollama, LM Studio...), or plain
// rules when there's no model.

// LLMSettings choose the provider ("anthropic", "local" or "none"), the
// model and, for local models, the endpoint. Empty fields take defaults.
type LLMSettings struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	BaseURL  string `json:"baseURL"`
}

const defaultClaudeModel = "claude-opus-5-5"

// LangCopy is everything written for one language.
type LangCopy struct {
	Title       string            `json:"title"`       // video title (platform)
	Hook        string            `json:"hook"`        // short title shown on screen
	Closing     string            `json:"closing"`     // call to action shown at the end
	Description string            `json:"description"` // with hashtags
	Tags        []string          `json:"tags"`
	Texts       map[string]string `json:"texts"` // the template's own phrases, by role
}

// CopyRequest is what the copywriter gets for one source and template.
type CopyRequest struct {
	BaseTitle string            // from the file name
	Template  string            // id
	Langs     []string          // e.g. es, en
	Texts     map[string]string // the template's phrases in Spanish, by role
	Shapes    int
	Frame     []byte // JPEG of the source, if available
}

type copywriter interface {
	Write(ctx context.Context, req CopyRequest) (map[string]LangCopy, error)
	Name() string
}

// newCopywriter picks the provider from the settings (or, when empty, from
// the environment: ANTHROPIC_API_KEY means Claude, LLM_BASE_URL a local model).
func newCopywriter(s LLMSettings) copywriter {
	provider := s.Provider
	if provider == "" {
		switch {
		case os.Getenv("ANTHROPIC_API_KEY") != "":
			provider = "anthropic"
		case os.Getenv("LLM_BASE_URL") != "":
			provider = "local"
		default:
			provider = "none"
		}
	}
	switch provider {
	case "anthropic":
		return &claudeWriter{model: claudeModel(s.Model)}
	case "local":
		base := s.BaseURL
		if base == "" {
			base = envOr("LLM_BASE_URL", "http://host.docker.internal:11434/v1")
		}
		model := s.Model
		if model == "" {
			model = envOr("LLM_MODEL", "llama3.1")
		}
		return &localWriter{baseURL: strings.TrimRight(base, "/"), model: model}
	}
	return rulesWriter{}
}

// claudeModel is the Claude model to use: the one chosen, CLAUDE_MODEL, or
// the default. A name that isn't a Claude model (e.g. the local model's,
// from LLM_MODEL or the same field on the Generar page) is ignored: sent to
// Claude it only gets a 404.
func claudeModel(chosen string) string {
	for _, m := range []string{chosen, os.Getenv("CLAUDE_MODEL"), os.Getenv("LLM_MODEL")} {
		if m = strings.TrimSpace(m); strings.HasPrefix(m, "claude-") {
			return m
		}
	}
	return defaultClaudeModel
}

// ---------- the base title ----------

var (
	camelSplit = regexp.MustCompile(`([a-zá-ú])([A-ZÁ-Ú])`)
	noise      = regexp.MustCompile(`(?i)\b(img|dsc|vid|pxl|copy|copia|final|v\d+|\d{6,}|\d{4}-\d{2}-\d{2})\b`)
	// "Screenshot 2026-10-19 at 11.00.27 PM": the rest of the name is a date.
	screenshot = regexp.MustCompile(`(?i)\b(screenshot|screen shot|captura de pantalla|captura)\b.*$`)
	spaces     = regexp.MustCompile(`\s+`)
)

// titleFromName turns "mona_lisa-final (2).png" into "Mona lisa".
func titleFromName(name string) string {
	s := strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
	s = strings.NewReplacer("_", " ", "-", " ", ".", " ", "(", " ", ")", " ", "[", " ", "]", " ").Replace(s)
	s = screenshot.ReplaceAllString(s, "")
	s = camelSplit.ReplaceAllString(s, "$1 $2")
	s = noise.ReplaceAllString(s, " ")
	s = strings.Trim(spaces.ReplaceAllString(s, " "), " 0123456789")
	if s == "" {
		return ""
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// ---------- the template's phrases ----------

// templateTexts are the phrases each template brings (as in
// web/templates.js), by role, for the copywriter to translate. "title" and
// "closing" are written fresh (hook and closing) and aren't listed.
var templateTexts = map[string]map[string]string{
	"levels":       {"question": "¿En qué nivel la adivinaste?", "level": "Nivel {nivel} · {formas} formas", "closing": "Comentá en qué nivel la adivinaste 👇"},
	"compare":      {"topLabel": "Original", "bottomLabel": "{formas} {tipo}"},
	"curtain":      {"question": "¿Se nota la diferencia?"},
	"mosaic":       {"question": "¿Cuántas formas hacen falta?", "closing": "¿Con cuál la reconociste? 👇"},
	"prefer":       {"question": "¿Cuál te gusta más?", "closing": "Comentá 1, 2, 3 o 4 👇"},
	"counter":      {"counter": "{n} {tipo}"},
	"countCurtain": {"counter": "{n} {tipo}", "question": "¿Se nota la diferencia?"},
	"countVideo":   {"counter": "{n} {tipo}"},
}

// ---------- prompt and schema (shared by the models) ----------

var langNames = map[string]string{"es": "Spanish (Rioplatense, neutral enough for Latin America)", "en": "English", "pt": "Portuguese (Brazil)", "it": "Italian", "fr": "French", "de": "German"}

func langName(code string) string {
	if n, ok := langNames[code]; ok {
		return n
	}
	return code
}

func copyPrompt(req CopyRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You write the copy for a short vertical video (YouTube Shorts, TikTok, Instagram Reels) of a channel that recreates images and videos with geometric shapes (the algorithm adds %d shapes one by one; the reel shows the result being built and then the original).\n\n", req.Shapes)
	fmt.Fprintf(&b, "Subject, from the source file name: %q. ", req.BaseTitle)
	if len(req.Frame) > 0 {
		b.WriteString("The attached image is the source: use it to understand the subject (if the file name says little, name what's in the image).")
	}
	fmt.Fprintf(&b, "\nTemplate: %s.\n\n", templateNames[req.Template])
	b.WriteString("For each language write:\n")
	b.WriteString("- title: the video's title, catchy, at most 70 characters, no hashtags.\n")
	b.WriteString("- hook: a very short title shown on screen at the start (at most 6 words).\n")
	b.WriteString("- closing: a short call to action shown at the end (at most 8 words, may end with one emoji).\n")
	b.WriteString("- description: 1-3 short sentences, then 3-5 relevant hashtags including #Shorts.\n")
	b.WriteString("- tags: 8-15 lowercase search tags, without '#'.\n")
	if len(req.Texts) > 0 {
		b.WriteString("- texts: these on-screen phrases of the template, adapted to the language. Keep {placeholders} exactly as they are and keep any emoji:\n")
		for role, t := range req.Texts {
			fmt.Fprintf(&b, "  - %s: %q\n", role, t)
		}
	} else {
		b.WriteString("- texts: an empty list.\n")
	}
	b.WriteString("\nLanguages: ")
	for i, l := range req.Langs {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s (%s)", l, langName(l))
	}
	b.WriteString(". Write natively in each language, don't translate word by word.")
	return b.String()
}

func copySchema() map[string]any {
	str := map[string]any{"type": "string"}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"languages": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"lang": str, "title": str, "hook": str, "closing": str, "description": str,
						"tags": map[string]any{"type": "array", "items": str},
						"texts": map[string]any{"type": "array", "items": map[string]any{
							"type":                 "object",
							"properties":           map[string]any{"role": str, "text": str},
							"required":             []string{"role", "text"},
							"additionalProperties": false,
						}},
					},
					"required":             []string{"lang", "title", "hook", "closing", "description", "tags", "texts"},
					"additionalProperties": false,
				},
			},
		},
		"required":             []string{"languages"},
		"additionalProperties": false,
	}
}

// parseCopy reads the models' JSON answer into copies per language,
// checking every language came back.
func parseCopy(raw string, req CopyRequest) (map[string]LangCopy, error) {
	raw = strings.TrimSpace(raw)
	if i := strings.Index(raw, "{"); i > 0 { // local models sometimes add a preamble or a code fence
		raw = raw[i:]
	}
	if i := strings.LastIndex(raw, "}"); i >= 0 {
		raw = raw[:i+1]
	}
	var ans struct {
		Languages []struct {
			Lang, Title, Hook, Closing, Description string
			Tags                                    []string
			Texts                                   []struct{ Role, Text string }
		}
	}
	if err := json.Unmarshal([]byte(raw), &ans); err != nil {
		return nil, fmt.Errorf("respuesta del modelo ilegible: %v", err)
	}
	out := map[string]LangCopy{}
	for _, l := range ans.Languages {
		c := LangCopy{Title: strings.TrimSpace(l.Title), Hook: strings.TrimSpace(l.Hook), Closing: strings.TrimSpace(l.Closing),
			Description: strings.TrimSpace(l.Description), Texts: map[string]string{}}
		for _, t := range l.Tags {
			if t = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(t, "#"))); t != "" {
				c.Tags = append(c.Tags, t)
			}
		}
		for _, t := range l.Texts {
			if _, ok := req.Texts[t.Role]; ok && strings.TrimSpace(t.Text) != "" {
				c.Texts[t.Role] = strings.TrimSpace(t.Text)
			}
		}
		out[strings.ToLower(l.Lang)] = c
	}
	// Anything missing comes from the rules, so a reel never lacks its copy.
	fallback, _ := rulesWriter{}.Write(context.Background(), req)
	for _, l := range req.Langs {
		c, ok := out[l]
		if !ok || c.Title == "" {
			out[l] = fallback[l]
			continue
		}
		for role, t := range fallback[l].Texts {
			if _, ok := c.Texts[role]; !ok {
				c.Texts[role] = t
			}
		}
		out[l] = c
	}
	return out, nil
}

// ---------- Claude ----------

type claudeWriter struct{ model string }

func (w *claudeWriter) Name() string { return "Claude (" + w.model + ")" }

func (w *claudeWriter) Write(ctx context.Context, req CopyRequest) (map[string]LangCopy, error) {
	client := anthropic.NewClient() // ANTHROPIC_API_KEY
	blocks := []anthropic.BetaContentBlockParamUnion{}
	if len(req.Frame) > 0 {
		blocks = append(blocks, anthropic.NewBetaImageBlock(anthropic.BetaBase64ImageSourceParam{
			Data: base64.StdEncoding.EncodeToString(req.Frame), MediaType: "image/jpeg",
		}))
	}
	blocks = append(blocks, anthropic.NewBetaTextBlock(copyPrompt(req)))
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	resp, err := client.Beta.Messages.New(ctx, anthropic.BetaMessageNewParams{
		Model:     w.model,
		MaxTokens: 16000,
		Messages:  []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(blocks...)},
		OutputConfig: anthropic.BetaOutputConfigParam{
			Effort: anthropic.BetaOutputConfigEffortLow, // short copy, not a hard problem
			Format: anthropic.BetaJSONOutputFormatParam{Schema: copySchema()},
		},
		// If a safety classifier declines, the API retries on a fallback model.
		Betas:     []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
		Fallbacks: anthropic.BetaFallbacksParamOfDefault(),
	})
	if err != nil {
		var apierr *anthropic.Error
		if errors.As(err, &apierr) {
			switch apierr.StatusCode {
			case 401, 403:
				return nil, fmt.Errorf("Claude rechazó la API key (%d)", apierr.StatusCode)
			case 429:
				return nil, errors.New("Claude: límite de uso alcanzado, probá más tarde")
			case 404:
				return nil, fmt.Errorf("Claude: el modelo %q no existe o tu cuenta no tiene acceso (404)", w.model)
			}
			return nil, fmt.Errorf("Claude: error %d", apierr.StatusCode)
		}
		return nil, fmt.Errorf("Claude: %v", err)
	}
	if resp.StopReason == anthropic.BetaStopReasonRefusal {
		return nil, errors.New("Claude se negó a escribir este texto")
	}
	var text strings.Builder
	for _, block := range resp.Content {
		if t, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
			text.WriteString(t.Text)
		}
	}
	return parseCopy(text.String(), req)
}

// ---------- local model (OpenAI-compatible: Ollama, LM Studio...) ----------

type localWriter struct{ baseURL, model string }

func (w *localWriter) Name() string { return "modelo local (" + w.model + " en " + w.baseURL + ")" }

func (w *localWriter) Write(ctx context.Context, req CopyRequest) (map[string]LangCopy, error) {
	schema, _ := json.Marshal(copySchema())
	// Text only: not every local model sees images.
	answer, err := w.chat(ctx, "Answer only with a JSON object matching this JSON schema, nothing else:\n"+string(schema), copyPrompt(req), 0.7)
	if err != nil {
		return nil, err
	}
	return parseCopy(answer, req)
}

// chat sends one system + user message to the OpenAI-compatible endpoint
// and returns the answer's text.
func (w *localWriter) chat(ctx context.Context, system, user string, temperature float64) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"model":           w.model,
		"messages":        []map[string]any{{"role": "system", "content": system}, {"role": "user", "content": user}},
		"response_format": map[string]any{"type": "json_object"},
		"temperature":     temperature,
		"stream":          false,
	})
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	hreq, _ := http.NewRequestWithContext(ctx, "POST", w.baseURL+"/chat/completions", bytes.NewReader(body))
	hreq.Header.Set("Content-Type", "application/json")
	if key := os.Getenv("LLM_API_KEY"); key != "" {
		hreq.Header.Set("Authorization", "Bearer "+key)
	}
	res, err := http.DefaultClient.Do(hreq)
	if err != nil {
		return "", fmt.Errorf("modelo local (%s): %v", w.baseURL, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode != 200 {
		return "", fmt.Errorf("modelo local: %s: %s", res.Status, strings.TrimSpace(string(b[:min(len(b), 300)])))
	}
	var ans struct {
		Choices []struct {
			Message struct{ Content string }
		}
	}
	if err := json.Unmarshal(b, &ans); err != nil || len(ans.Choices) == 0 {
		return "", errors.New("modelo local: respuesta inesperada")
	}
	return ans.Choices[0].Message.Content, nil
}

// ---------- translation ----------

// Copy is a reel's texts: what the platforms show and what's on screen.
type Copy struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Texts       []string `json:"texts"` // the on-screen phrases, in order
}

// localTranslator is the local model chosen for translations: the
// generator's if it's a local one, otherwise LLM_BASE_URL / LLM_MODEL.
func localTranslator(s LLMSettings) *localWriter {
	if s.Provider != "local" {
		s = LLMSettings{Provider: "local"}
	}
	return newCopywriter(s).(*localWriter)
}

// Translate puts a reel's copy into another language with a local model.
// Placeholders ({formas}...), emoji and line breaks stay; every on-screen
// phrase comes back, in the same order.
func (w *localWriter) Translate(ctx context.Context, from, to string, c Copy) (Copy, error) {
	in, _ := json.Marshal(c)
	system := `You translate the copy of short vertical videos (YouTube Shorts, TikTok, Reels). Answer only with a JSON object with the same keys as the input: {"title": string, "description": string, "tags": [string], "texts": [string]}.`
	user := fmt.Sprintf("Translate from %s to %s. Write it natively (as a creator in that language would), not word by word. Keep exactly: {placeholders} like {formas} or {nivel}, emoji, line breaks, and the #Shorts hashtag; translate the other hashtags. Tags in lowercase, without '#'. \"texts\" must have exactly %d items, in the same order.\n\n%s",
		langName(from), langName(to), len(c.Texts), in)
	answer, err := w.chat(ctx, system, user, 0.3)
	if err != nil {
		return Copy{}, err
	}
	answer = strings.TrimSpace(answer)
	if i := strings.Index(answer, "{"); i > 0 {
		answer = answer[i:]
	}
	if i := strings.LastIndex(answer, "}"); i >= 0 {
		answer = answer[:i+1]
	}
	var out Copy
	if err := json.Unmarshal([]byte(answer), &out); err != nil {
		return Copy{}, fmt.Errorf("traducción ilegible: %v", err)
	}
	if len(out.Texts) != len(c.Texts) {
		return Copy{}, fmt.Errorf("la traducción devolvió %d frases de %d", len(out.Texts), len(c.Texts))
	}
	// Placeholders must survive: the reel fills them in.
	ph := regexp.MustCompile(`\{\w+\}`)
	for i, t := range c.Texts {
		for _, p := range ph.FindAllString(t, -1) {
			if !strings.Contains(out.Texts[i], p) {
				return Copy{}, fmt.Errorf("la traducción perdió %s en %q", p, out.Texts[i])
			}
		}
	}
	if strings.TrimSpace(out.Title) == "" {
		return Copy{}, errors.New("la traducción vino sin título")
	}
	for i, t := range out.Tags {
		out.Tags[i] = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(t, "#")))
	}
	return out, nil
}

// ---------- rules (no model) ----------

type rulesWriter struct{}

func (rulesWriter) Name() string { return "reglas (sin modelo)" }

// Fixed texts per language. Languages without an entry use English.
var ruleTexts = map[string]map[string]string{
	"es": {
		"desc":     "%s, recreada con %d formas geométricas.",
		"closing":  "¿Cuál hago mañana? 👇",
		"tags":     "arte,arte generativo,formas geométricas,low poly,arte digital,timelapse,shorts",
		"Original": "Original", "¿En qué nivel la adivinaste?": "¿En qué nivel la adivinaste?",
	},
	"en": {
		"desc":                            "%s, recreated with %d geometric shapes.",
		"closing":                         "What should I do next? 👇",
		"tags":                            "art,generative art,geometric shapes,low poly,digital art,timelapse,shorts",
		"¿En qué nivel la adivinaste?":    "At which level did you guess it?",
		"Nivel {nivel} · {formas} formas": "Level {nivel} · {formas} shapes",
		"Comentá en qué nivel la adivinaste 👇": "Comment the level you guessed it at 👇",
		"Original":                     "Original",
		"{formas} {tipo}":              "{formas} {tipo}",
		"¿Se nota la diferencia?":      "Can you tell the difference?",
		"¿Cuántas formas hacen falta?": "How many shapes does it take?",
		"¿Con cuál la reconociste? 👇":  "Which one gave it away? 👇",
		"¿Cuál te gusta más?":          "Which one do you like best?",
		"Comentá 1, 2, 3 o 4 👇":        "Comment 1, 2, 3 or 4 👇",
		"{n} {tipo}":                   "{n} {tipo}",
	},
}

func (rulesWriter) Write(ctx context.Context, req CopyRequest) (map[string]LangCopy, error) {
	out := map[string]LangCopy{}
	subject := req.BaseTitle
	for _, l := range req.Langs {
		t, ok := ruleTexts[l]
		if !ok {
			t = ruleTexts["en"]
		}
		name := subject
		if name == "" {
			name = map[bool]string{true: "Esta imagen", false: "This image"}[l == "es"]
		}
		c := LangCopy{Title: name, Hook: name, Closing: t["closing"], Texts: map[string]string{}}
		c.Description = fmt.Sprintf(t["desc"], name, req.Shapes) + "\n\n#Shorts #art #lowpoly"
		for _, w := range strings.Fields(strings.ToLower(subject)) {
			if len([]rune(w)) > 2 {
				c.Tags = append(c.Tags, w)
			}
		}
		c.Tags = append(c.Tags, strings.Split(t["tags"], ",")...)
		for role, text := range req.Texts {
			if tr, ok := t[text]; ok {
				c.Texts[role] = tr
			} else {
				c.Texts[role] = text
			}
		}
		out[l] = c
	}
	return out, nil
}
