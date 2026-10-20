package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"log"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// The generator makes reels by itself: every photo or video that appears in
// the input folder becomes several reels, one per template, saved in the
// output folder (each with its JSON, the same format the publishing queue
// reads). Sources already done move to <input>/procesados/.
//
// To draw exactly what the editor draws, it opens the reel editor itself in
// a headless browser (reels.html?auto=<job>), one job at a time; the page
// loads the source, applies the template's base, waits for the shapes and
// sends the encoded reel back through /api/reels/mux (job=<id>).

var imageExts = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".gif": true, ".bmp": true}

// Templates per kind of source (as in web/templates.js), in the order the
// versions are made.
var autoTemplates = map[string][]string{
	"image": {"reveal", "levels", "compare", "curtain", "counter", "loop", "mosaic", "prefer"},
	"video": {"shapes", "levels", "compare", "curtain", "loop", "mosaic"},
}

var templateNames = map[string]string{
	"reveal": "Revelado", "loop": "Loop hipnótico", "shapes": "Video en formas", "levels": "En qué nivel",
	"compare": "Comparativa", "curtain": "Cortina", "counter": "Contador", "mosaic": "Mosaico de niveles", "prefer": "Cuál preferís",
}

const (
	processedDir = "procesados"
	genStateFile = ".generador.json"
	jobTimeout   = 40 * time.Minute
)

// GenSettings: whether the folder is watched, and which templates to make
// (empty: all the ones that fit the source).
type GenSettings struct {
	Enabled   bool        `json:"enabled"`
	Templates []string    `json:"templates"`
	Languages []string    `json:"languages"` // one version of each reel per language (e.g. es, en)
	LLM       LLMSettings `json:"llm"`       // who writes titles, descriptions, tags and translations
	// Versions can go to the publishing queue once chosen, without the
	// manual refining step (by default it's required).
	SkipRefine bool `json:"skipRefine"`
}

func (s GenSettings) langs() []string {
	if len(s.Languages) == 0 {
		return []string{"es", "en"}
	}
	return s.Languages
}

type GenJob struct {
	ID       string `json:"id"`
	Source   string `json:"source"` // file name in the input folder
	Kind     string `json:"kind"`   // image | video
	Template string `json:"template"`
	Lang     string `json:"lang"`
	// A copy of a queued reel in another language: its texts translated,
	// the same settings, and the result straight to the publishing queue.
	Dest     string          `json:"dest,omitempty"`   // "queue" for those
	From     string          `json:"from,omitempty"`   // the queued reel it copies
	Record   json.RawMessage `json:"record,omitempty"` // its settings, texts translated once done
	Copy     *Copy           `json:"copy,omitempty"`   // title, description and tags translated
	State    string          `json:"state"`            // queued | running | done | error
	Stage    string          `json:"stage,omitempty"`
	Progress float64         `json:"progress"`
	Output   string          `json:"output,omitempty"`
	Error    string          `json:"error,omitempty"`
	Created  time.Time       `json:"created"`
	Started  time.Time       `json:"started,omitzero"`
	Finished time.Time       `json:"finished,omitzero"`
}

type genState struct {
	Settings GenSettings   `json:"settings"`
	Jobs     []*GenJob     `json:"jobs"`
	Log      []pubLogEntry `json:"log"`
	// What was written for each source and template, by language (so all
	// the languages of a reel share one call to the model).
	Copies map[string]map[string]LangCopy `json:"copies"`
}

func copyKey(j *GenJob) string { return j.Source + "|" + j.Template }

type generator struct {
	in, out string
	chrome  string // headless browser ("" = not available)
	baseURL string // where the browser finds the editor
	now     func() time.Time

	mu      sync.Mutex
	state   genState
	seen    map[string]string // name -> size|mtime at the last scan (to wait until a copy ends)
	running *exec.Cmd
	wake    chan struct{}
	finish  chan string // job ids that ended (done or error)
	pub     *publisher  // where copies in other languages go
}

