package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The review steps: choose one version per source and language (the rest
// are discarded), refine it from the editor, then send it to the queue.
func TestReview(t *testing.T) {
	needFFmpeg(t)
	g, dir, _ := genFixture(t)
	video := silentVideo(t, t.TempDir(), 1)
	b, _ := os.ReadFile(video)
	// Four versions of one source (two templates × two languages) and one of another.
	versions := []struct{ name, src, tpl, lang string }{
		{"mona · Revelado · es.mp4", "mona.png", "reveal", "es"},
		{"mona · Revelado · en.mp4", "mona.png", "reveal", "en"},
		{"mona · En qué nivel · es.mp4", "mona.png", "levels", "es"},
		{"mona · En qué nivel · en.mp4", "mona.png", "levels", "en"},
		{"otra · Revelado · es.mp4", "otra.png", "reveal", "es"},
	}
	for _, v := range versions {
		p := filepath.Join(g.out, v.name)
		os.WriteFile(p, b, 0o644)
		writeSidecar(p, map[string]any{"source": v.src, "template": v.tpl, "lang": v.lang, "title": "T " + v.lang, "description": "d", "settings": map[string]any{"template": v.tpl}})
	}
	os.WriteFile(filepath.Join(g.in, processedDir, "mona.png"), []byte("png"), 0o644)

	srv := newServer(filepath.Join(dir, "data"), filepath.Join(dir, "reels"))
	os.MkdirAll(srv.dataDir, 0o755)
	srv.gen = g
	pub, _ := newPublisher(filepath.Join(dir, "pub"), simulatedUploader{})
	srv.pub = pub
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()
	call := func(method, path string) (*http.Response, ReviewOverview) {
		req, _ := http.NewRequest(method, ts.URL+path, nil)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var o ReviewOverview
		json.NewDecoder(res.Body).Decode(&o)
		return res, o
	}
	esc := url.PathEscape

	_, o := call("GET", "/api/review")
	if len(o.Groups) != 2 {
		t.Fatalf("groups %+v", o.Groups)
	}
	mona := func(o ReviewOverview) ReviewGroup {
		for _, gr := range o.Groups {
			if gr.Source == "mona.png" {
				return gr
			}
		}
		t.Fatal("no mona group")
		return ReviewGroup{}
	}
	if len(mona(o).Versions) != 4 {
		t.Fatalf("mona versions %+v", mona(o).Versions)
	}

	// Choosing the Spanish "levels" discards the Spanish "reveal" only.
	res, o := call("POST", "/api/review/"+esc("mona · En qué nivel · es.mp4")+"/choose")
	if res.StatusCode != 200 {
		t.Fatalf("choose: %d", res.StatusCode)
	}
	m := mona(o)
	stages := map[string]string{}
	for _, v := range m.Versions {
		stages[v.Name] = v.Stage
	}
	// The same template is chosen in English too (nothing chosen there yet).
	if len(m.Versions) != 2 || stages["mona · En qué nivel · es.mp4"] != "chosen" || stages["mona · En qué nivel · en.mp4"] != "chosen" || len(m.Discarded) != 2 {
		t.Fatalf("after choose: %v, discarded %+v", stages, m.Discarded)
	}
	// The editor gets the other language as a sibling.
	res, _ = http.Get(ts.URL + "/api/review/" + esc("mona · En qué nivel · es.mp4") + "/spec")
	var spec struct{ Siblings []struct{ Name, Lang string } }
	json.NewDecoder(res.Body).Decode(&spec)
	if len(spec.Siblings) != 1 || spec.Siblings[0].Lang != "en" {
		t.Errorf("siblings %+v", spec.Siblings)
	}
	if _, err := os.Stat(filepath.Join(g.out, discardedDir, "mona · Revelado · es.json")); err != nil {
		t.Error("discarded JSON not moved")
	}
	// The other source is untouched.
	for _, gr := range o.Groups {
		if gr.Source == "otra.png" && len(gr.Versions) != 1 {
			t.Error("another source's version was discarded")
		}
	}

	// Not refined yet: it can't go to the queue.
	if res, _ := call("POST", "/api/review/"+esc("mona · En qué nivel · es.mp4")+"/queue"); res.StatusCode != http.StatusConflict {
		t.Errorf("queue before refining: %d", res.StatusCode)
	}

	// The editor gets its settings and source.
	res, _ = call("GET", "/api/review/"+esc("mona · En qué nivel · es.mp4")+"/spec")
	if res.StatusCode != 200 {
		t.Fatalf("spec: %d", res.StatusCode)
	}
	if res, _ := call("GET", "/api/review/"+esc("mona · En qué nivel · es.mp4")+"/source"); res.StatusCode != 200 {
		t.Errorf("source from procesados/: %d", res.StatusCode)
	}

	// Refined through mux: saved over itself.
	body, ct := multipartBody(t, map[string]string{"video": video}, map[string]string{"refine": "mona · En qué nivel · es.mp4", "meta": `{"template":"levels","shapes":{"shapes":99}}`})
	if r, err := http.Post(ts.URL+"/api/reels/mux", ct, body); err != nil || r.StatusCode != 200 {
		t.Fatalf("refine: %v %v", err, r.Status)
	}
	side := readSidecar(filepath.Join(g.out, "mona · En qué nivel · es.mp4"))
	if side["stage"] != "refined" || !strings.Contains(string(mustJSON(side["settings"])), "99") {
		t.Errorf("after refine: %v", side)
	}

	// Now to the queue (the publishing folder gets it, with its title).
	if res, _ := call("POST", "/api/review/"+esc("mona · En qué nivel · es.mp4")+"/queue"); res.StatusCode != 200 {
		t.Fatalf("queue: %d", res.StatusCode)
	}
	if q := pub.Overview().Queue; len(q) != 1 || q[0].Title != "T es" {
		t.Errorf("publishing queue %+v", q)
	}

	// Restore a discarded one and choose it: English already has its choice,
	// so only Spanish changes.
	if res, _ := call("POST", "/api/review/"+esc("mona · Revelado · en.mp4")+"/restore"); res.StatusCode != 200 {
		t.Errorf("restore: %d", res.StatusCode)
	}
	if res, _ := call("POST", "/api/review/"+esc("mona · Revelado · es.mp4")+"/restore"); res.StatusCode != 200 {
		t.Errorf("restore: %d", res.StatusCode)
	}
	_, o = call("POST", "/api/review/"+esc("mona · Revelado · es.mp4")+"/choose")
	stages = map[string]string{}
	for _, v := range mona(o).Versions {
		stages[v.Name] = v.Stage
	}
	if stages["mona · Revelado · es.mp4"] != "chosen" || stages["mona · En qué nivel · en.mp4"] != "chosen" || stages["mona · Revelado · en.mp4"] != "generated" {
		t.Errorf("choosing again: %v", stages)
	}
	_, o = call("POST", "/api/review/"+esc("mona · Revelado · en.mp4")+"/discard")
	if m := mona(o); len(m.Versions) != 2 {
		t.Errorf("after discarding: %d versions", len(m.Versions))
	}
	if res, _ := call("POST", "/api/review/"+esc("../x.mp4")+"/choose"); res.StatusCode != 400 && res.StatusCode != 404 {
		t.Errorf("path outside: %d", res.StatusCode)
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
