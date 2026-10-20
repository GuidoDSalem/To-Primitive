package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Uploading sources from the Generar page: they are saved in the input
// folder (as if dropped there) and their versions are queued right away.

// Upload saves a photo or video named name in the input folder and queues
// its versions. It returns the name it got (another if that one is taken,
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
	g.enqueueLocked(name, kind, "fuente subida")
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
