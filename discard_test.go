package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A queued video discarded to descartados/ (with its JSON) and restored.
func TestDiscard(t *testing.T) {
	dir, p, _ := pubFixture(t)
	writeSidecar(filepath.Join(dir, "a medio.mp4"), map[string]any{"title": "Medio"})
	p.SetOrder([]string{"a medio.mp4", "c nuevo.mp4"})
	srv := newServer(filepath.Join(dir, "data"), filepath.Join(dir, "reels"))
	srv.pub = p
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()
	post := func(path string) int {
		res, err := http.Post(ts.URL+path, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode
	}

	if code := post("/api/publish/queue/" + url.PathEscape("a medio.mp4") + "/discard"); code != 200 {
		t.Fatalf("discard: %d", code)
	}
	o := p.Overview()
	if names(o.Queue) != "c nuevo.mp4,b viejo.mp4" || names(o.Discarded) != "a medio.mp4" || o.Discarded[0].Title != "Medio" {
		t.Fatalf("queue %s, discarded %s", names(o.Queue), names(o.Discarded))
	}
	if _, err := os.Stat(filepath.Join(dir, discardedDir, "a medio.json")); err != nil {
		t.Errorf("its JSON stayed behind: %v", err)
	}
	if res, _ := http.Get(ts.URL + "/api/publish/discarded/" + url.PathEscape("a medio.mp4") + "/video"); res.StatusCode != 200 {
		t.Errorf("discarded video: %d", res.StatusCode)
	}
	if code := post("/api/publish/queue/" + url.PathEscape("a medio.mp4") + "/discard"); code != 404 {
		t.Errorf("discarding twice: %d", code)
	}

	if code := post("/api/publish/discarded/" + url.PathEscape("a medio.mp4") + "/restore"); code != 200 {
		t.Fatalf("restore: %d", code)
	}
	o = p.Overview()
	if names(o.Queue) != "c nuevo.mp4,b viejo.mp4,a medio.mp4" || len(o.Discarded) != 0 || o.Queue[2].Title != "Medio" {
		t.Errorf("after restore: queue %s, discarded %s", names(o.Queue), names(o.Discarded))
	}
}

// A video without a sound track is flagged.
func TestSilentWarning(t *testing.T) {
	_, p, _ := pubFixture(t) // silent videos
	q := p.Overview().Queue
	if !q[0].Silent || !strings.Contains(strings.Join(q[0].Warnings, " "), "sin audio") {
		t.Errorf("silent video: %+v", q[0])
	}
}
