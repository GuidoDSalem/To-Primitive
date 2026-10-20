package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Review: the generated versions go through a human before publishing.
//  1. choose: for each source and language, one version survives; the rest
//     move to <generated>/descartados/ (they can be restored);
//  2. refine: the chosen one opens in the reel editor with all its settings
//     (reels.html?refine=<name>) and is exported again over itself;
//  3. queue: only then (unless the settings skip refining) it can go to
//     the publishing folder.
// The stage lives in each version's JSON ("stage").

const (
	discardedDir   = "descartados"
	stageGenerated = "generated"
	stageChosen    = "chosen"
	stageRefined   = "refined"
)

// ReviewItem is a version as the review page shows it.
type ReviewItem struct {
	PubItem
	Source   string `json:"source"`
	Template string `json:"template"`
	Lang     string `json:"lang"`
	Stage    string `json:"stage"`
}

// ReviewGroup is everything generated from one source.
type ReviewGroup struct {
	Source    string       `json:"source"`
	Pending   int          `json:"pending"` // versions still being generated
	Versions  []ReviewItem `json:"versions"`
	Discarded []ReviewItem `json:"discarded"`
	Latest    time.Time    `json:"-"`
}

type ReviewOverview struct {
	Groups     []ReviewGroup `json:"groups"`
	SkipRefine bool          `json:"skipRefine"`
	Publishing bool          `json:"publishing"`
	Generated  string        `json:"generated"`
}

func reviewItem(it PubItem, side map[string]any) ReviewItem {
	r := ReviewItem{PubItem: it}
	r.Source, _ = side["source"].(string)
	r.Template, _ = side["template"].(string)
	r.Lang, _ = side["lang"].(string)
	r.Stage, _ = side["stage"].(string)
	if r.Stage == "" {
		r.Stage = stageGenerated
	}
	if r.Source == "" { // made by hand or before review existed
		r.Source = it.Name
	}
	return r
}

// Review lists the generated versions grouped by source (newest first).
func (g *generator) Review(describe func(dir string) []PubItem) ReviewOverview {
	g.mu.Lock()
	pending := map[string]int{}
	for _, j := range g.state.Jobs {
		if j.State == "queued" || j.State == "running" {
			pending[j.Source]++
		}
	}
	skip := g.state.Settings.SkipRefine
	langRank := map[string]int{}
	for i, l := range g.state.Settings.langs() {
		langRank[l] = i + 1
	}
	g.mu.Unlock()
	rank := func(l string) int { // configured languages first, in their order
		if r, ok := langRank[l]; ok {
			return r
		}
		return 1000
	}

	groups := map[string]*ReviewGroup{}
	get := func(src string) *ReviewGroup {
		if groups[src] == nil {
			groups[src] = &ReviewGroup{Source: src, Versions: []ReviewItem{}, Discarded: []ReviewItem{}}
		}
		return groups[src]
	}
	for _, it := range describe(g.out) {
		r := reviewItem(it, readSidecar(filepath.Join(g.out, it.Name)))
		grp := get(r.Source)
		grp.Versions = append(grp.Versions, r)
		if it.Modified.After(grp.Latest) {
			grp.Latest = it.Modified
		}
	}
	for _, it := range describe(filepath.Join(g.out, discardedDir)) {
		r := reviewItem(it, readSidecar(filepath.Join(g.out, discardedDir, it.Name)))
		grp := get(r.Source)
		grp.Discarded = append(grp.Discarded, r)
		if it.Modified.After(grp.Latest) {
			grp.Latest = it.Modified
		}
	}
	for src, n := range pending {
		get(src).Pending = n
	}
	o := ReviewOverview{SkipRefine: skip, Groups: []ReviewGroup{}}
	for _, grp := range groups {
		if len(grp.Versions) == 0 && grp.Pending == 0 && len(grp.Discarded) > 0 && grp.Latest.Before(time.Now().AddDate(0, 0, -30)) {
			continue // long finished and all discarded
		}
		order := func(v []ReviewItem) {
			sort.Slice(v, func(i, j int) bool {
				if v[i].Lang != v[j].Lang {
					if rank(v[i].Lang) != rank(v[j].Lang) {
						return rank(v[i].Lang) < rank(v[j].Lang)
					}
					return v[i].Lang < v[j].Lang
				}
				return v[i].Name < v[j].Name
			})
		}
		order(grp.Versions)
		order(grp.Discarded)
		o.Groups = append(o.Groups, *grp)
	}
	sort.Slice(o.Groups, func(i, j int) bool {
		if (o.Groups[i].Pending > 0) != (o.Groups[j].Pending > 0) {
			return o.Groups[i].Pending > 0
		}
		return o.Groups[i].Latest.After(o.Groups[j].Latest)
	})
	return o
}

