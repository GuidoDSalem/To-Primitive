package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// By default a new source (uploaded or dropped in the folder) waits until
// its templates are chosen.
func TestChooseTemplates(t *testing.T) {
	g, _, now := genFixture(t)
	g.state.Settings.Enabled, g.state.Settings.SkipChoice = true, false
	if _, err := g.Upload(bytes.NewReader([]byte("png")), "mona.png"); err != nil {
		t.Fatal(err)
	}
	// Dropped in the folder: taken once it stops changing.
	clip := filepath.Join(g.in, "clip.mp4")
	os.WriteFile(clip, []byte("mp4"), 0o644)
	old := now.Add(-time.Minute)
	os.Chtimes(clip, old, old)
	g.scan()
	g.scan()
	if len(g.state.Jobs) != 0 || len(g.state.Waiting) != 2 {
		t.Fatalf("jobs %v, waiting %d", jobsOf(g), len(g.state.Waiting))
	}
	if _, err := g.Start("mona.png", []string{"shapes"}, nil, nil); err == nil {
		t.Error("a video template for a photo")
	}
	n, err := g.Start("mona.png", []string{"levels", "nope", "reveal"}, []string{"FR", "it"}, nil)
	if err != nil || n != 4 {
		t.Fatalf("start: %d %v", n, err)
	}
	var got []string
	for _, j := range g.state.Jobs {
		got = append(got, j.Template+":"+j.Lang)
	}
	if strings.Join(got, ",") != "reveal:fr,reveal:it,levels:fr,levels:it" {
		t.Errorf("jobs %v", got)
	}
	if _, err := g.Start("mona.png", []string{"reveal"}, nil, nil); err == nil {
		t.Error("started twice")
	}
	g.scan()
	if len(g.state.Waiting) != 1 || len(g.state.Jobs) != 4 {
		t.Errorf("after scan: waiting %d, jobs %d", len(g.state.Waiting), len(g.state.Jobs))
	}
	if err := g.Dismiss("clip.mp4"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(g.in, discardedDir, "clip.mp4")); err != nil || len(g.state.Waiting) != 0 {
		t.Errorf("dismissed: %v, waiting %d", err, len(g.state.Waiting))
	}
	g.scan()
	if len(g.state.Waiting) != 0 {
		t.Error("a dismissed source came back")
	}
}

// A waiting source made by hand: the reel goes to the generated ones,
// already refined, and the source stops waiting.
func TestManual(t *testing.T) {
	needFFmpeg(t)
	g, dir, _ := genFixture(t)
	g.state.Settings.Enabled, g.state.Settings.SkipChoice = true, false
	if _, err := g.Upload(bytes.NewReader([]byte("png")), "mona_lisa.png"); err != nil {
		t.Fatal(err)
	}
	srv := newServer(filepath.Join(dir, "data"), filepath.Join(dir, "reels"))
	os.MkdirAll(srv.dataDir, 0o755)
	srv.gen = g
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()
	res, _ := http.Get(ts.URL + "/api/auto/inputs/mona_lisa.png/spec")
	var spec struct{ Kind, Title, Lang string }
	json.NewDecoder(res.Body).Decode(&spec)
	if spec.Kind != "image" || spec.Title != "Mona lisa" || spec.Lang != "es" {
		t.Errorf("spec %+v", spec)
	}
	video := silentVideo(t, t.TempDir(), 1)
	body, ct := multipartBody(t, map[string]string{"video": video}, map[string]string{"manual": "mona_lisa.png", "meta": `{"template":"reveal","lang":"es","phrases":[{"role":"closing","text":"¿Cuál sigue?"}]}`})
	res, err := http.Post(ts.URL+"/api/reels/mux", ct, body)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("save: %v %v", err, res.Status)
	}
	var out struct{ Name string }
	json.NewDecoder(res.Body).Decode(&out)
	if out.Name != "mona_lisa · Revelado · es.mp4" {
		t.Errorf("name %q", out.Name)
	}
	side := readSidecar(filepath.Join(g.out, out.Name))
	if side["stage"] != stageRefined || side["title"] != "Mona lisa" || side["source"] != "mona_lisa.png" || side["manual"] != true {
		t.Errorf("sidecar %v", side)
	}
	if len(g.state.Waiting) != 0 {
		t.Error("still waiting")
	}
	if _, err := os.Stat(filepath.Join(g.in, processedDir, "mona_lisa.png")); err != nil {
		t.Errorf("source not in procesados: %v", err)
	}
	// Still reachable to refine it later.
	if res, _ := http.Get(ts.URL + "/api/review/" + url.PathEscape(out.Name) + "/source"); res.StatusCode != 200 {
		t.Errorf("source for refining: %d", res.StatusCode)
	}
}
