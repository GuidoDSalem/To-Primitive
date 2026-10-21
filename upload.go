package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Uploading sources from the Generar page: they are saved in the input
// folder (as if dropped there) and wait for their templates to be chosen
// (Start), unless the settings skip that.

// Upload saves a photo or video named name in the input folder, ready to
// choose its templates. It returns the name it got (another if that one is taken,
// here or in procesados/).
func (g *generator) Upload(r io.Reader, name string) (string, error) {
	name = filepath.Base(strings.TrimSpace(name))
	kind := sourceKind(name)
	if !validSourceName(name) || kind == "" {
		return "", errors.New("solo fotos o videos (JPG, PNG, WebP, MP4, MOV…)")
	}
	// Written aside first (a dot file the scan ignores), without holding
	// the generator while a large video arrives.
	tmp := filepath.Join(g.in, ".subiendo-"+newID()+".part")
	if err := saveFile(tmp, r); err != nil {
		os.Remove(tmp)
		return "", err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 2; g.sourceNameTakenLocked(name); i++ {
		name = fmt.Sprintf("%s (%d)%s", base, i, ext)
	}
	if err := os.Rename(tmp, filepath.Join(g.in, name)); err != nil {
		os.Remove(tmp)
		return "", err
	}
	g.addSourceLocked(name, kind, "fuente subida")
	g.saveLocked()
	g.poke()
	return name, nil
}

func (g *generator) sourceNameTakenLocked(name string) bool {
	for _, p := range []string{filepath.Join(g.in, name), filepath.Join(g.in, processedDir, name)} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	for _, j := range g.state.Jobs {
		if j.Source == name {
			return true
		}
	}
	return false
}

// genUpload receives one or more files (field "file").
func (srv *server) genUpload(w http.ResponseWriter, r *http.Request, g *generator) {
	mr, err := r.MultipartReader()
	if err != nil {
		http.Error(w, "bad form: "+err.Error(), http.StatusBadRequest)
		return
	}
	var names []string
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			http.Error(w, "bad form: "+err.Error(), http.StatusBadRequest)
			return
		}
		if part.FormName() != "file" || part.FileName() == "" {
			continue
		}
		name, err := g.Upload(part, part.FileName())
		if err != nil {
			http.Error(w, part.FileName()+": "+err.Error(), http.StatusBadRequest)
			return
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		http.Error(w, "no llegó ningún archivo", http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"names": names})
}

// Start queues the chosen templates (those that fit the source) in the
// chosen languages for a waiting source.
func (g *generator) Start(name string, templates, languages []string) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	i := slices.IndexFunc(g.state.Waiting, func(w *WaitingSource) bool { return w.Name == name })
	if i < 0 {
		return 0, errNotFound
	}
	w := g.state.Waiting[i]
	var tpls []string
	for _, t := range autoTemplates[w.Kind] { // in the usual order
		if slices.Contains(templates, t) {
			tpls = append(tpls, t)
		}
	}
	if len(tpls) == 0 {
		return 0, errors.New("elegí al menos un template")
	}
	langs := normLangs(languages)
	if len(langs) == 0 {
		langs = g.state.Settings.langs()
	}
	g.state.Waiting = slices.Delete(g.state.Waiting, i, i+1)
	g.enqueueLocked(w.Name, w.Kind, tpls, langs, "templates elegidos")
	g.saveLocked()
	g.poke()
	return len(tpls) * len(langs), nil
}

// Dismiss drops a waiting source: it moves to <input>/descartados/ (the
// file isn't deleted: it may be the only copy).
func (g *generator) Dismiss(name string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	i := slices.IndexFunc(g.state.Waiting, func(w *WaitingSource) bool { return w.Name == name })
	if i < 0 {
		return errNotFound
	}
	if err := os.MkdirAll(filepath.Join(g.in, discardedDir), 0o755); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(g.in, name), filepath.Join(g.in, discardedDir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	g.state.Waiting = slices.Delete(g.state.Waiting, i, i+1)
	g.logLocked(name, "fuente descartada", "en "+discardedDir+"/ de la entrada")
	g.saveLocked()
	return nil
}

func (srv *server) genStart(w http.ResponseWriter, r *http.Request, g *generator) {
	var body struct {
		Templates []string `json:"templates"`
		Languages []string `json:"languages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := g.Start(r.PathValue("name"), body.Templates, body.Languages); err != nil {
		pubError(w, err)
		return
	}
	srv.genOverview(w, r, g)
}

func (srv *server) genDismiss(w http.ResponseWriter, r *http.Request, g *generator) {
	if err := g.Dismiss(r.PathValue("name")); err != nil {
		pubError(w, err)
		return
	}
	srv.genOverview(w, r, g)
}

// genInput serves a source (to see it while choosing, or edit it by hand).
func (srv *server) genInput(w http.ResponseWriter, r *http.Request, g *generator) {
	path, err := g.sourcePath(r.PathValue("name"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	http.ServeFile(w, r, path)
}