func (g *generator) setStage(path, stage string) error {
	side := readSidecar(path)
	side["stage"] = stage
	return writeSidecar(path, side)
}

// moveVersion moves a version (and its JSON) between Generados and descartados/.
func moveVersion(from, toDir string) error {
	if err := os.MkdirAll(toDir, 0o755); err != nil {
		return err
	}
	dest := filepath.Join(toDir, filepath.Base(from))
	if err := os.Rename(from, dest); err != nil {
		return err
	}
	os.Rename(sidecarOf(from), sidecarOf(dest))
	return nil
}

// Choose keeps this version for its source and language and discards the
// others of the same source and language. The languages with nothing
// chosen yet get the same template chosen too: a reel is one piece with a
// version per language (another template can still be chosen for one).
func (g *generator) Choose(name string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	path, err := g.outputPath(name)
	if err != nil {
		return err
	}
	me := reviewItem(PubItem{Name: name}, readSidecar(path))
	type version struct {
		path string
		item ReviewItem
	}
	var others []version
	decided := map[string]bool{} // languages with a version chosen or refined
	entries, _ := os.ReadDir(g.out)
	for _, e := range entries {
		if e.IsDir() || !validName(e.Name()) || e.Name() == name {
			continue
		}
		p := filepath.Join(g.out, e.Name())
		o := reviewItem(PubItem{Name: e.Name()}, readSidecar(p))
		if o.Source != me.Source {
			continue
		}
		others = append(others, version{p, o})
		if o.Stage != stageGenerated && o.Lang != me.Lang {
			decided[o.Lang] = true
		}
	}
	// The same template in the undecided languages.
	chosen := map[string]string{me.Lang: name}
	for _, o := range others {
		if o.item.Lang != me.Lang && !decided[o.item.Lang] && o.item.Template == me.Template && chosen[o.item.Lang] == "" {
			chosen[o.item.Lang] = o.item.Name
		}
	}
	for _, o := range others {
		pick, ok := chosen[o.item.Lang]
		switch {
		case !ok: // a language decided before, or without this template: left as is
		case o.item.Name == pick:
			if o.item.Stage == stageGenerated {
				g.setStage(o.path, stageChosen)
			}
		default:
			if err := moveVersion(o.path, filepath.Join(g.out, discardedDir)); err != nil {
				return err
			}
		}
	}
	langs := make([]string, 0, len(chosen))
	for l := range chosen {
		langs = append(langs, l)
	}
	sort.Strings(langs)
	g.logLocked(name, "elegido", fmt.Sprintf("%s en %s; las demás versiones de esos idiomas pasan a %s/", templateNames[me.Template], strings.Join(langs, ", "), discardedDir))
	if me.Stage == stageGenerated {
		return g.setStage(path, stageChosen)
	}
	return nil
}

func (g *generator) Discard(name string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	path, err := g.outputPath(name)
	if err != nil {
		return err
	}
	return moveVersion(path, filepath.Join(g.out, discardedDir))
}

// Restore brings a discarded version back (as just generated).
func (g *generator) Restore(name string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !validName(name) {
		return errors.New("nombre inválido")
	}
	path := filepath.Join(g.out, discardedDir, name)
	if _, err := os.Stat(path); err != nil {
		return errNotFound
	}
	if _, err := os.Stat(filepath.Join(g.out, name)); err == nil {
		return errors.New("ya hay una versión con ese nombre")
	}
	if err := moveVersion(path, g.out); err != nil {
		return err
	}
	return g.setStage(filepath.Join(g.out, name), stageGenerated)
}

// SetVersionMeta edits title, description and tags of a version.
func (g *generator) SetVersionMeta(name, title, description string, tags []string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	path, err := g.outputPath(name)
	if err != nil {
		return err
	}
	side := readSidecar(path)
	side["title"], side["description"], side["tags"] = strings.TrimSpace(title), strings.TrimSpace(description), tags
	return writeSidecar(path, side)
}

// sourcePath finds a version's source (in the input folder or procesados/).
func (g *generator) sourcePath(src string) (string, error) {
	if !validSourceName(src) {
		return "", errors.New("fuente inválida")
	}
	for _, p := range []string{filepath.Join(g.in, src), filepath.Join(g.in, processedDir, src)} {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("no está la fuente %q (ni en la entrada ni en %s/)", src, processedDir)
}

func validSourceName(name string) bool {
	return name != "" && name == filepath.Base(name) && sourceKind(name) != ""
}

// Refine stores the version exported again from the editor over itself,
// with its new settings.
func (g *generator) Refine(name, reelPath string, meta json.RawMessage) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	path, err := g.outputPath(name)
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
	side := readSidecar(path)
	if len(meta) > 0 && json.Valid(meta) {
		side["settings"] = meta
	}
	side["stage"], side["refined"] = stageRefined, g.now().Format(time.RFC3339)
	g.logLocked(name, "refinado", "")
	return writeSidecar(path, side)
}

