package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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
	versions := map[string]string{}
	for _, it := range q {
		versions[it.Name] = it.Version
	}
	*now = now.Add(time.Minute)
	// What the browser has of the video before the edit.
	videoURL := ts.URL + "/api/publish/queue/" + url.PathEscape("mona · en.mp4") + "/video"
	first, _ := http.Get(videoURL)
	io.Copy(io.Discard, first.Body)
	etag, lastMod := first.Header.Get("ETag"), first.Header.Get("Last-Modified")
	if etag == "" || first.Header.Get("Cache-Control") != "no-cache" {
		t.Errorf("headers %v", first.Header)
	}
	body, ct := multipartBody(t, map[string]string{"video": video}, map[string]string{"queue": "mona · en.mp4", "meta": `{"lang":"en","phrases":[{"role":"title","text":"hello"}]}`})
	if r, err := http.Post(ts.URL+"/api/reels/mux", ct, body); err != nil || r.StatusCode != 200 {
		t.Fatalf("mux: %v %v", err, r.Status)
	}
	q = pub.Overview().Queue
	if names(q) != before {
		t.Fatalf("queue %s, was %s", names(q), before)
	}
	// The edited video keeps its date, but the browser's copy is no longer good.
	req, _ := http.NewRequest("GET", videoURL, nil)
	req.Header.Set("If-None-Match", etag)
	req.Header.Set("If-Modified-Since", lastMod)
	if again, _ := http.DefaultClient.Do(req); again.StatusCode != 200 {
		t.Errorf("edited video answered %d to the old copy", again.StatusCode)
	}
	for _, it := range q {
		if it.Name == "mona · en.mp4" && it.Title != "T en" {
			t.Errorf("title %q", it.Title)
		}
		if changed := it.Version != versions[it.Name]; changed != (it.Name == "mona · en.mp4") {
			t.Errorf("%s: version %s → %s", it.Name, versions[it.Name], it.Version)
		}
	}
	side := readSidecar(filepath.Join(pub.dir, "mona · en.mp4"))
	if s, _ := json.Marshal(side["settings"]); string(s) != `{"lang":"en","phrases":[{"role":"title","text":"hello"}]}` || side["edited"] == nil {
		t.Errorf("sidecar %v", side)
	}
	// With a song: it's kept, and the record says where, to put it back.
	song := sine(t, t.TempDir(), "mi canción.wav", 2)
	body, ct = multipartBody(t, map[string]string{"video": video, "audio": song}, map[string]string{"queue": "mona · es.mp4", "meta": `{"lang":"es","audio":{"mode":"file","name":"mi canción.wav","start":0.5}}`})
	if r, err := http.Post(ts.URL+"/api/reels/mux", ct, body); err != nil || r.StatusCode != 200 {
		t.Fatalf("mux with a song: %v %v", err, r.Status)
	}
	for _, it := range pub.Overview().Queue {
		if it.Name == "mona · es.mp4" && strings.Contains(strings.Join(it.Warnings, " "), "sonido") {
			t.Errorf("with its song: %v", it.Warnings)
		}
	}
	audio, _ := readSidecar(filepath.Join(pub.dir, "mona · es.mp4"))["settings"].(map[string]any)["audio"].(map[string]any)
	kept, _ := audio["kept"].(string)
	if !strings.HasSuffix(kept, "/mi canción.wav") || audio["start"] != 0.5 {
		t.Fatalf("audio record %v", audio)
	}
	res, _ = http.Get(ts.URL + "/api/reels/files/" + strings.TrimPrefix(filepath.Dir(kept), "sources/") + "/" + url.PathEscape("mi canción.wav"))
	want, _ := os.ReadFile(song)
	if b, _ := io.ReadAll(res.Body); !bytes.Equal(b, want) {
		t.Errorf("kept song: %d, %d bytes", res.StatusCode, len(b))
	}
	if r, _ := http.Get(ts.URL + "/api/reels/files/xyz/..%2f..%2fetc"); r.StatusCode == 200 {
		t.Error("served outside the kept files")
	}

	body, ct = multipartBody(t, map[string]string{"video": video}, map[string]string{"queue": "mona · en.mp4", "meta": `{"lang":"en","audio":{"mode":"file","name":"x.wav"}}`})
	http.Post(ts.URL+"/api/reels/mux", ct, body) // no audio field: a silent video
	for _, it := range pub.Overview().Queue {
		if it.Name == "mona · en.mp4" && !strings.Contains(strings.Join(it.Warnings, " "), "no tiene sonido") {
			t.Errorf("silent but with a song in its record: %v", it.Warnings)
		}
	}
	body, ct = multipartBody(t, map[string]string{"video": video}, map[string]string{"queue": "no está.mp4"})
	if r, _ := http.Post(ts.URL+"/api/reels/mux", ct, body); r.StatusCode != http.StatusNotFound {
		t.Errorf("replacing a missing video: %d", r.StatusCode)
	}
}

