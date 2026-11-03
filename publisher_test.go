package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScheduleSlots(t *testing.T) {
	s := PublishSettings{Times: []string{"19:00", "09:30"}, Days: []int{1, 3}, TZ: "America/Argentina/Buenos_Aires"}
	if err := s.validate(); err != nil {
		t.Fatal(err)
	}
	loc := s.loc()
	// Monday 2026-10-19, 10:00 in Buenos Aires.
	now := time.Date(2026, 10, 19, 10, 0, 0, 0, loc)
	got := s.slotsAfter(now, 4)
	want := []string{"Mon 19:00", "Wed 09:30", "Wed 19:00", "Mon 09:30"}
	for i, w := range want {
		if g := got[i].In(loc).Format("Mon 15:04"); g != w {
			t.Errorf("slot %d = %s, want %s", i, g, w)
		}
	}
	if g := s.lastSlot(now).In(loc).Format("Mon 15:04"); g != "Mon 09:30" {
		t.Errorf("last slot %s", g)
	}
	// The time zone matters: 19:00 in Buenos Aires is 22:00 UTC.
	if got[0].UTC().Hour() != 22 {
		t.Errorf("19:00 Buenos Aires = %v UTC", got[0].UTC())
	}
	for _, bad := range []PublishSettings{
		{Times: []string{"25:00"}, Days: []int{1}, TZ: "UTC"},
		{Times: []string{"19:00"}, Days: nil, TZ: "UTC"},
		{Times: []string{"19:00"}, Days: []int{1}, TZ: "Marte/Olimpo"},
	} {
		if bad.validate() == nil {
			t.Errorf("%+v should be invalid", bad)
		}
	}
}