// findChrome returns the headless browser to use: CHROME_BIN, or the usual names.
func findChrome() string {
	if p := os.Getenv("CHROME_BIN"); p != "" {
		return p
	}
	for _, name := range []string{"chromium-browser", "chromium", "google-chrome", "google-chrome-stable"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

func newGenerator(in, out, chrome, baseURL string) (*generator, error) {
	for _, d := range []string{filepath.Join(in, processedDir), out} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	g := &generator{in: in, out: out, chrome: chrome, baseURL: baseURL, now: time.Now,
		seen: map[string]string{}, wake: make(chan struct{}, 1), finish: make(chan string, 4)}
	g.state.Settings.Enabled = true
	if b, err := os.ReadFile(filepath.Join(out, genStateFile)); err == nil {
		json.Unmarshal(b, &g.state)
	}
	// Jobs cut by a restart go back to the queue.
	for _, j := range g.state.Jobs {
		if j.State == "running" {
			j.State, j.Stage, j.Progress = "queued", "", 0
		}
	}
	return g, nil
}

func (g *generator) saveLocked() {
	// Keep the record short: finished jobs of sources no longer around go away.
	if len(g.state.Jobs) > 300 {
		g.state.Jobs = g.state.Jobs[len(g.state.Jobs)-300:]
	}
	b, _ := json.MarshalIndent(g.state, "", "  ")
	tmp := filepath.Join(g.out, genStateFile+".tmp")
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, filepath.Join(g.out, genStateFile))
	}
}

func (g *generator) logLocked(name, action, detail string) {
	g.state.Log = append(g.state.Log, pubLogEntry{Time: g.now(), Name: name, Action: action, Detail: detail})
	if len(g.state.Log) > maxLog {
		g.state.Log = g.state.Log[len(g.state.Log)-maxLog:]
	}
	log.Printf("generator: %s %s %s", action, name, detail)
}

func sourceKind(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch {
	case strings.HasPrefix(name, "."):
		return ""
	case imageExts[ext]:
		return "image"
	case videoExts[ext]:
		return "video"
	}
	return ""
}

func (g *generator) templatesFor(kind string) []string {
	want := map[string]bool{}
	for _, t := range g.state.Settings.Templates {
		want[t] = true
	}
	var out []string
	for _, t := range autoTemplates[kind] {
		if len(want) == 0 || want[t] {
			out = append(out, t)
		}
	}
	return out
}