// A library reel saved with its photo, reopened, saved again over itself,
// and editable from the queue too.
func TestLibraryEdit(t *testing.T) {
	needFFmpeg(t)
	dir := t.TempDir()
	srv := newServer(filepath.Join(dir, "data"), filepath.Join(dir, "reels"))
	os.MkdirAll(srv.dataDir, 0o755)
	os.MkdirAll(srv.lib.dir, 0o755)
	pub, _ := newPublisher(filepath.Join(dir, "pub"), simulatedUploader{})
	srv.pub = pub
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()
	video := silentVideo(t, t.TempDir(), 1)
	photo := filepath.Join(t.TempDir(), "mona lisa.png")
	os.WriteFile(photo, []byte("png bytes"), 0o644)

	save := func() ReelEntry {
		body, ct := multipartBody(t, map[string]string{"video": video, "src": photo}, map[string]string{"save": "1", "meta": `{"template":"reveal","phrases":[{"role":"title","text":"uno"}]}`})
		res, err := http.Post(ts.URL+"/api/reels/mux", ct, body)
		if err != nil || res.StatusCode != 200 {
			t.Fatalf("save: %v %v", err, res.Status)
		}
		var e ReelEntry
		json.NewDecoder(res.Body).Decode(&e)
		return e
	}
	e, e2 := save(), save()
	if e.Source == "" || e.Source != e2.Source {
		t.Fatalf("source kept once for both: %q %q", e.Source, e2.Source)
	}
	res, _ := http.Get(ts.URL + "/api/reels/" + e.ID + "/spec")
	var spec struct {
		Source   struct{ Name, Kind string }
		Settings struct{ Template string }
	}
	json.NewDecoder(res.Body).Decode(&spec)
	if spec.Source.Name != "mona lisa.png" || spec.Source.Kind != "image" || spec.Settings.Template != "reveal" {
		t.Fatalf("spec %+v", spec)
	}
	res, _ = http.Get(ts.URL + "/api/reels/" + e.ID + "/source")
	if b, _ := io.ReadAll(res.Body); string(b) != "png bytes" {
		t.Errorf("source %q", b)
	}

	body, ct := multipartBody(t, map[string]string{"video": video}, map[string]string{"lib": e.ID, "meta": `{"template":"reveal","phrases":[{"role":"title","text":"dos"}]}`})
	if r, err := http.Post(ts.URL+"/api/reels/mux", ct, body); err != nil || r.StatusCode != 200 {
		t.Fatalf("replace: %v %v", err, r.Status)
	}
	list, _ := srv.lib.List()
	if len(list) != 2 {
		t.Fatalf("%d reels", len(list))
	}
	got, _ := srv.lib.Get(e.ID)
	var meta bytes.Buffer
	json.Compact(&meta, got.Meta)
	if meta.String() != `{"template":"reveal","phrases":[{"role":"title","text":"dos"}]}` || got.Edited == nil || got.Source != e.Source {
		t.Errorf("replaced %+v", got)
	}

	// Sent to the queue: editable there too (from the library's source).
	if r, _ := http.Post(ts.URL+"/api/reels/"+e.ID+"/queue", "", nil); r.StatusCode != 200 {
		t.Fatalf("queue: %d", r.StatusCode)
	}
	q := pub.Overview().Queue
	if len(q) != 1 || !q[0].Editable || q[0].Duplicable {
		t.Fatalf("queued %+v", q)
	}
	res, _ = http.Get(ts.URL + "/api/publish/queue/" + url.PathEscape(q[0].Name) + "/spec")
	var qs struct {
		Source   struct{ Name string }
		Siblings []any
	}
	json.NewDecoder(res.Body).Decode(&qs)
	if qs.Source.Name != "mona lisa.png" || len(qs.Siblings) != 0 {
		t.Errorf("queue spec %+v", qs)
	}

	// The source goes with the last reel that uses it.
	src := filepath.Join(srv.lib.dir, e.Source)
	srv.lib.Delete(e.ID)
	if _, err := os.Stat(src); err != nil {
		t.Errorf("source removed while another reel uses it")
	}
	srv.lib.Delete(e2.ID)
	if _, err := os.Stat(src); err == nil {
		t.Errorf("unused source kept")
	}
}
