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

// A source made with presets: one version per preset chosen, named after
// it, and each job brings its settings to the editor.
func TestGenerateWithPresets(t *testing.T) {
	g, dir, _ := genFixture(t)
	g.state.Settings.Enabled, g.state.Settings.SkipChoice = true, false
	srv := newServer(filepath.Join(dir, "data"), filepath.Join(dir, "reels"))
	srv.gen = g
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()
	p, _ := srv.presets.Add("reveal", "Círculos lentos", json.RawMessage(`{"shapes":{"shapes":70,"mode":4},"ending":7}`))
	srv.presets.Add("levels", "Otro", json.RawMessage(`{}`))
	if _, err := g.Upload(strings.NewReader("png"), "mona.png"); err != nil {
		t.Fatal(err)
	}
	start := func(body string) int {
		res, err := http.Post(ts.URL+"/api/auto/inputs/mona.png/start", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode
	}
	if code := start(`{"templates":["reveal"],"languages":["es"],"presets":{"reveal":["nope"]}}`); code != 400 {
		t.Errorf("unknown preset: %d", code)
	}
	if code := start(`{"templates":["reveal","levels"],"languages":["es","en"],"presets":{"reveal":["","` + p.ID + `"]}}`); code != 200 {
		t.Fatalf("start: %d", code)
	}
	var got []string
	var withPreset *GenJob
	for _, j := range g.state.Jobs {
		got = append(got, j.Template+"/"+j.PresetName+"/"+j.Lang)
		if j.Preset == p.ID && withPreset == nil {
			withPreset = j
		}
	}
	if strings.Join(got, ",") != "reveal//es,reveal//en,reveal/Círculos lentos/es,reveal/Círculos lentos/en,levels//es,levels//en" {
		t.Fatalf("jobs %v", got)
	}
	if name := g.outputName(withPreset); name != "mona · Revelado · Círculos lentos · es.mp4" {
		t.Errorf("output name %q", name)
	}
	res, _ := http.Get(ts.URL + "/api/auto/jobs/" + withPreset.ID)
	var spec struct{ Preset struct{ Ending float64 } }
	json.NewDecoder(res.Body).Decode(&spec)
	if spec.Preset.Ending != 7 {
		t.Errorf("job spec without the preset: %+v", spec)
	}
}