// scan queues jobs for sources that appeared (and finished copying) since
// the last scan.
func (g *generator) scan() {
	entries, _ := os.ReadDir(g.in)
	g.mu.Lock()
	defer g.mu.Unlock()
	known := map[string]bool{}
	for _, j := range g.state.Jobs {
		if j.State != "done" || g.sourceExistsLocked(j.Source) {
			known[j.Source] = true
		}
	}
	seen := map[string]string{}
	for _, e := range entries {
		name := e.Name()
		kind := sourceKind(name)
		if e.IsDir() || kind == "" {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		sig := fmt.Sprintf("%d|%d", fi.Size(), fi.ModTime().UnixNano())
		seen[name] = sig
		// Wait until it stops changing (a copy in progress) and isn't known.
		if known[name] || g.seen[name] != sig || g.now().Sub(fi.ModTime()) < 3*time.Second {
			continue
		}
		if !g.state.Settings.Enabled {
			continue
		}
		g.enqueueLocked(name, kind, "fuente nueva")
	}
	g.seen = seen
	g.saveLocked()
}

// enqueueLocked queues a job per template and language for a new source.
func (g *generator) enqueueLocked(name, kind, why string) {
	tpls, langs := g.templatesFor(kind), g.state.Settings.langs()
	for _, t := range tpls {
		for _, l := range langs {
			g.state.Jobs = append(g.state.Jobs, &GenJob{ID: newID(), Source: name, Kind: kind, Template: t, Lang: l, State: "queued", Created: g.now()})
		}
	}
	g.logLocked(name, why, fmt.Sprintf("%d versiones: %s × %s", len(tpls)*len(langs), strings.Join(tpls, ", "), strings.Join(langs, ", ")))
}

func (g *generator) sourceExistsLocked(name string) bool {
	_, err := os.Stat(filepath.Join(g.in, name))
	return err == nil
}

func (g *generator) jobLocked(id string) *GenJob {
	for _, j := range g.state.Jobs {
		if j.ID == id {
			return j
		}
	}
	return nil
}

// next returns the next queued job (nil if none).
func (g *generator) next() *GenJob {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, j := range g.state.Jobs {
		// Copies for the queue go even with the generator paused.
		if !g.state.Settings.Enabled && j.Dest != "queue" {
			continue
		}
		if _, err := g.sourcePath(j.Source); j.State == "queued" && err == nil {
			j.State, j.Started, j.Stage, j.Progress, j.Error = "running", g.now(), "abriendo el editor", 0, ""
			g.saveLocked()
			return j
		}
	}
	return nil
}

// run makes one reel: the editor in a headless browser, until the page says
// it's done, fails, or takes too long.
func (g *generator) run(ctx context.Context, j *GenJob) {
	if j.Dest == "queue" {
		if err := g.translate(ctx, j); err != nil {
			g.end(j.ID, "", err)
			return
		}
	} else if g.chrome != "" {
		g.ensureCopy(ctx, j)
	}
	if g.chrome == "" {
		g.end(j.ID, "", errors.New("no hay un navegador para generar (instalá Chromium o definí CHROME_BIN)"))
		return
	}
	profile, _ := os.MkdirTemp("", "to-primitive-chrome-")
	defer os.RemoveAll(profile)
	cctx, cancel := context.WithTimeout(ctx, jobTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, g.chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage", "--mute-audio",
		"--autoplay-policy=no-user-gesture-required", "--no-first-run", "--hide-scrollbars",
		"--window-size=1280,900", "--user-data-dir="+profile,
		g.baseURL+"/reels.html?auto="+j.ID)
	cmd.Env = append(os.Environ(), "HOME="+profile)
	if err := cmd.Start(); err != nil {
		g.end(j.ID, "", fmt.Errorf("no se pudo abrir el navegador: %v", err))
		return
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	for {
		select {
		case id := <-g.finish:
			if id != j.ID {
				continue
			}
			cmd.Process.Kill()
			<-exited
			return
		case err := <-exited:
			if cctx.Err() != nil {
				g.end(j.ID, "", errors.New("tardó demasiado"))
			} else {
				g.end(j.ID, "", fmt.Errorf("el navegador se cerró: %v", err))
			}
			return
		}
	}
}

// end records how a job ended; once all the versions of a source are done,
// the source moves to procesados/.
func (g *generator) end(id, output string, failure error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	j := g.jobLocked(id)
	if j == nil || j.State != "running" {
		return
	}
	j.Finished = g.now()
	if failure != nil {
		j.State, j.Error = "error", failure.Error()
		g.logLocked(j.Source, "error", templateNames[j.Template]+": "+j.Error)
	} else {
		j.State, j.Output, j.Progress, j.Stage = "done", output, 1, ""
		g.logLocked(output, "generado", fmt.Sprintf("%s · %s en %.0f s", j.Source, templateNames[j.Template], j.Finished.Sub(j.Started).Seconds()))
	}
	if j.Dest == "queue" { // copies don't move their source
		g.saveLocked()
		select {
		case g.finish <- id:
		default:
		}
		return
	}
	pending := false
	for _, o := range g.state.Jobs {
		pending = pending || (o.Source == j.Source && (o.State == "queued" || o.State == "running"))
	}
	if !pending {
		for k := range g.state.Copies { // its texts aren't needed anymore
			if strings.HasPrefix(k, j.Source+"|") {
				delete(g.state.Copies, k)
			}
		}
	}
	if !pending && g.sourceExistsLocked(j.Source) {
		dest := filepath.Join(g.in, processedDir, j.Source)
		if err := os.Rename(filepath.Join(g.in, j.Source), dest); err == nil {
			g.logLocked(j.Source, "fuente procesada", "movida a "+processedDir+"/")
		} else {
			g.logLocked(j.Source, "error", "no se pudo mover a "+processedDir+"/: "+err.Error())
		}
	}
	g.saveLocked()
	select {
	case g.finish <- id:
	default:
	}
}

// Run scans the folder every few seconds and works through the queue.
func (g *generator) Run(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		g.scan()
		for j := g.next(); j != nil; j = g.next() {
			g.run(ctx, j)
			g.scan()
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-g.wake:
		}
	}
}

func (g *generator) poke() {
	select {
	case g.wake <- struct{}{}:
	default:
	}
}

// ---------- results ----------

// outputName is "<source> · <template>.mp4", without clashing.
// ensureCopy has the texts of the job's source and template written (in
// every language, in one go) before its reel is made. If the model fails,
// the rules write them, so a reel is never left without its copy.
func (g *generator) ensureCopy(ctx context.Context, j *GenJob) {
	g.mu.Lock()
	if _, ok := g.state.Copies[copyKey(j)]; ok {
		g.mu.Unlock()
		return
	}
	j.Stage = "escribiendo títulos y textos"
	settings := g.state.Settings
	g.mu.Unlock()

	req := CopyRequest{BaseTitle: titleFromName(j.Source), Template: j.Template, Langs: settings.langs(), Texts: templateTexts[j.Template], Shapes: templateShapes[j.Template]}
	req.Frame = sourceFrame(ctx, filepath.Join(g.in, j.Source), j.Kind)
	w := newCopywriter(settings.LLM)
	copies, err := w.Write(ctx, req)
	g.mu.Lock()
	defer g.mu.Unlock()
	if err != nil {
		g.logLocked(j.Source, "error", "textos: "+err.Error()+" (se usan las reglas)")
		copies, _ = rulesWriter{}.Write(ctx, req)
	} else {
		g.logLocked(j.Source, "textos", templateNames[j.Template]+" · "+w.Name())
	}
	if g.state.Copies == nil {
		g.state.Copies = map[string]map[string]LangCopy{}
	}
	g.state.Copies[copyKey(j)] = copies
	g.saveLocked()
}

// Shapes in each template's base (as in web/templates.js), for the copy.
var templateShapes = map[string]int{"reveal": 150, "loop": 150, "shapes": 150, "levels": 150, "compare": 200, "curtain": 200, "counter": 300, "mosaic": 500, "prefer": 150}

// sourceFrame is a JPEG of the source (the middle of a video) for the
// model to see; nil if it can't be made.
func sourceFrame(ctx context.Context, path, kind string) []byte {
	t := 0.0
	if kind == "video" {
		if info, err := probeMedia(ctx, path); err == nil {
			t = info.Duration / 2
		}
	}
	img, err := extractFrame(ctx, path, t, 768)
	if err != nil {
		return nil
	}
	var buf bytes.Buffer
	if jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}) != nil {
		return nil
	}
	return buf.Bytes()
}

