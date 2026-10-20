package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// fakeTranslator answers like a local model: it takes the JSON in the
// prompt and returns it "translated" (prefixed), or broken if asked.
func fakeTranslator(t *testing.T, breakPlaceholders *bool) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct{ Content string }
		}
		json.NewDecoder(r.Body).Decode(&req)
		user := req.Messages[len(req.Messages)-1].Content
		in := Copy{}
		json.Unmarshal([]byte(user[strings.Index(user, "{\""):]), &in)
		out := Copy{Title: "[fr] " + in.Title, Description: "[fr] " + in.Description, Tags: []string{"#Art", "fr"}}
		for _, s := range in.Texts {
			if *breakPlaceholders {
				s = regexp.MustCompile(`\{\w+\}`).ReplaceAllString(s, "X")
			}
			out.Texts = append(out.Texts, "[fr] "+s)
		}
		b, _ := json.Marshal(out)
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(b)}}}})
	}))
}

func TestTranslate(t *testing.T) {
	broken := false
	ts := fakeTranslator(t, &broken)
	defer ts.Close()
	w := localTranslator(LLMSettings{Provider: "local", BaseURL: ts.URL, Model: "m"})
	in := Copy{Title: "Mona Lisa", Description: "d #Shorts", Texts: []string{"¿En qué nivel?", "Nivel {nivel} · {formas} formas"}}
	out, err := w.Translate(context.Background(), "es", "fr", in)
	if err != nil {
		t.Fatal(err)
	}
	if out.Texts[1] != "[fr] Nivel {nivel} · {formas} formas" || out.Tags[0] != "art" || out.Title != "[fr] Mona Lisa" {
		t.Errorf("out %+v", out)
	}
	broken = true
	if _, err := w.Translate(context.Background(), "es", "fr", in); err == nil || !strings.Contains(err.Error(), "{nivel}") {
		t.Errorf("lost placeholder not caught: %v", err)
	}
	// Not the generator's Claude: translations always go to the local model.
	if !strings.Contains(localTranslator(LLMSettings{Provider: "anthropic"}).Name(), "modelo local") {
		t.Error("translator is not local")
	}
}

func TestCopyName(t *testing.T) {
	for in, want := range map[string]string{
		"mona · En qué nivel · es.mp4": "mona · En qué nivel · fr.mp4",
		"mi video.mp4":                 "mi video · fr.mp4",
	} {
		if got := copyName(in, "fr"); got != want {
			t.Errorf("copyName(%q) = %q, want %q", in, got, want)
		}
	}
}

// A queued reel duplicated in French: translated, drawn again (here the
// page's part is the mux call), and placed right after the original.
func TestDuplicate(t *testing.T) {
	needFFmpeg(t)
	broken := false
	llm := fakeTranslator(t, &broken)
	defer llm.Close()
	g, dir, now := genFixture(t)
	g.state.Settings.LLM = LLMSettings{Provider: "local", BaseURL: llm.URL, Model: "m"}
	pub, _ := newPublisher(filepath.Join(dir, "pub"), simulatedUploader{})
	pub.now = func() time.Time { return *now }
	g.pub = pub
	os.WriteFile(filepath.Join(g.in, processedDir, "mona.png"), []byte("png"), 0o644)
	video := silentVideo(t, t.TempDir(), 1)
	settings := map[string]any{"template": "levels", "lang": "es", "phrases": []any{
		map[string]any{"role": "question", "text": "¿En qué nivel?", "from": 0.01, "to": 0.8, "pos": "top", "style": "question"},
		map[string]any{"role": "level", "text": "Nivel {nivel}", "from": 0, "to": 0.8, "pos": "foot", "style": "label"},
	}}
	for i, n := range []string{"mona · En qué nivel · es.mp4", "otro.mp4"} {
		meta := map[string]any{"title": "T", "description": "D"}
		if i == 0 {
			meta = map[string]any{"source": "mona.png", "template": "levels", "lang": "es", "title": "Mona Lisa", "description": "Hecha con formas #Shorts", "tags": []string{"arte"}, "settings": settings}
		}
		if _, err := pub.Add(video, n, meta, "test"); err != nil {
			t.Fatal(err)
		}
	}
	srv := newServer(filepath.Join(dir, "data"), filepath.Join(dir, "reels"))
	os.MkdirAll(srv.dataDir, 0o755)
	srv.gen, srv.pub = g, pub
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()
	dup := func(name, lang string) *http.Response {
		res, _ := http.Post(ts.URL+"/api/publish/queue/"+url.PathEscape(name)+"/duplicate", "application/json", strings.NewReader(`{"lang":"`+lang+`"}`))
		return res
	}
	if res := dup("otro.mp4", "fr"); res.StatusCode != 400 {
		t.Errorf("a video without record: %d", res.StatusCode)
	}
	if res := dup("mona · En qué nivel · es.mp4", "es"); res.StatusCode != 400 {
		t.Errorf("same language: %d", res.StatusCode)
	}
	if res := dup("mona · En qué nivel · es.mp4", "fr"); res.StatusCode != 200 {
		t.Fatalf("duplicate: %d", res.StatusCode)
	}
	// Runs even with the generator paused; the translation happens first.
	g.state.Settings.Enabled = false
	j := g.next()
	if j == nil || j.Dest != "queue" {
		t.Fatalf("job %+v", j)
	}
	if err := g.translate(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	res, _ := http.Get(ts.URL + "/api/auto/jobs/" + j.ID)
	var spec struct {
		Lang   string
		Record struct {
			Lang    string
			Phrases []struct{ Text string }
		}
	}
	json.NewDecoder(res.Body).Decode(&spec)
	if spec.Lang != "fr" || spec.Record.Lang != "fr" || spec.Record.Phrases[1].Text != "[fr] Nivel {nivel}" {
		t.Fatalf("spec %+v", spec)
	}
	// The page sends the reel; it lands in the queue after the original.
	body, ct := multipartBody(t, map[string]string{"video": video}, map[string]string{"job": j.ID, "meta": `{"lang":"fr"}`})
	if r, err := http.Post(ts.URL+"/api/reels/mux", ct, body); err != nil || r.StatusCode != 200 {
		t.Fatalf("mux: %v %v", err, r.Status)
	}
	q := pub.Overview().Queue
	if names(q) != "mona · En qué nivel · es.mp4,mona · En qué nivel · fr.mp4,otro.mp4" {
		t.Fatalf("queue %s", names(q))
	}
	if q[1].Title != "[fr] Mona Lisa" || q[1].Tags[0] != "art" {
		t.Errorf("copy %+v", q[1])
	}
	if res := dup("mona · En qué nivel · es.mp4", "fr"); res.StatusCode != 400 {
		t.Errorf("duplicating twice in the same language: %d", res.StatusCode)
	}
	if j.State != "done" {
		t.Errorf("job %s", j.State)
	}
	// A broken translation fails the job with the reason.
	broken = true
	dup("mona · En qué nivel · es.mp4", "it")
	j2 := g.next()
	g.run(context.Background(), j2) // chrome is "" here, but the translation comes first
	if j2.State != "error" || !strings.Contains(j2.Error, "traducción") {
		t.Errorf("broken translation: %s %q", j2.State, j2.Error)
	}
}
