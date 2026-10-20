package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTitleFromName(t *testing.T) {
	for in, want := range map[string]string{
		"mona_lisa-final (2).png":                            "Mona lisa",
		"MonaLisa.jpg":                                       "Mona Lisa",
		"Screenshot 2026-10-19 at 11.00.27 PM.png":           "",
		"IMG_20240101_123456.jpg":                            "",
		"atardecer en la playa.mp4":                          "Atardecer en la playa",
		"charly garcia v2.webp":                              "Charly garcia",
		"Sunset at the beach.jpg":                            "Sunset at the beach",
		"Mona Lisa Screenshot 2026-10-19 at 11.00.27 PM.png": "Mona Lisa",
	} {
		if got := titleFromName(in); got != want {
			t.Errorf("titleFromName(%q) = %q, want %q", in, got, want)
		}
	}
}

var levelsReq = CopyRequest{BaseTitle: "Mona lisa", Template: "levels", Langs: []string{"es", "en"}, Texts: templateTexts["levels"], Shapes: 150}

func TestRulesWriter(t *testing.T) {
	c, _ := rulesWriter{}.Write(context.Background(), levelsReq)
	if c["en"].Texts["question"] != "At which level did you guess it?" || c["en"].Texts["level"] != "Level {nivel} · {formas} shapes" {
		t.Errorf("en texts %v", c["en"].Texts)
	}
	if c["es"].Texts["question"] != "¿En qué nivel la adivinaste?" || c["es"].Title != "Mona lisa" {
		t.Errorf("es %+v", c["es"])
	}
	if !strings.Contains(c["en"].Description, "#Shorts") || !strings.Contains(c["en"].Description, "150 geometric shapes") {
		t.Errorf("en description %q", c["en"].Description)
	}
	if c["en"].Tags[0] != "mona" {
		t.Errorf("tags %v", c["en"].Tags)
	}
}

// The models' answer, as both providers return it.
const modelAnswer = `{"languages":[
 {"lang":"es","title":"La Mona Lisa hecha de triángulos","hook":"Mona Lisa en formas","closing":"¿Cuál sigue? 👇","description":"La Mona Lisa con 150 triángulos. #Shorts #arte","tags":["#Arte","mona lisa"],"texts":[{"role":"question","text":"¿En qué nivel la adivinaste?"},{"role":"level","text":"Nivel {nivel} · {formas} formas"}]},
 {"lang":"en","title":"The Mona Lisa made of triangles","hook":"Mona Lisa in shapes","closing":"What's next? 👇","description":"The Mona Lisa with 150 triangles. #Shorts #art","tags":["art","mona lisa"],"texts":[{"role":"question","text":"At what level did you get it?"},{"role":"level","text":"Level {nivel} · {formas} shapes"},{"role":"bogus","text":"x"}]}
]}`

func checkModelCopy(t *testing.T, c map[string]LangCopy) {
	t.Helper()
	if c["en"].Title != "The Mona Lisa made of triangles" || c["en"].Texts["question"] != "At what level did you get it?" {
		t.Errorf("en %+v", c["en"])
	}
	if _, ok := c["en"].Texts["bogus"]; ok {
		t.Error("kept a role the template doesn't have")
	}
	// Missing roles come from the rules.
	if c["en"].Texts["closing"] != "Comment the level you guessed it at 👇" {
		t.Errorf("closing not filled: %q", c["en"].Texts["closing"])
	}
	if c["es"].Tags[0] != "arte" {
		t.Errorf("tags not cleaned: %v", c["es"].Tags)
	}
}

func TestLocalWriter(t *testing.T) {
	var got map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		json.NewDecoder(r.Body).Decode(&got)
		// Local models sometimes wrap the JSON in a code fence.
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": "```json\n" + modelAnswer + "\n```"}}}})
	}))
	defer ts.Close()
	w := newCopywriter(LLMSettings{Provider: "local", BaseURL: ts.URL + "/v1/", Model: "llama3.1"})
	c, err := w.Write(context.Background(), levelsReq)
	if err != nil {
		t.Fatal(err)
	}
	checkModelCopy(t, c)
	if got["model"] != "llama3.1" || got["response_format"].(map[string]any)["type"] != "json_object" {
		t.Errorf("request %v", got)
	}
}