func (g *generator) outputName(j *GenJob) string {
	base := strings.TrimSuffix(j.Source, filepath.Ext(j.Source)) + " · " + templateNames[j.Template] + " · " + j.Lang
	name := base + ".mp4"
	for i := 2; ; i++ {
		if _, err := os.Stat(filepath.Join(g.out, name)); errors.Is(err, os.ErrNotExist) {
			return name
		}
		name = fmt.Sprintf("%s (%d).mp4", base, i)
	}
}

// Complete stores the finished reel of a job (from /api/reels/mux).
func (g *generator) Complete(id, reelPath string, meta json.RawMessage) (string, error) {
	g.mu.Lock()
	j := g.jobLocked(id)
	if j == nil || j.State != "running" {
		g.mu.Unlock()
		return "", errors.New("ese trabajo no está en curso")
	}
	if j.Dest == "queue" {
		g.mu.Unlock()
		return g.completeCopy(j, reelPath, meta)
	}
	name := g.outputName(j)
	src, tpl, lang := j.Source, j.Template, j.Lang
	c, hasCopy := g.state.Copies[copyKey(j)][lang]
	g.mu.Unlock()

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
	side := map[string]any{"source": src, "template": tpl, "lang": lang, "generated": g.now().Format(time.RFC3339), "settings": meta}
	if hasCopy {
		side["title"], side["description"], side["tags"], side["hook"] = c.Title, c.Description, c.Tags, c.Hook
	} else {
		title, desc := reelTexts(meta)
		if title != "" {
			side["title"] = title
		}
		side["description"] = strings.TrimSpace(desc + "\n\n#Shorts")
	}
	writeSidecar(dest, side)
	g.end(id, name, nil)
	return name, nil
}

