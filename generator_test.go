package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func genFixture(t *testing.T) (*generator, string, *time.Time) {
	t.Helper()
	dir := t.TempDir()
	g, err := newGenerator(filepath.Join(dir, "in"), filepath.Join(dir, "out"), "", "http://127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	g.state.Settings.Languages = []string{"es"} // one version per template, unless a test says otherwise
	g.state.Settings.SkipChoice = true          // every template at once, unless a test says otherwise
	now := time.Now()
	g.now = func() time.Time { return now }
	return g, dir, &now
}

func jobsOf(g *generator) map[string][]string {
	out := map[string][]string{}
	for _, j := range g.state.Jobs {
		out[j.Source] = append(out[j.Source], j.Template+":"+j.State)
	}
	return out
}

func TestGeneratorQueue(t *testing.T) {
	g, _, now := genFixture(t)
	old := now.Add(-time.Minute)
	write := func(name string) {
		p := filepath.Join(g.in, name)
		os.WriteFile(p, []byte("x"), 0o644)
		os.Chtimes(p, old, old)
	}
	write("foto.jpg")
	write("clip.mp4")
	write("notas.txt")
	write(".oculto.png")

	// The first scan only notes them (a copy may be in progress); the second queues.
	g.scan()
	if len(g.state.Jobs) != 0 {
		t.Fatal("queued on the first sight")
	}
	g.scan()
	js := jobsOf(g)
	if len(js["foto.jpg"]) != len(autoTemplates["image"]) || len(js["clip.mp4"]) != len(autoTemplates["video"]) || len(js) != 2 {
		t.Fatalf("jobs: %v", js)
	}
	// Scanning again doesn't queue them twice.
	g.scan()
	if n := len(g.state.Jobs); n != len(autoTemplates["image"])+len(autoTemplates["video"]) {
		t.Errorf("%d jobs after a rescan", n)
	}

	// Only some templates; a file that keeps changing waits.
	g.SetSettings(GenSettings{Enabled: true, SkipChoice: true, Templates: []string{"reveal", "shapes", "nope"}, Languages: []string{"es"}})
	if strings.Join(g.state.Settings.Templates, ",") != "reveal,shapes" {
		t.Errorf("settings %+v", g.state.Settings)
	}
	write("otra.png")
	g.scan()
	os.WriteFile(filepath.Join(g.in, "otra.png"), []byte("xy"), 0o644) // still being copied
	os.Chtimes(filepath.Join(g.in, "otra.png"), old, old)
	g.scan()
	if len(jobsOf(g)["otra.png"]) != 0 {
		t.Error("queued a file that was still changing")
	}
	g.scan()
	if got := jobsOf(g)["otra.png"]; strings.Join(got, ",") != "reveal:queued" {
		t.Errorf("otra.png: %v", got)
	}

	// Without a browser, a job fails with a clear message, and can be retried.
	j := g.next()
	g.run(context.Background(), j)
	if j.State != "error" || !strings.Contains(j.Error, "navegador") {
		t.Fatalf("job without browser: %+v", j)
	}
	if err := g.Retry(j.ID); err != nil || j.State != "queued" {
		t.Errorf("retry: %v %+v", err, j)
	}
	// Paused: nothing runs.
	g.SetSettings(GenSettings{Enabled: false})
	if g.next() != nil {
		t.Error("ran while paused")
	}
}

// The page sends the finished reel through /api/reels/mux with job=<id>: it
// lands in the output folder with its JSON, and when every version of a
// source is done the source moves to procesados/.
func TestGeneratorComplete(t *testing.T) {
	needFFmpeg(t)
	g, dir, now := genFixture(t)
	g.SetSettings(GenSettings{Enabled: true, SkipChoice: true, Templates: []string{"reveal", "levels"}, Languages: []string{"es"}})
	src := filepath.Join(g.in, "mona lisa.png")
	os.WriteFile(src, []byte("not really a png"), 0o644)
	old := now.Add(-time.Minute)
	os.Chtimes(src, old, old)
	g.scan()
	g.scan()
	if len(g.state.Jobs) != 2 {
		t.Fatalf("jobs %v", jobsOf(g))
	}

	srv := newServer(filepath.Join(dir, "data"), filepath.Join(dir, "reels"))
	os.MkdirAll(srv.dataDir, 0o755)
	srv.gen = g
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()

	video := silentVideo(t, t.TempDir(), 1)
	meta := `{"phrases":[{"role":"question","text":"¿En qué nivel la adivinaste?"},{"role":"closing","text":"Comentá 👇"}],"shapes":{"shapes":150,"mode":1}}`
	for i := 0; i < 2; i++ {
		j := g.next()
		if j == nil {
			t.Fatal("no job")
		}
		// What the page asks for.
		res, _ := http.Get(ts.URL + "/api/auto/jobs/" + j.ID)
		var spec struct {
			Template string
			Source   struct{ Name, Type string }
		}
		json.NewDecoder(res.Body).Decode(&spec)
		if spec.Template != j.Template || spec.Source.Name != "mona lisa.png" || spec.Source.Type != "image/png" {
			t.Errorf("job spec %+v", spec)
		}
		body, ct := multipartBody(t, map[string]string{"video": video}, map[string]string{"job": j.ID, "meta": meta})
		res, err := http.Post(ts.URL+"/api/reels/mux", ct, body)
		if err != nil || res.StatusCode != 200 {
			t.Fatalf("mux: %v %v", err, res.Status)
		}
		g.mu.Lock()
		st := j.State
		g.mu.Unlock()
		if st != "done" {
			t.Fatalf("job %s: %s", j.Template, st)
		}
	}
	out, _ := filepath.Glob(filepath.Join(g.out, "*.mp4"))
	if len(out) != 2 {
		t.Fatalf("outputs %v", out)
	}
	side := readSidecar(filepath.Join(g.out, "mona lisa · Revelado · es.mp4"))
	if side["title"] != "¿En qué nivel la adivinaste?" || !strings.Contains(side["description"].(string), "#Shorts") || side["template"] != "reveal" {
		t.Errorf("sidecar %v", side)
	}
	if _, err := os.Stat(filepath.Join(g.in, "procesados", "mona lisa.png")); err != nil {
		t.Error("source not moved to procesados/")
	}
	// A job not running can't be completed twice.
	body, ct := multipartBody(t, map[string]string{"video": video}, map[string]string{"job": g.state.Jobs[0].ID})
	if res, _ := http.Post(ts.URL+"/api/reels/mux", ct, body); res.StatusCode != http.StatusConflict {
		t.Errorf("completing a finished job: %d", res.StatusCode)
	}
	// The overview lists the outputs; one goes to the publishing queue.
	var o GenOverview
	res, _ := http.Get(ts.URL + "/api/auto")
	json.NewDecoder(res.Body).Decode(&o)
	if len(o.Outputs) != 2 || len(o.Jobs) != 2 {
		t.Fatalf("overview: %d outputs, %d jobs", len(o.Outputs), len(o.Jobs))
	}
	pub, _ := newPublisher(filepath.Join(dir, "pub"), simulatedUploader{})
	srv.pub = pub
	req, _ := http.NewRequest("POST", ts.URL+"/api/auto/outputs/"+strings.ReplaceAll("mona lisa · Revelado · es.mp4", " ", "%20")+"/queue", nil)
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != 200 {
		t.Fatalf("to the queue: %v %v", err, res.Status)
	}
	if q := pub.Overview().Queue; len(q) != 1 || q[0].Title != "¿En qué nivel la adivinaste?" {
		t.Errorf("queue %+v", q)
	}
	if _, err := os.Stat(filepath.Join(g.out, "mona lisa · Revelado · es.mp4")); err == nil {
		t.Error("still in the output folder after moving it to the queue")
	}
}