// pubFixture makes a publishing folder with three short videos, oldest first.
func pubFixture(t *testing.T) (string, *publisher, *time.Time) {
	t.Helper()
	needFFmpeg(t)
	dir := t.TempDir()
	src := silentVideo(t, t.TempDir(), 1)
	b, _ := os.ReadFile(src)
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for i, n := range []string{"b viejo.mp4", "a medio.mp4", "c nuevo.mp4"} {
		path := filepath.Join(dir, n)
		os.WriteFile(path, b, 0o644)
		os.Chtimes(path, base.Add(time.Duration(i)*time.Hour), base.Add(time.Duration(i)*time.Hour))
	}
	os.WriteFile(filepath.Join(dir, "notas.txt"), []byte("no es un video"), 0o644)
	p, err := newPublisher(dir, simulatedUploader{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return now }
	return dir, p, &now
}

func names(items []PubItem) string {
	var out []string
	for _, it := range items {
		out = append(out, it.Name)
	}
	return strings.Join(out, ",")
}

func TestPublisherQueue(t *testing.T) {
	dir, p, now := pubFixture(t)
	if q := names(p.Overview().Queue); q != "b viejo.mp4,a medio.mp4,c nuevo.mp4" {
		t.Fatalf("queue by date: %s", q)
	}
	// Preferred order, skipping, metadata.
	p.SetOrder([]string{"c nuevo.mp4"})
	p.SetSkipped("b viejo.mp4", true)
	p.SetMeta("a medio.mp4", "Mona Lisa en formas", "¿Cuál sigue? #Shorts", []string{"arte"})
	if err := p.SetSettings(PublishSettings{Enabled: true, Times: []string{"19:00"}, Days: []int{0, 1, 2, 3, 4, 5, 6}, TZ: "UTC"}); err != nil {
		t.Fatal(err)
	}
	o := p.Overview()
	if names(o.Queue) != "c nuevo.mp4,b viejo.mp4,a medio.mp4" {
		t.Fatalf("queue: %s", names(o.Queue))
	}
	sched := map[string]string{}
	for _, it := range o.Queue {
		if it.ScheduledAt != nil {
			sched[it.Name] = it.ScheduledAt.Format("01-02 15:04")
		}
	}
	if sched["c nuevo.mp4"] != "10-19 19:00" || sched["a medio.mp4"] != "10-20 19:00" || sched["b viejo.mp4"] != "" {
		t.Errorf("times: %v", sched)
	}
	if o.Queue[2].Title != "Mona Lisa en formas" || o.Queue[0].Title != "c nuevo" || o.Queue[0].Duration < 0.9 {
		t.Errorf("items: %+v", o.Queue)
	}

	// The schedule: nothing before 19:00, one video at 19:00, not again.
	ctx := context.Background()
	p.tick(ctx)
	if len(p.Overview().Published) != 0 {
		t.Fatal("published before its time")
	}
	*now = time.Date(2026, 10, 19, 19, 0, 30, 0, time.UTC)
	p.tick(ctx)
	p.tick(ctx)
	o = p.Overview()
	if names(o.Published) != "c nuevo.mp4" || names(o.Queue) != "b viejo.mp4,a medio.mp4" {
		t.Fatalf("after 19:00: published %s, queue %s", names(o.Published), names(o.Queue))
	}
	if o.Published[0].PublishedAt == nil || !strings.Contains(o.Published[0].Result, "simulado") {
		t.Errorf("published item %+v", o.Published[0])
	}
	if _, err := os.Stat(filepath.Join(dir, "publicados", "c nuevo.json")); err != nil {
		t.Error("no record next to the published video")
	}
	// Server off for three days: only one video goes out when it's back.
	*now = now.AddDate(0, 0, 3)
	p.tick(ctx)
	p.tick(ctx)
	if q := names(p.Overview().Queue); q != "b viejo.mp4" {
		t.Errorf("after a long gap: queue %s", q)
	}
	// The JSON's title travels with it.
	if pub := p.Overview().Published; pub[0].Name != "a medio.mp4" || pub[0].Title != "Mona Lisa en formas" {
		t.Errorf("published %+v", pub[0])
	}
	// Only skipped left: the next time finds nothing to publish.
	*now = now.AddDate(0, 0, 1)
	p.tick(ctx)
	if len(p.Overview().Queue) != 1 || p.Overview().Log[0].Action != "sin videos" {
		t.Errorf("empty queue: %+v", p.Overview().Log[0])
	}

	// Back to the queue, first in line.
	if err := p.Requeue("c nuevo.mp4"); err != nil {
		t.Fatal(err)
	}
	if q := names(p.Overview().Queue); q != "c nuevo.mp4,b viejo.mp4" {
		t.Errorf("requeued: %s", q)
	}
	// The state lives in the folder: a new publisher picks it up.
	p2, _ := newPublisher(dir, simulatedUploader{})
	if !p2.state.Settings.Enabled || len(p2.state.Skipped) != 1 {
		t.Errorf("state not kept: %+v", p2.state)
	}
}

func TestPublishEndpoints(t *testing.T) {
	dir, p, _ := pubFixture(t)
	srv := newServer(filepath.Join(t.TempDir(), "data"), filepath.Join(t.TempDir(), "reels"))
	os.MkdirAll(srv.lib.dir, 0o755)
	srv.pub = p
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()
	do := func(method, path, body string) *http.Response {
		req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	var o Overview
	json.NewDecoder(do("GET", "/api/publish", "").Body).Decode(&o)
	if len(o.Queue) != 3 || o.Settings.TZ != "America/Argentina/Buenos_Aires" {
		t.Fatalf("overview %+v", o)
	}
	esc := url.PathEscape("a medio.mp4")
	if r := do("POST", "/api/publish/queue/"+esc+"/now", ""); r.StatusCode != 200 {
		t.Fatalf("publish now: %d", r.StatusCode)
	}
	if r := do("GET", "/api/publish/published/"+esc+"/video", ""); r.StatusCode != 200 {
		t.Errorf("published video: %d", r.StatusCode)
	}
	if r := do("POST", "/api/publish/queue/"+url.PathEscape("../main.go")+"/now", ""); r.StatusCode != 400 && r.StatusCode != 404 {
		t.Errorf("path outside the folder: %d", r.StatusCode)
	}
	if r := do("POST", "/api/publish/queue/nope.mp4/now", ""); r.StatusCode != 404 {
		t.Errorf("missing video: %d", r.StatusCode)
	}
	if r := do("PUT", "/api/publish/settings", `{"enabled":true,"times":["7pm"],"days":[1],"tz":"UTC"}`); r.StatusCode != 400 {
		t.Errorf("bad time accepted: %d", r.StatusCode)
	}
	if r := do("PUT", "/api/publish/order", `{"names":["c nuevo.mp4","b viejo.mp4"]}`); r.StatusCode != 200 {
		t.Errorf("order: %d", r.StatusCode)
	}

	// From the reel library to the queue, with title and description from the phrases.
	video := silentVideo(t, t.TempDir(), 1)
	f, _ := os.Open(video)
	e, err := srv.lib.Save(context.Background(), f, json.RawMessage(`{"phrases":[{"role":"title","text":"La Mona Lisa con {formas} {tipo}"},{"role":"closing","text":"¿Cuál hago mañana?"}],"shapes":{"shapes":150,"mode":1}}`))
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	r := do("POST", "/api/reels/"+e.ID+"/queue", "")
	var added struct{ Name string }
	json.NewDecoder(r.Body).Decode(&added)
	if r.StatusCode != 200 || !strings.HasPrefix(added.Name, "reel ") {
		t.Fatalf("queue from library: %d %+v", r.StatusCode, added)
	}
	json.NewDecoder(do("GET", "/api/publish", "").Body).Decode(&o)
	var it *PubItem
	for i := range o.Queue {
		if o.Queue[i].Name == added.Name {
			it = &o.Queue[i]
		}
	}
	if it == nil || it.Title != "La Mona Lisa con 150 triángulos" || !strings.Contains(it.Description, "¿Cuál hago mañana?") || !strings.Contains(it.Description, "#Shorts") {
		t.Errorf("queued reel %+v", it)
	}
	if _, err := os.Stat(filepath.Join(dir, strings.TrimSuffix(added.Name, ".mp4")+".json")); err != nil {
		t.Error("no JSON next to the queued reel")
	}
}