func (g *generator) Fail(id, msg string) {
	g.end(id, "", errors.New(msg))
}

func (g *generator) Progress(id, stage string, fraction float64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if j := g.jobLocked(id); j != nil && j.State == "running" {
		j.Stage, j.Progress = stage, fraction
	}
}

// GenOverview is what the generator page shows.
type GenOverview struct {
	InDir        string        `json:"in"`
	OutDir       string        `json:"out"`
	Browser      string        `json:"browser"`
	Settings     GenSettings   `json:"settings"`
	Templates    []string      `json:"templates"` // all, in order
	Jobs         []*GenJob     `json:"jobs"`      // newest first
	Outputs      []PubItem     `json:"outputs"`   // newest first
	Log          []pubLogEntry `json:"log"`
	Publishing   bool          `json:"publishing"` // there's a publishing folder to send them to
	Writer       string        `json:"writer"`     // who writes the texts now
	HasClaudeKey bool          `json:"hasClaudeKey"`
}

func (g *generator) Overview(probe func(string) []PubItem) GenOverview {
	g.mu.Lock()
	o := GenOverview{InDir: hostDir("INPUT_HOST_DIR", g.in), OutDir: hostDir("GENERATED_HOST_DIR", g.out), Browser: g.chrome,
		Settings: g.state.Settings}
	if o.Settings.Templates == nil {
		o.Settings.Templates = []string{}
	}
	all := map[string]bool{}
	for _, k := range []string{"image", "video"} {
		for _, t := range autoTemplates[k] {
			if !all[t] {
				all[t] = true
				o.Templates = append(o.Templates, t)
			}
		}
	}
	for i := len(g.state.Jobs) - 1; i >= 0; i-- {
		j := *g.state.Jobs[i]
		o.Jobs = append(o.Jobs, &j)
	}
	if o.Jobs == nil {
		o.Jobs = []*GenJob{}
	}
	for i := len(g.state.Log) - 1; i >= 0; i-- {
		o.Log = append(o.Log, g.state.Log[i])
	}
	g.mu.Unlock()
	o.Outputs = probe(g.out)
	sort.Slice(o.Outputs, func(i, j int) bool { return o.Outputs[i].Modified.After(o.Outputs[j].Modified) })
	if o.Outputs == nil {
		o.Outputs = []PubItem{}
	}
	return o
}

func hostDir(env, dir string) string {
	if h := os.Getenv(env); h != "" {
		return h
	}
	return dir
}

