package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Editing a queued reel: it opens in the reel editor with all its settings
// (reels.html?edit=<name>), together with the same reel in its other queued
// languages, and is exported again over itself, keeping its place in the
// queue, its title, description and tags.

// Replace stores the reel at reelPath over the queued video name, with its
// new settings.
func (p *publisher) Replace(name, reelPath string, meta json.RawMessage) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	path, err := p.queuedPath(name)
	if err != nil {
		return err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	in, err := os.Open(reelPath)
	if err != nil {
		return err
	}
	tmp := path + ".part.mp4"
	err = saveFile(tmp, in)
	in.Close()
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	// The queue goes oldest first: keep its date so it keeps its place.
	os.Chtimes(path, fi.ModTime(), fi.ModTime())
	side := readSidecar(path)
	if len(meta) > 0 && json.Valid(meta) {
		side["settings"] = meta
	}
	side["edited"] = time.Now().Format(time.RFC3339)
	p.logLocked(name, "editado", "desde el editor de reels")
	if err := writeSidecar(path, side); err != nil {
		return err
	}
	return p.saveLocked()
}

// queuedSource is where a queued reel's source is: the generator's input
// folder, or the library it was sent from.
func (srv *server) queuedSource(side map[string]any) (string, error) {
	if id, _ := side["reel"].(string); id != "" {
		return srv.lib.sourcePath(id)
	}
	if srv.gen == nil {
		return "", errors.New("generator not configured")
	}
	src, _ := side["source"].(string)
	return srv.gen.sourcePath(src)
}

// queuedEdit is a queued video's JSON and source, or an error if it
// can't reopen in the editor.
func (srv *server) queuedEdit(w http.ResponseWriter, r *http.Request, p *publisher) (map[string]any, string, bool) {
	p.mu.Lock()
	path, err := p.queuedPath(r.PathValue("name"))
	p.mu.Unlock()
	if err != nil {
		pubError(w, err)
		return nil, "", false
	}
	side := readSidecar(path)
	if side["settings"] == nil {
		http.Error(w, "este video no tiene sus ajustes (no salió del editor ni del generador)", http.StatusConflict)
		return nil, "", false
	}
	src, err := srv.queuedSource(side)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return nil, "", false
	}
	return side, src, true
}

func (srv *server) pubSpec(w http.ResponseWriter, r *http.Request, p *publisher) {
	side, src, ok := srv.queuedEdit(w, r, p)
	if !ok {
		return
	}
	dir := p.dir
	if side["reel"] != nil { // from the library: no other languages
		dir = ""
	}
	writeReelSpec(w, r.PathValue("name"), side, src, dir, "")
}

func (srv *server) pubSource(w http.ResponseWriter, r *http.Request, p *publisher) {
	if _, src, ok := srv.queuedEdit(w, r, p); ok {
		http.ServeFile(w, r, src)
	}
}

// ---------- library ----------

// A library reel reopens in the editor (reels.html?lib=<id>) from the source
// kept with it, and is saved again over itself.

const sourcesDir = "sources"

func (l *library) entry(id string) (ReelEntry, string, error) {
	_, mpath, err := l.paths(id)
	if err != nil {
		return ReelEntry{}, "", err
	}
	b, err := os.ReadFile(mpath)
	if err != nil {
		return ReelEntry{}, "", errNotFound
	}
	var e ReelEntry
	if err := json.Unmarshal(b, &e); err != nil {
		return ReelEntry{}, "", err
	}
	return e, mpath, nil
}

// Get is reel id's entry.
func (l *library) Get(id string) (ReelEntry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, _, err := l.entry(id)
	return e, err
}

func writeEntry(mpath string, e ReelEntry) error {
	b, _ := json.MarshalIndent(e, "", "  ")
	return os.WriteFile(mpath, b, 0o644)
}

