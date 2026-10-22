package main

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
)

// Discarding a queued video: it leaves the queue for <queue>/descartados/
// (with its JSON), and can come back.

// Discard moves a queued video to descartados/.
func (p *publisher) Discard(name string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	path, err := p.queuedPath(name)
	if err != nil {
		return err
	}
	if _, busy := p.busy[name]; busy {
		return errors.New("se está publicando ahora")
	}
	dir := filepath.Join(p.dir, discardedDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	dest := filepath.Join(dir, name)
	if _, err := os.Stat(dest); err == nil { // an older one with that name: this one wins
		os.Remove(dest)
		os.Remove(sidecarOf(dest))
	}
	if err := os.Rename(path, dest); err != nil {
		return err
	}
	if _, err := os.Stat(sidecarOf(path)); err == nil {
		os.Rename(sidecarOf(path), sidecarOf(dest))
	}
	p.forgetLocked(name)
	p.logLocked(name, "descartado", "en "+discardedDir+"/")
	return p.saveLocked()
}

// Undiscard puts a discarded video back in the queue (where its date puts it).
func (p *publisher) Undiscard(name string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	path, err := p.discardedPath(name)
	if err != nil {
		return err
	}
	dest := filepath.Join(p.dir, name)
	if _, err := os.Stat(dest); err == nil {
		return errors.New("ya hay un video con ese nombre en la cola")
	}
	if err := os.Rename(path, dest); err != nil {
		return err
	}
	if _, err := os.Stat(sidecarOf(path)); err == nil {
		os.Rename(sidecarOf(path), sidecarOf(dest))
	}
	p.logLocked(name, "devuelto a la cola", "desde "+discardedDir+"/")
	return p.saveLocked()
}

func (p *publisher) discardedPath(name string) (string, error) {
	if !validName(name) {
		return "", errors.New("nombre inválido")
	}
	path := filepath.Join(p.dir, discardedDir, name)
	if _, err := os.Stat(path); err != nil {
		return "", errNotFound
	}
	return path, nil
}

// discardedLocked lists the discarded videos, the last discarded first.
func (p *publisher) discardedLocked() []PubItem {
	items := p.videosIn(filepath.Join(p.dir, discardedDir))
	sort.Slice(items, func(i, j int) bool { return items[i].Modified.After(items[j].Modified) })
	if items == nil {
		items = []PubItem{}
	}
	return items
}

func (srv *server) pubAction(act func(*publisher, string) error) func(http.ResponseWriter, *http.Request, *publisher) {
	return func(w http.ResponseWriter, r *http.Request, p *publisher) {
		if err := act(p, r.PathValue("name")); err != nil {
			pubError(w, err)
			return
		}
		srv.pubOverview(w, r, p)
	}
}