func (g *generator) SetSettings(s GenSettings) {
	g.mu.Lock()
	known := map[string]bool{}
	for _, t := range append(autoTemplates["image"], autoTemplates["video"]...) {
		known[t] = true
	}
	tpls := []string{}
	for _, t := range s.Templates {
		if known[t] {
			tpls = append(tpls, t)
		}
	}
	s.Templates = tpls
	langs := []string{}
	for _, l := range s.Languages {
		if l = strings.ToLower(strings.TrimSpace(l)); regexp.MustCompile(`^[a-z]{2}$`).MatchString(l) && !slices.Contains(langs, l) {
			langs = append(langs, l)
		}
	}
	s.Languages = langs
	if !slices.Contains([]string{"", "anthropic", "local", "none"}, s.LLM.Provider) {
		s.LLM.Provider = ""
	}
	g.state.Settings = s
	if s.Enabled {
		g.logLocked("", "generador", "activo")
	} else {
		g.logLocked("", "generador", "pausado")
	}
	g.saveLocked()
	g.mu.Unlock()
	g.poke()
}

// Retry puts a failed job back in the queue (and its source, if it was
// already moved to procesados/).
func (g *generator) Retry(id string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	j := g.jobLocked(id)
	if j == nil {
		return errNotFound
	}
	if j.State != "error" {
		return errors.New("solo se reintentan los que fallaron")
	}
	if j.Dest != "queue" && !g.sourceExistsLocked(j.Source) {
		if err := os.Rename(filepath.Join(g.in, processedDir, j.Source), filepath.Join(g.in, j.Source)); err != nil {
			return errors.New("la fuente ya no está")
		}
	}
	j.State, j.Error, j.Stage, j.Progress = "queued", "", "", 0
	g.saveLocked()
	defer g.poke()
	return nil
}

// ---------- HTTP ----------

func (srv *server) withGenerator(h func(http.ResponseWriter, *http.Request, *generator)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if srv.gen == nil {
			http.Error(w, "generator not configured", http.StatusServiceUnavailable)
			return
		}
		h(w, r, srv.gen)
	}
}

func (srv *server) genOverview(w http.ResponseWriter, r *http.Request, g *generator) {
	o := g.Overview(func(dir string) []PubItem {
		// The same description the publishing queue uses (title, duration, warnings).
		p := &publisher{probe: map[string]mediaInfo{}}
		if srv.pub != nil {
			p = srv.pub
			p.mu.Lock()
			defer p.mu.Unlock()
		}
		return p.videosIn(dir)
	})
	o.Settings.Languages = o.Settings.langs()
	o.Publishing = srv.pub != nil
	o.Writer = newCopywriter(o.Settings.LLM).Name()
	o.HasClaudeKey = os.Getenv("ANTHROPIC_API_KEY") != ""
	writeJSON(w, o)
}

func (srv *server) genSettings(w http.ResponseWriter, r *http.Request, g *generator) {
	var s GenSettings
	if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
		http.Error(w, "bad settings: "+err.Error(), http.StatusBadRequest)
		return
	}
	g.SetSettings(s)
	srv.genOverview(w, r, g)
}

func (srv *server) genJob(w http.ResponseWriter, r *http.Request, g *generator) {
	g.mu.Lock()
	j := g.jobLocked(r.PathValue("id"))
	var out map[string]any
	if j != nil {
		out = map[string]any{"id": j.ID, "template": j.Template, "kind": j.Kind, "lang": j.Lang,
			"source": map[string]string{"name": j.Source, "type": mime.TypeByExtension(strings.ToLower(filepath.Ext(j.Source)))}}
		if j.Record != nil { // a copy: the reel's own settings, texts translated
			out["record"] = j.Record
		}
		// The on-screen texts for this language, by phrase role.
		if c, ok := g.state.Copies[copyKey(j)][j.Lang]; ok {
			phrases := map[string]string{"title": c.Hook, "closing": c.Closing}
			for role, t := range c.Texts {
				phrases[role] = t
			}
			out["phrases"] = phrases
		}
	}
	g.mu.Unlock()
	if out == nil {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}
	writeJSON(w, out)
}

