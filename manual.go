package main

import (
	"encoding/json"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// A new source can also be made by hand: it opens in the reel editor
// (reels.html?src=<name>) and the reel saved there goes to the generated
// ones as a version already refined, ready for the queue.

// SaveManual stores the reel made by hand from the waiting source src. The
// source stops waiting and, unless versions of it are still being made,
// moves to procesados/.
func (g *generator) SaveManual(src, reelPath string, meta json.RawMessage) (string, error) {
	var m struct {
		Template string `json:"template"`
		Lang     string `json:"lang"`
	}
	json.Unmarshal(meta, &m)
	if m.Lang == "" {
		m.Lang = "es"
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, err := g.sourcePath(src); err != nil {
		return "", err
	}
	name := g.outputName(&GenJob{Source: src, Template: m.Template, Lang: m.Lang})
	dest := filepath.Join(g.out, name)
	in, err := os.Open(reelPath)
	if err != nil {
		return "", err
	}
	err = saveFile(dest, in)
	in.Close()
	if err != nil {
		return "", err
	}
	side := map[string]any{"source": src, "template": m.Template, "lang": m.Lang, "generated": g.now().Format(time.RFC3339),
		"stage": stageRefined, "refined": g.now().Format(time.RFC3339), "manual": true, "settings": meta}
	title, desc := reelTexts(meta)
	if title == "" {
		title = titleFromName(src)
	}
	side["title"], side["description"] = title, strings.TrimSpace(desc+"\n\n#Shorts")
	if err := writeSidecar(dest, side); err != nil {
		return "", err
	}
	g.state.Waiting = slices.DeleteFunc(g.state.Waiting, func(w *WaitingSource) bool { return w.Name == src })
	busy := slices.ContainsFunc(g.state.Jobs, func(j *GenJob) bool {
		return j.Source == src && j.Dest == "" && (j.State == "queued" || j.State == "running")
	})
	if !busy && g.sourceExistsLocked(src) {
		os.MkdirAll(filepath.Join(g.in, processedDir), 0o755)
		os.Rename(filepath.Join(g.in, src), filepath.Join(g.in, processedDir, src))
	}
	g.logLocked(name, "hecho a mano", "desde el editor de reels")
	g.saveLocked()
	return name, nil
}

// genInputSpec says what the editor needs to open a source by hand.
func (srv *server) genInputSpec(w http.ResponseWriter, r *http.Request, g *generator) {
	name := r.PathValue("name")
	if _, err := g.sourcePath(name); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	g.mu.Lock()
	langs := g.state.Settings.langs()
	g.mu.Unlock()
	writeJSON(w, map[string]any{
		"name": name, "kind": sourceKind(name), "type": mime.TypeByExtension(strings.ToLower(filepath.Ext(name))),
		"title": titleFromName(name), "lang": langs[0],
	})
}
