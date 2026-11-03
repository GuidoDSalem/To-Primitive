package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Duplicating a queued reel in another language: a local model translates
// its texts (on-screen phrases, title, description, tags), the generator
// draws it again with the same settings, and the copy joins the publishing
// queue right after the original.

var langCode = regexp.MustCompile(`^[a-z]{2}$`)

// Duplicate queues the copy of the queued reel `name` in language `lang`.
func (g *generator) Duplicate(pub *publisher, name, lang string) (*GenJob, error) {
	if !langCode.MatchString(lang) {
		return nil, errors.New("idioma inválido (dos letras, como en)")
	}
	pub.mu.Lock()
	path, err := pub.queuedPath(name)
	pub.mu.Unlock()
	if err != nil {
		return nil, err
	}
	side := readSidecar(path)
	settings, _ := side["settings"].(map[string]any)
	src, _ := side["source"].(string)
	if settings == nil || src == "" {
		return nil, errors.New("este video no tiene registro ni fuente (no salió del generador): no se puede rehacer en otro idioma")
	}
	if from, _ := side["lang"].(string); from == lang {
		return nil, errors.New("ya está en ese idioma")
	}
	if _, err := os.Stat(filepath.Join(pub.dir, copyName(name, lang))); err == nil {
		return nil, fmt.Errorf("ya está en la cola en %s", strings.ToUpper(lang))
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, o := range g.state.Jobs {
		if o.Dest == "queue" && o.From == name && o.Lang == lang && (o.State == "queued" || o.State == "running") {
			return nil, fmt.Errorf("ya se está duplicando en %s", strings.ToUpper(lang))
		}
	}
	if _, err := g.sourcePath(src); err != nil {
		return nil, err
	}
	tpl, _ := side["template"].(string)
	if tpl == "" {
		tpl, _ = settings["template"].(string)
	}
	j := &GenJob{ID: newID(), Source: src, Kind: sourceKind(src), Template: tpl, Lang: lang, State: "queued", Created: g.now(), Dest: "queue", From: name}
	g.state.Jobs = append(g.state.Jobs, j)
	g.logLocked(name, "duplicar", "en "+strings.ToUpper(lang)+": traducir y generar")
	g.saveLocked()
	g.poke()
	return j, nil
}

// translate writes the copy's texts in its language (once: a retry keeps
// them) from the queued reel it copies.
func (g *generator) translate(ctx context.Context, j *GenJob) error {
	g.mu.Lock()
	if j.Record != nil && j.Copy != nil {
		g.mu.Unlock()
		return nil
	}
	j.Stage = "traduciendo los textos"
	llm := g.state.Settings.LLM
	g.mu.Unlock()

	path := filepath.Join(g.pub.dir, j.From)
	side := readSidecar(path)
	if len(side) == 0 { // published meanwhile?
		side = readSidecar(filepath.Join(g.pub.dir, publishedDir, j.From))
	}
	settings, _ := side["settings"].(map[string]any)
	if settings == nil {
		return errors.New("el original ya no está en la cola")
	}
	from, _ := side["lang"].(string)
	if from == "" {
		from, _ = settings["lang"].(string)
	}
	in := Copy{}
	in.Title, _ = side["title"].(string)
	in.Description, _ = side["description"].(string)
	if tags, ok := side["tags"].([]any); ok {
		for _, t := range tags {
			if s, ok := t.(string); ok {
				in.Tags = append(in.Tags, s)
			}
		}
	}
	phrases, _ := settings["phrases"].([]any)
	for _, ph := range phrases {
		m, _ := ph.(map[string]any)
		t, _ := m["text"].(string)
		in.Texts = append(in.Texts, t)
	}
	w := localTranslator(llm)
	out, err := w.Translate(ctx, from, j.Lang, in)
	if err != nil {
		return fmt.Errorf("traducción con %s: %v", w.Name(), err)
	}
	// The same settings with the texts and the language replaced.
	for i, ph := range phrases {
		if m, ok := ph.(map[string]any); ok {
			m["text"] = out.Texts[i]
		}
	}
	settings["lang"] = j.Lang
	record, _ := json.Marshal(settings)
	g.mu.Lock()
	j.Record, j.Copy = record, &out
	g.logLocked(j.From, "traducido", strings.ToUpper(from)+" → "+strings.ToUpper(j.Lang)+" con "+w.Name())
	g.saveLocked()
	g.mu.Unlock()
	return nil
}

// copyName is the original's name with the language changed ("… · es.mp4"
// → "… · en.mp4"), or with it added.
func copyName(orig, lang string) string {
	base := strings.TrimSuffix(orig, filepath.Ext(orig))
	if i := strings.LastIndex(base, " · "); i >= 0 && langCode.MatchString(base[i+len(" · "):]) {
		base = base[:i]
	}
	return base + " · " + lang + ".mp4"
}

// completeCopy puts a finished copy in the queue, after its original.
func (g *generator) completeCopy(j *GenJob, reelPath string, meta json.RawMessage) (string, error) {
	g.mu.Lock()
	c, from := j.Copy, j.From
	g.mu.Unlock()
	if c == nil {
		c = &Copy{}
	}
	side := map[string]any{
		"source": j.Source, "template": j.Template, "lang": j.Lang, "settings": meta,
		"title": c.Title, "description": c.Description, "tags": c.Tags,
		"duplicatedFrom": from, "generated": g.now().Format(time.RFC3339), "stage": stageRefined,
	}
	name, err := g.pub.Add(reelPath, copyName(from, j.Lang), side, "copia de "+from+" en "+strings.ToUpper(j.Lang))
	if err != nil {
		return "", err
	}
	g.pub.PlaceAfter(name, from)
	g.end(j.ID, name, nil)
	return name, nil
}

// PlaceAfter moves `name` right after `after` in the queue's order.
func (p *publisher) PlaceAfter(name, after string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	names := []string{}
	for _, it := range p.queueLocked() {
		if it.Name != name {
			names = append(names, it.Name)
		}
	}
	at := len(names)
	for i, n := range names {
		if n == after {
			at = i + 1
		}
	}
	names = append(names[:at], append([]string{name}, names[at:]...)...)
	p.state.Order = names
	p.saveLocked()
}

// ---------- HTTP ----------

func (srv *server) pubDuplicate(w http.ResponseWriter, r *http.Request, p *publisher) {
	if srv.gen == nil {
		http.Error(w, "generator not configured", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Lang string `json:"lang"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	j, err := srv.gen.Duplicate(p, r.PathValue("name"), strings.ToLower(strings.TrimSpace(body.Lang)))
	if err != nil {
		pubError(w, err)
		return
	}
	writeJSON(w, map[string]string{"job": j.ID})
}

// The copies on their way to the queue (and the failed ones), for the
// publishing page.
func (srv *server) pubCopies(w http.ResponseWriter, r *http.Request, p *publisher) {
	out := []GenJob{}
	if g := srv.gen; g != nil {
		g.mu.Lock()
		for _, j := range g.state.Jobs {
			if j.Dest == "queue" && j.State != "done" {
				c := *j
				c.Record, c.Copy = nil, nil
				out = append(out, c)
			}
		}
		g.mu.Unlock()
	}
	writeJSON(w, out)
}
