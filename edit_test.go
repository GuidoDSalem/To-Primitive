package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A queued reel reopened in the editor (with its other language) and saved
// again over itself, keeping its place and texts.
func TestEditQueued(t *testing.T) {
	needFFmpeg(t)
	g, dir, now := genFixture(t)
	pub, _ := newPublisher(filepath.Join(dir, "pub"), simulatedUploader{})
	pub.now = func() time.Time { return *now }
	os.WriteFile(filepath.Join(g.in, processedDir, "mona.png"), []byte("png"), 0o644)
	video := silentVideo(t, t.TempDir(), 1)
	for _, q := range []struct{ name, lang, src string }{
		{"mona · es.mp4", "es", "mona.png"}, {"mona · en.mp4", "en", "mona.png"}, {"otro.mp4", "", ""},
	} {
		meta := map[string]any{"title": "T " + q.lang}
		if q.src != "" {
			meta = map[string]any{"source": q.src, "template": "levels", "lang": q.lang, "title": "T " + q.lang, "stage": stageRefined,
				"settings": map[string]any{"lang": q.lang, "phrases": []any{map[string]any{"role": "title", "text": "hola " + q.lang}}}}
		}
		if _, err := pub.Add(video, q.name, meta, "test"); err != nil {
			t.Fatal(err)
		}
	}
	srv := newServer(filepath.Join(dir, "data"), filepath.Join(dir, "reels"))
	os.MkdirAll(srv.dataDir, 0o755)
	srv.gen, srv.pub = g, pub
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()

	q := pub.Overview().Queue
	if !q[0].Editable || !q[1].Editable || q[2].Editable {
		t.Errorf("editable: %v %v %v", q[0].Editable, q[1].Editable, q[2].Editable)
	}
	if res, _ := http.Get(ts.URL + "/api/publish/queue/otro.mp4/spec"); res.StatusCode != http.StatusConflict {
		t.Errorf("spec of a video without settings: %d", res.StatusCode)
	}
	res, _ := http.Get(ts.URL + "/api/publish/queue/" + url.PathEscape("mona · es.mp4") + "/spec")
	var spec struct {
		Lang     string
		Source   struct{ Name, Kind string }
		Siblings []struct{ Name, Lang string }
	}
	json.NewDecoder(res.Body).Decode(&spec)
	if spec.Lang != "es" || spec.Source.Name != "mona.png" || len(spec.Siblings) != 1 || spec.Siblings[0].Name != "mona · en.mp4" {
		t.Fatalf("spec %+v", spec)
	}
	if res, _ := http.Get(ts.URL + "/api/publish/queue/" + url.PathEscape("mona · es.mp4") + "/source"); res.StatusCode != 200 {
		t.Errorf("source: %d", res.StatusCode)
	}

	before := names(q)
	*now = now.Add(time.Minute)
	body, ct := multipartBody(t, map[string]string{"video": video}, map[string]string{"queue": "mona · en.mp4", "meta": `{"lang":"en","phrases":[{"role":"title","text":"hello"}]}`})
	if r, err := http.Post(ts.URL+"/api/reels/mux", ct, body); err != nil || r.StatusCode != 200 {
		t.Fatalf("mux: %v %v", err, r.Status)
	}
	q = pub.Overview().Queue
	if names(q) != before {
		t.Fatalf("queue %s, was %s", names(q), before)
	}
	for _, it := range q {
		if it.Name == "mona · en.mp4" && it.Title != "T en" {
			t.Errorf("title %q", it.Title)
		}
	}
	side := readSidecar(filepath.Join(pub.dir, "mona · en.mp4"))
	if s, _ := json.Marshal(side["settings"]); string(s) != `{"lang":"en","phrases":[{"role":"title","text":"hello"}]}` || side["edited"] == nil {
		t.Errorf("sidecar %v", side)
	}
	body, ct = multipartBody(t, map[string]string{"video": video}, map[string]string{"queue": "no está.mp4"})
	if r, _ := http.Post(ts.URL+"/api/reels/mux", ct, body); r.StatusCode != http.StatusNotFound {
		t.Errorf("replacing a missing video: %d", r.StatusCode)
	}
}
