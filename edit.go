package main

import (
	"encoding/json"
	"net/http"
	"os"
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

// queuedSide is the JSON of a queued video, or an error if it isn't there.
func (srv *server) queuedSide(w http.ResponseWriter, r *http.Request, p *publisher) (string, map[string]any, bool) {
	if srv.gen == nil {
		http.Error(w, "generator not configured", http.StatusServiceUnavailable)
		return "", nil, false
	}
	p.mu.Lock()
	path, err := p.queuedPath(r.PathValue("name"))
	p.mu.Unlock()
	if err != nil {
		pubError(w, err)
		return "", nil, false
	}
	side := readSidecar(path)
	if side["settings"] == nil {
		http.Error(w, "este video no tiene sus ajustes (no salió del editor ni del generador)", http.StatusConflict)
		return "", nil, false
	}
	return path, side, true
}

func (srv *server) pubSpec(w http.ResponseWriter, r *http.Request, p *publisher) {
	if _, side, ok := srv.queuedSide(w, r, p); ok {
		writeReelSpec(w, srv.gen, r.PathValue("name"), side, p.dir, "")
	}
}

func (srv *server) pubSource(w http.ResponseWriter, r *http.Request, p *publisher) {
	_, side, ok := srv.queuedSide(w, r, p)
	if !ok {
		return
	}
	src, err := srv.gen.sourcePath(reviewItem(PubItem{}, side).Source)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	http.ServeFile(w, r, src)
}
