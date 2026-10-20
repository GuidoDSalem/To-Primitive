package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// Photos and videos uploaded from the Generar page land in the input folder
// with their versions queued at once (a name in use gets another).
func TestUpload(t *testing.T) {
	g, dir, _ := genFixture(t)
	g.state.Settings.Enabled = true
	os.WriteFile(filepath.Join(g.in, processedDir, "mona.png"), []byte("old"), 0o644)
	srv := newServer(filepath.Join(dir, "data"), filepath.Join(dir, "reels"))
	srv.gen = g
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()

	post := func(files map[string]string) (*http.Response, []string) {
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		for name, body := range files {
			fw, _ := w.CreateFormFile("file", name)
			fw.Write([]byte(body))
		}
		w.Close()
		res, err := http.Post(ts.URL+"/api/auto/inputs", w.FormDataContentType(), &buf)
		if err != nil {
			t.Fatal(err)
		}
		var out struct{ Names []string }
		json.NewDecoder(res.Body).Decode(&out)
		return res, out.Names
	}
	res, names := post(map[string]string{"mona.png": "png", "clip.mp4": "mp4"})
	if res.StatusCode != 200 || len(names) != 2 {
		t.Fatalf("upload: %d %v", res.StatusCode, names)
	}
	got := map[string]bool{}
	for _, n := range names {
		got[n] = true
	}
	if !got["mona (2).png"] || !got["clip.mp4"] {
		t.Errorf("names %v", names)
	}
	if b, _ := os.ReadFile(filepath.Join(g.in, "mona (2).png")); string(b) != "png" {
		t.Errorf("saved %q", b)
	}
	jobs := jobsOf(g)
	if len(jobs["mona (2).png"]) != len(g.templatesFor("image")) || len(jobs["clip.mp4"]) != len(g.templatesFor("video")) {
		t.Errorf("jobs %v", jobs)
	}
	// The scan doesn't queue them again, and leaves nothing half written.
	g.scan()
	g.scan()
	if j := jobsOf(g); len(j["clip.mp4"]) != len(jobs["clip.mp4"]) {
		t.Errorf("queued twice: %v", j)
	}
	entries, _ := os.ReadDir(g.in)
	for _, e := range entries {
		if !e.IsDir() && sourceKind(e.Name()) == "" {
			t.Errorf("left over %s", e.Name())
		}
	}
	if res, _ := post(map[string]string{"notas.txt": "x"}); res.StatusCode != 400 {
		t.Errorf("a text file: %d", res.StatusCode)
	}
}