func TestClaudeWriter(t *testing.T) {
	var body map[string]any
	var beta string
	status := 200
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &body)
		beta = r.Header.Get("anthropic-beta")
		w.Header().Set("Content-Type", "application/json")
		if status != 200 {
			w.WriteHeader(status)
			w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-opus-5-5",
			"stop_reason": "end_turn", "content": []any{map[string]any{"type": "text", "text": modelAnswer}},
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 10},
		})
	}))
	defer ts.Close()
	t.Setenv("ANTHROPIC_BASE_URL", ts.URL)
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("LLM_MODEL", "")

	w := newCopywriter(LLMSettings{}) // automatic: there's a key
	if !strings.Contains(w.Name(), "claude-opus-5-5") {
		t.Fatalf("writer %s", w.Name())
	}
	req := levelsReq
	req.Frame = []byte{0xff, 0xd8, 0xff}
	c, err := w.Write(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	checkModelCopy(t, c)
	oc := body["output_config"].(map[string]any)
	if body["model"] != "claude-opus-5-5" || oc["format"].(map[string]any)["type"] != "json_schema" || oc["effort"] != "low" || body["fallbacks"] != "default" {
		t.Errorf("request %v", body)
	}
	if !strings.Contains(beta, "server-side-fallback-2026-07-01") {
		t.Errorf("beta header %q", beta)
	}
	content := body["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if content[0].(map[string]any)["type"] != "image" || !strings.Contains(content[1].(map[string]any)["text"].(string), "Mona lisa") {
		t.Errorf("content %v", content)
	}

	// A bad key: a clear error (the generator then uses the rules).
	status = 401
	if _, err := w.Write(context.Background(), req); err == nil || !strings.Contains(err.Error(), "API key") {
		t.Errorf("401: %v", err)
	}
}

// Each source becomes one job per template and language; the texts are
// written once per template (all languages) and the page gets them by role.
func TestGeneratorLanguages(t *testing.T) {
	needFFmpeg(t)
	g, dir, now := genFixture(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("LLM_BASE_URL", "")
	g.SetSettings(GenSettings{Enabled: true, SkipChoice: true, Templates: []string{"levels"}, Languages: []string{"es", "EN", "en", "xx1"}})
	if strings.Join(g.state.Settings.Languages, ",") != "es,en" {
		t.Fatalf("languages %v", g.state.Settings.Languages)
	}
	src := filepath.Join(g.in, "mona_lisa.png")
	makeMedia(t, g.in, "mona_lisa.png", "-f", "lavfi", "-i", "color=red:size=64x64", "-frames:v", "1")
	old := now.Add(-time.Minute)
	os.Chtimes(src, old, old)
	g.scan()
	g.scan()
	if js := jobsOf(g)["mona_lisa.png"]; strings.Join(js, ",") != "levels:queued,levels:queued" || g.state.Jobs[1].Lang != "en" {
		t.Fatalf("jobs %v", js)
	}

	srv := newServer(filepath.Join(dir, "data"), filepath.Join(dir, "reels"))
	os.MkdirAll(srv.dataDir, 0o755)
	srv.gen = g
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()

	j := g.next()
	g.ensureCopy(context.Background(), j) // what run() does first
	if len(g.state.Copies) != 1 || g.state.Copies["mona_lisa.png|levels"]["en"].Title != "Mona lisa" {
		t.Fatalf("copies %+v", g.state.Copies)
	}
	res, _ := http.Get(ts.URL + "/api/auto/jobs/" + g.state.Jobs[1].ID)
	var spec struct {
		Lang    string
		Phrases map[string]string
	}
	json.NewDecoder(res.Body).Decode(&spec)
	if spec.Lang != "en" || spec.Phrases["question"] != "At which level did you guess it?" || spec.Phrases["title"] != "Mona lisa" {
		t.Errorf("spec %+v", spec)
	}
	// The finished reel carries its language's copy.
	video := silentVideo(t, t.TempDir(), 1)
	body, ct := multipartBody(t, map[string]string{"video": video}, map[string]string{"job": j.ID, "meta": "{}"})
	if res, err := http.Post(ts.URL+"/api/reels/mux", ct, body); err != nil || res.StatusCode != 200 {
		t.Fatalf("mux: %v %v", err, res.Status)
	}
	side := readSidecar(filepath.Join(g.out, "mona_lisa · En qué nivel · es.mp4"))
	if side["lang"] != "es" || side["title"] != "Mona lisa" || !strings.Contains(side["description"].(string), "#Shorts") || side["tags"] == nil {
		t.Errorf("sidecar %v", side)
	}
}

// The local model's name never reaches Claude (it would answer 404).
func TestClaudeModel(t *testing.T) {
	t.Setenv("LLM_MODEL", "llama3.1")
	t.Setenv("CLAUDE_MODEL", "")
	for chosen, want := range map[string]string{
		"":                  defaultClaudeModel,
		"llama3.1":          defaultClaudeModel,
		"claude-sonnet-5-5": "claude-sonnet-5-5",
	} {
		if got := claudeModel(chosen); got != want {
			t.Errorf("claudeModel(%q) = %q, want %q", chosen, got, want)
		}
	}
	t.Setenv("CLAUDE_MODEL", "claude-haiku-5-5")
	if got := claudeModel(""); got != "claude-haiku-5-5" {
		t.Errorf("CLAUDE_MODEL: %q", got)
	}
}