// ---------- HTTP ----------

func (srv *server) describe(dir string) []PubItem {
	p := &publisher{probe: map[string]mediaInfo{}}
	if srv.pub != nil {
		p = srv.pub
		p.mu.Lock()
		defer p.mu.Unlock()
	}
	return p.videosIn(dir)
}

func (srv *server) reviewOverview(w http.ResponseWriter, r *http.Request, g *generator) {
	o := g.Review(srv.describe)
	o.Publishing = srv.pub != nil
	o.Generated = hostDir("GENERATED_HOST_DIR", g.out)
	writeJSON(w, o)
}

func (srv *server) reviewAction(act func(*generator, string) error) func(http.ResponseWriter, *http.Request, *generator) {
	return func(w http.ResponseWriter, r *http.Request, g *generator) {
		if err := act(g, r.PathValue("name")); err != nil {
			pubError(w, err)
			return
		}
		srv.reviewOverview(w, r, g)
	}
}

func (srv *server) reviewMeta(w http.ResponseWriter, r *http.Request, g *generator) {
	var body struct {
		Title       string   `json:"title"`
		Description string   `json:"description"`
		Tags        []string `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad meta: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := g.SetVersionMeta(r.PathValue("name"), body.Title, body.Description, body.Tags); err != nil {
		pubError(w, err)
		return
	}
	srv.reviewOverview(w, r, g)
}

func (srv *server) reviewDiscardedVideo(w http.ResponseWriter, r *http.Request, g *generator) {
	name := r.PathValue("name")
	if !validName(name) {
		http.Error(w, "nombre inválido", http.StatusBadRequest)
		return
	}
	http.ServeFile(w, r, filepath.Join(g.out, discardedDir, name))
}

// reviewSpec is what the editor needs to reopen a version: its settings and
// where its source is.
func (srv *server) reviewSpec(w http.ResponseWriter, r *http.Request, g *generator) {
	name := r.PathValue("name")
	g.mu.Lock()
	path, err := g.outputPath(name)
	g.mu.Unlock()
	if err != nil {
		pubError(w, err)
		return
	}
	side := readSidecar(path)
	src, err := g.sourcePath(reviewItem(PubItem{Name: name}, side).Source)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeReelSpec(w, name, side, src, g.out, stageGenerated)
}

// writeReelSpec answers with what the editor needs to reopen the reel name
// (with JSON side, made from src): its settings, its source and the same
// reel in the other languages (same source and template, found in dir, if
// any), since an edit applies to all of them. Versions at stage skip are
// left out.
func writeReelSpec(w http.ResponseWriter, name string, side map[string]any, src, dir, skip string) {
	it := reviewItem(PubItem{Name: name}, side)
	siblings := []map[string]any{}
	entries := []os.DirEntry{}
	if dir != "" {
		entries, _ = os.ReadDir(dir)
	}
	for _, e := range entries {
		if e.IsDir() || !validName(e.Name()) || e.Name() == name {
			continue
		}
		sd := readSidecar(filepath.Join(dir, e.Name()))
		o := reviewItem(PubItem{Name: e.Name()}, sd)
		if o.Source == it.Source && o.Template == it.Template && o.Lang != it.Lang && o.Stage != skip {
			siblings = append(siblings, map[string]any{"name": o.Name, "lang": o.Lang, "stage": o.Stage, "settings": sd["settings"]})
		}
	}
	writeJSON(w, map[string]any{
		"name": name, "lang": it.Lang, "template": it.Template, "stage": it.Stage, "title": side["title"],
		"source":   sourceInfo(filepath.Base(src)),
		"settings": side["settings"],
		"siblings": siblings,
	})
}

func (srv *server) reviewSource(w http.ResponseWriter, r *http.Request, g *generator) {
	path, err := g.outputPath(r.PathValue("name"))
	if err != nil {
		pubError(w, err)
		return
	}
	it := reviewItem(PubItem{}, readSidecar(path))
	src, err := g.sourcePath(it.Source)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	http.ServeFile(w, r, src)
}

// reviewQueue sends a version to the publishing folder: refined, unless
// the settings skip refining (then chosen is enough).
func (srv *server) reviewQueue(w http.ResponseWriter, r *http.Request, g *generator) {
	g.mu.Lock()
	path, err := g.outputPath(r.PathValue("name"))
	skip := g.state.Settings.SkipRefine
	g.mu.Unlock()
	if err != nil {
		pubError(w, err)
		return
	}
	stage := reviewItem(PubItem{}, readSidecar(path)).Stage
	if stage != stageRefined && !(skip && stage == stageChosen) {
		http.Error(w, "primero hay que elegirla y refinarla", http.StatusConflict)
		return
	}
	srv.genToQueue(w, r, g)
}