// KeepSource stores the file at path (named name) as the source of reel id.
func (l *library) KeepSource(id, path, name string) error {
	if !validSourceName(filepath.Base(name)) {
		return errors.New("fuente inválida")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = io.Copy(h, f)
	f.Close()
	if err != nil {
		return err
	}
	rel := filepath.Join(sourcesDir, hex.EncodeToString(h.Sum(nil))[:16], filepath.Base(name))
	dest := filepath.Join(l.dir, rel)
	if _, err := os.Stat(dest); err != nil {
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		err = saveFile(dest+".part", in)
		in.Close()
		if err == nil {
			err = os.Rename(dest+".part", dest)
		}
		if err != nil {
			os.Remove(dest + ".part")
			return err
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	e, mpath, err := l.entry(id)
	if err != nil {
		return err
	}
	e.Source = rel
	return writeEntry(mpath, e)
}

// sourcePath is the kept source of reel id.
func (l *library) sourcePath(id string) (string, error) {
	e, err := l.Get(id)
	if err != nil {
		return "", err
	}
	if e.Source == "" {
		return "", errors.New("este reel no guardó su foto o video de origen (es de antes de poder editarlos)")
	}
	path := filepath.Join(l.dir, e.Source)
	if _, err := os.Stat(path); err != nil {
		return "", errors.New("falta su foto o video de origen")
	}
	return path, nil
}

// Replace stores the reel at reelPath over reel id, with its new settings.
func (l *library) Replace(ctx context.Context, id, reelPath string, meta json.RawMessage) (ReelEntry, error) {
	vpath, _, err := l.paths(id)
	if err != nil {
		return ReelEntry{}, err
	}
	info, err := probeMedia(ctx, reelPath)
	if err != nil {
		return ReelEntry{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	e, mpath, err := l.entry(id)
	if err != nil {
		return ReelEntry{}, err
	}
	in, err := os.Open(reelPath)
	if err != nil {
		return ReelEntry{}, err
	}
	err = saveFile(vpath+".part", in)
	in.Close()
	if err == nil {
		err = os.Rename(vpath+".part", vpath)
	}
	if err != nil {
		os.Remove(vpath + ".part")
		return ReelEntry{}, err
	}
	if st, err := os.Stat(vpath); err == nil {
		e.Size = st.Size()
	}
	e.Duration, e.Width, e.Height = info.Duration, info.Width, info.Height
	if len(meta) > 0 && json.Valid(meta) {
		e.Meta = meta
	}
	now := time.Now().UTC()
	e.Edited = &now
	return e, writeEntry(mpath, e)
}

// dropUnusedSourceLocked removes a kept source no reel refers to any more.
func (l *library) dropUnusedSourceLocked(rel string) {
	if rel == "" {
		return
	}
	files, _ := filepath.Glob(filepath.Join(l.dir, "*.json"))
	for _, f := range files {
		var e ReelEntry
		if b, err := os.ReadFile(f); err == nil && json.Unmarshal(b, &e) == nil && e.Source == rel {
			return
		}
	}
	os.Remove(filepath.Join(l.dir, rel))
	os.Remove(filepath.Dir(filepath.Join(l.dir, rel)))
}

// keepMuxSource keeps the source the editor sent with a reel for the
// library: a photo (field src) or an uploaded video (srcVideo, its id).
func (srv *server) keepMuxSource(r *http.Request, id, dir string) error {
	if vid := r.FormValue("srcVideo"); vid != "" {
		srv.mu.Lock()
		v := srv.videos[vid]
		srv.mu.Unlock()
		if v == nil {
			return errors.New("ya no está el video de origen en el servidor")
		}
		return srv.lib.KeepSource(id, v.sourcePath(), r.FormValue("srcName"))
	}
	f, hdr, err := r.FormFile("src")
	if errors.Is(err, http.ErrMissingFile) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	path := filepath.Join(dir, "src")
	if err := saveFile(path, f); err != nil {
		return err
	}
	return srv.lib.KeepSource(id, path, hdr.Filename)
}

func (srv *server) reelSpec(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	e, err := srv.lib.Get(id)
	if err != nil {
		pubError(w, err)
		return
	}
	if _, err := srv.lib.sourcePath(id); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{
		"name": id, "settings": e.Meta, "siblings": []any{},
		"source": sourceInfo(filepath.Base(e.Source)),
	})
}

func (srv *server) reelSource(w http.ResponseWriter, r *http.Request) {
	path, err := srv.lib.sourcePath(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	http.ServeFile(w, r, path)
}

func sourceInfo(name string) map[string]string {
	return map[string]string{"name": name, "type": mime.TypeByExtension(strings.ToLower(filepath.Ext(name))), "kind": sourceKind(name)}
}
