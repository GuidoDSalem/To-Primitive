package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPresets(t *testing.T) {
	needFFmpeg(t)
	dir := t.TempDir()
	srv := newServer(filepath.Join(dir, "data"), filepath.Join(dir, "reels"))
	os.MkdirAll(srv.dataDir, 0o755)
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()
	song := sine(t, t.TempDir(), "tema.wav", 1)
	add := func(name string, withSong bool) (int, Preset) {
		files := map[string]string{}
		if withSong {
			files["audio"] = song
		}
		body, ct := multipartBody(t, files, map[string]string{"template": "reveal", "name": name,
			"settings": `{"template":"reveal","shapes":{"shapes":80,"mode":4},"ending":3,"audio":{"mode":"file","name":"tema.wav"}}`})
		res, err := http.Post(ts.URL+"/api/presets", ct, body)
		if err != nil {
			t.Fatal(err)
		}
		var p Preset
		json.NewDecoder(res.Body).Decode(&p)
		return res.StatusCode, p
	}
	code, p := add("Lento y con círculos", true)
	if code != 200 || p.ID == "" {
		t.Fatalf("add: %d", code)
	}
	var s struct {
		Audio  struct{ Kept string }
		Ending float64
	}
	json.Unmarshal(p.Settings, &s)
	if !strings.HasSuffix(s.Audio.Kept, "/tema.wav") || s.Ending != 3 {
		t.Errorf("settings %s", p.Settings)
	}
	if code, _ := add(" ", false); code != 400 {
		t.Errorf("no name: %d", code)
	}
	// The same name for the template replaces it.
	_, p2 := add("lento y con círculos", false)
	list := srv.presets.List()
	if len(list) != 1 || list[0].ID != p2.ID {
		t.Fatalf("list %+v", list)
	}
	// Kept across restarts.
	if again := newPresetStore(srv.presets.path).List(); len(again) != 1 || again[0].Name != "lento y con círculos" {
		t.Errorf("reloaded %+v", again)
	}
	req, _ := http.NewRequest("DELETE", ts.URL+"/api/presets/"+p2.ID, nil)
	if res, _ := http.DefaultClient.Do(req); res.StatusCode != 204 || len(srv.presets.List()) != 0 {
		t.Errorf("delete: %d", res.StatusCode)
	}
}