// genSource serves a job's source. With ?compat, a video comes as WebM
// (VP9 + Opus): browsers without H.264 (open-source Chromium builds) can
// still play it.
func (srv *server) genSource(w http.ResponseWriter, r *http.Request, g *generator) {
	g.mu.Lock()
	j := g.jobLocked(r.PathValue("id"))
	g.mu.Unlock()
	if j == nil {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}
	path, err := g.sourcePath(j.Source) // in the input folder or already in procesados/
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if r.URL.Query().Has("compat") && j.Kind == "video" {
		compat := filepath.Join(srv.dataDir, "compat-"+j.ID+".webm")
		if _, err := os.Stat(compat); err != nil {
			tmp := compat + ".part.webm"
			_, err := runFFmpeg(r.Context(), "ffmpeg", "-v", "error", "-y", "-i", path,
				"-c:v", "libvpx-vp9", "-deadline", "realtime", "-cpu-used", "8", "-row-mt", "1", "-b:v", "4M",
				"-c:a", "libopus", "-b:a", "128k", tmp)
			if err == nil {
				err = os.Rename(tmp, compat)
			}
			if err != nil {
				os.Remove(tmp)
				http.Error(w, "no se pudo convertir el video: "+err.Error(), http.StatusUnprocessableEntity)
				return
			}
		}
		w.Header().Set("Content-Type", "video/webm")
		path = compat
	}
	http.ServeFile(w, r, path)
}

func (srv *server) genProgress(w http.ResponseWriter, r *http.Request, g *generator) {
	var body struct {
		Stage    string  `json:"stage"`
		Fraction float64 `json:"fraction"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	g.Progress(r.PathValue("id"), body.Stage, body.Fraction)
	w.WriteHeader(http.StatusNoContent)
}

func (srv *server) genFail(w http.ResponseWriter, r *http.Request, g *generator) {
	var body struct {
		Error string `json:"error"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	if body.Error == "" {
		body.Error = "falló sin explicación"
	}
	g.Fail(r.PathValue("id"), body.Error)
	w.WriteHeader(http.StatusNoContent)
}

func (srv *server) genRetry(w http.ResponseWriter, r *http.Request, g *generator) {
	if err := g.Retry(r.PathValue("id")); err != nil {
		pubError(w, err)
		return
	}
	srv.genOverview(w, r, g)
}

func (g *generator) outputPath(name string) (string, error) {
	if !validName(name) {
		return "", errors.New("nombre inválido")
	}
	path := filepath.Join(g.out, name)
	if _, err := os.Stat(path); err != nil {
		return "", errNotFound
	}
	return path, nil
}

func (srv *server) genVideo(w http.ResponseWriter, r *http.Request, g *generator) {
	path, err := g.outputPath(r.PathValue("name"))
	if err != nil {
		pubError(w, err)
		return
	}
	if r.URL.Query().Has("download") {
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s", strings.ReplaceAll(r.PathValue("name"), " ", "%20")))
	}
	http.ServeFile(w, r, path)
}

// genToQueue moves a generated reel (and its JSON) to the publishing folder.
func (srv *server) genToQueue(w http.ResponseWriter, r *http.Request, g *generator) {
	if srv.pub == nil {
		http.Error(w, "publisher not configured", http.StatusServiceUnavailable)
		return
	}
	path, err := g.outputPath(r.PathValue("name"))
	if err != nil {
		pubError(w, err)
		return
	}
	got, err := srv.pub.Add(path, filepath.Base(path), readSidecar(path), "desde los generados")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	os.Remove(path)
	os.Remove(sidecarOf(path))
	writeJSON(w, map[string]string{"name": got})
}

func (srv *server) genDelete(w http.ResponseWriter, r *http.Request, g *generator) {
	path, err := g.outputPath(r.PathValue("name"))
	if err != nil {
		pubError(w, err)
		return
	}
	os.Remove(path)
	os.Remove(sidecarOf(path))
	w.WriteHeader(http.StatusNoContent)
}
