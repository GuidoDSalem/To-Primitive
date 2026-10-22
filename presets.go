package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Presets: named settings of a template (shapes, animation, duration, the
// original at the end, phrases, brand, song...), saved from the reel editor
// to apply them to any photo or video. What belongs to one source (framing,
// portion of the video, test frames) isn't part of them. They live with the
// library, in .presets.json; a preset's song is kept like a reel's.

type Preset struct {
	ID       string          `json:"id"`
	Template string          `json:"template"`
	Name     string          `json:"name"`
	Created  time.Time       `json:"created"`
	Settings json.RawMessage `json:"settings"` // as a reel's record, without the source's parts
}

type presetStore struct {
	path string
	mu   sync.Mutex
	list []Preset
}

func newPresetStore(path string) *presetStore {
	s := &presetStore{path: path}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &s.list); err != nil {
			log.Printf("presets: ignoring unreadable %s: %v", path, err)
		}
	}
	return s
}

func (s *presetStore) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(s.list, "", "  ")
	return os.WriteFile(s.path, b, 0o644)
}

func (s *presetStore) List() []Preset {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]Preset{}, s.list...)
	slices.SortStableFunc(out, func(a, b Preset) int { return a.Created.Compare(b.Created) })
	return out
}

// Add saves a preset; a name already used for that template is replaced.
func (s *presetStore) Add(template, name string, settings json.RawMessage) (Preset, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Preset{}, errors.New("falta el nombre del preset")
	}
	if len([]rune(name)) > 60 {
		return Preset{}, errors.New("el nombre es muy largo (hasta 60 letras)")
	}
	if template == "" || !json.Valid(settings) {
		return Preset{}, errors.New("faltan el template o los ajustes")
	}
	p := Preset{ID: newID(), Template: template, Name: name, Created: time.Now().UTC(), Settings: settings}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.list = slices.DeleteFunc(s.list, func(x Preset) bool {
		return x.Template == template && strings.EqualFold(x.Name, name)
	})
	s.list = append(s.list, p)
	return p, s.saveLocked()
}

func (s *presetStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.list)
	s.list = slices.DeleteFunc(s.list, func(x Preset) bool { return x.ID == id })
	if len(s.list) == n {
		return errNotFound
	}
	return s.saveLocked()
}

// ---------- HTTP ----------

func (srv *server) presetList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, srv.presets.List())
}

// presetAdd takes template, name and settings (JSON), and optionally the
// song (field audio), kept so the preset brings it back.
func (srv *server) presetAdd(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 200<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "bad form: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer r.MultipartForm.RemoveAll()
	settings := json.RawMessage(r.FormValue("settings"))
	if _, ok := r.MultipartForm.File["audio"]; ok {
		dir, err := os.MkdirTemp(srv.dataDir, "preset-")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer os.RemoveAll(dir)
		song := filepath.Join(dir, "audio")
		if _, err := saveFormFile(r, "audio", song); err != nil {
			http.Error(w, "bad audio: "+err.Error(), http.StatusBadRequest)
			return
		}
		settings = srv.keepAudio(r, song, settings)
	}
	p, err := srv.presets.Add(r.FormValue("template"), r.FormValue("name"), settings)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, p)
}

func (srv *server) presetDelete(w http.ResponseWriter, r *http.Request) {
	if err := srv.presets.Delete(r.PathValue("id")); err != nil {
		pubError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
