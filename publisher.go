package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // time zones without depending on the system's
)

// The publisher takes videos from a folder and publishes them following a
// schedule ("one a day at 19:00"). The folder is the meeting point: reels
// made by hand, exported from the editor or (later) generated automatically
// all land there. Each video may have a JSON next to it with the same name
// (title, description, tags). Published videos move to publicados/.
//
// Publishing goes through an uploader; for now it's simulated (nothing is
// uploaded), so the queue and the schedule can be tried first.

var videoExts = map[string]bool{".mp4": true, ".mov": true, ".m4v": true, ".webm": true}

const (
	publishedDir = "publicados"
	stateFile    = ".cola.json"
	maxLog       = 60
)

// PublishSettings is the schedule: at each of Times (HH:MM) on each of Days
// (0 = Sunday), in time zone TZ, the next video in the queue is published.
type PublishSettings struct {
	Enabled bool     `json:"enabled"`
	Times   []string `json:"times"`
	Days    []int    `json:"days"`
	TZ      string   `json:"tz"`
	// Where it publishes (youtube, tiktok); none: simulated.
	Platforms      []string `json:"platforms"`
	YouTubePrivacy string   `json:"youtubePrivacy"` // public | unlisted | private ("" = public)
	TikTokPrivacy  string   `json:"tiktokPrivacy"`  // PUBLIC_TO_EVERYONE | MUTUAL_FOLLOW_FRIENDS | FOLLOWER_OF_CREATOR | SELF_ONLY

	// What the person chose in the confirmation window for this one post
	// (never saved; nil for the schedule).
	Choice *PostChoice `json:"-"`
}

// PostChoice is what is confirmed before publishing one video by hand.
type PostChoice struct {
	Platforms []string `json:"platforms"`
	Again     []string `json:"again,omitempty"` // upload again where it's noted as already uploaded
	YouTube   *struct {
		Privacy string `json:"privacy"`
		Title   string `json:"title"`
	} `json:"youtube,omitempty"`
	TikTok *TikTokChoice `json:"tiktok,omitempty"`
}

// TikTokChoice is a TikTok Direct Post as the person set it up: nothing
// here has a default chosen for them (as TikTok asks).
type TikTokChoice struct {
	Privacy      string `json:"privacy"`
	Caption      string `json:"caption"`
	AllowComment bool   `json:"allowComment"`
	AllowDuet    bool   `json:"allowDuet"`
	AllowStitch  bool   `json:"allowStitch"`
	BrandOrganic bool   `json:"brandOrganic"` // promotes the creator's own brand
	BrandContent bool   `json:"brandContent"` // paid partnership (can't be private)
}

func defaultSettings() PublishSettings {
	return PublishSettings{Times: []string{"19:00"}, Days: []int{0, 1, 2, 3, 4, 5, 6}, TZ: "America/Argentina/Buenos_Aires"}
}

var hhmm = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

func (s *PublishSettings) validate() error {
	if len(s.Times) == 0 {
		return errors.New("hace falta al menos un horario")
	}
	for _, t := range s.Times {
		if !hhmm.MatchString(t) {
			return fmt.Errorf("horario inválido %q (usá HH:MM)", t)
		}
	}
	sort.Strings(s.Times)
	if len(s.Days) == 0 {
		return errors.New("hace falta al menos un día")
	}
	for _, d := range s.Days {
		if d < 0 || d > 6 {
			return fmt.Errorf("día inválido %d", d)
		}
	}
	if _, err := time.LoadLocation(s.TZ); err != nil {
		return fmt.Errorf("zona horaria desconocida %q", s.TZ)
	}
	var plats []string
	for _, p := range s.Platforms {
		if !slices.Contains(platformIDs, p) {
			return fmt.Errorf("plataforma desconocida %q", p)
		}
		if !slices.Contains(plats, p) {
			plats = append(plats, p)
		}
	}
	s.Platforms = plats
	if !slices.Contains([]string{"", "public", "unlisted", "private"}, s.YouTubePrivacy) {
		return fmt.Errorf("privacidad de YouTube inválida %q", s.YouTubePrivacy)
	}
	if !slices.Contains([]string{"", "PUBLIC_TO_EVERYONE", "MUTUAL_FOLLOW_FRIENDS", "FOLLOWER_OF_CREATOR", "SELF_ONLY"}, s.TikTokPrivacy) {
		return fmt.Errorf("privacidad de TikTok inválida %q", s.TikTokPrivacy)
	}
	return nil
}

func (s PublishSettings) loc() *time.Location {
	l, err := time.LoadLocation(s.TZ)
	if err != nil {
		return time.UTC
	}
	return l
}

// slotsOn returns the publishing times of the day containing t (in the
// schedule's time zone), if that day is a publishing day.
func (s PublishSettings) slotsOn(t time.Time) []time.Time {
	loc := s.loc()
	t = t.In(loc)
	ok := false
	for _, d := range s.Days {
		ok = ok || time.Weekday(d) == t.Weekday()
	}
	if !ok {
		return nil
	}
	out := make([]time.Time, 0, len(s.Times))
	for _, hm := range s.Times {
		h, _ := strconv.Atoi(hm[:2])
		m, _ := strconv.Atoi(hm[3:])
		out = append(out, time.Date(t.Year(), t.Month(), t.Day(), h, m, 0, 0, loc))
	}
	return out
}

// slotsAfter returns the next n publishing times strictly after t.
func (s PublishSettings) slotsAfter(t time.Time, n int) []time.Time {
	var out []time.Time
	day := t.In(s.loc())
	for i := 0; i < 800 && len(out) < n; i++ {
		for _, slot := range s.slotsOn(day.AddDate(0, 0, i)) {
			if slot.After(t) && len(out) < n {
				out = append(out, slot)
			}
		}
	}
	return out
}

// lastSlot returns the latest publishing time at or before t (zero if none
// in the past two weeks).
func (s PublishSettings) lastSlot(t time.Time) time.Time {
	day := t.In(s.loc())
	for i := 0; i < 15; i++ {
		slots := s.slotsOn(day.AddDate(0, 0, -i))
		for j := len(slots) - 1; j >= 0; j-- {
			if !slots[j].After(t) {
				return slots[j]
			}
		}
	}
	return time.Time{}
}

type pubLogEntry struct {
	Time   time.Time `json:"time"`
	Name   string    `json:"name"`
	Action string    `json:"action"`
	Detail string    `json:"detail,omitempty"`
}

// pubState is kept in the folder itself (.cola.json), so it travels with it.
type pubState struct {
	Settings PublishSettings `json:"settings"`
	Order    []string        `json:"order"`   // preferred order (names); the rest go by date
	Skipped  []string        `json:"skipped"` // stay in the folder, not published
	LastSlot time.Time       `json:"lastSlot"`
	Log      []pubLogEntry   `json:"log"`
}

// PubItem is a video in the queue or among the published ones.
type PubItem struct {
	Name        string                  `json:"name"`
	Size        int64                   `json:"size"`
	Modified    time.Time               `json:"modified"`
	Duration    float64                 `json:"duration"`
	Width       int                     `json:"width"`
	Height      int                     `json:"height"`
	Title       string                  `json:"title"`
	Description string                  `json:"description"`
	Tags        []string                `json:"tags"`
	Skipped     bool                    `json:"skipped,omitempty"`
	ScheduledAt *time.Time              `json:"scheduledAt,omitempty"`
	PublishedAt *time.Time              `json:"publishedAt,omitempty"`
	Result      string                  `json:"result,omitempty"`
	Brand       string                  `json:"brand,omitempty"`      // the reel's brand (its record), e.g. @account
	Version     string                  `json:"version"`              // changes whenever the video or its JSON do (for the browser's cache)
	Editable    bool                    `json:"editable,omitempty"`   // has its settings and source: it can reopen in the reel editor
	Duplicable  bool                    `json:"duplicable,omitempty"` // from the generator: it can be made again in another language
	Warnings    []string                `json:"warnings,omitempty"`
	Silent      bool                    `json:"silent,omitempty"`     // the file has no sound track
	Publishing  bool                    `json:"publishing,omitempty"` // being uploaded now
	Progress    float64                 `json:"progress,omitempty"`   // how much of it is up (0..1), while publishing
	Uploads     map[string]uploadResult `json:"uploads,omitempty"`    // where it already went (by platform)
}

// uploader publishes one video (on the platforms the settings say) and
// says where it went.
type uploader interface {
	Upload(ctx context.Context, path string, item PubItem, s PublishSettings) (string, error)
}

// simulatedUploader publishes nothing: it lets the queue and the schedule
// run for real before connecting a platform.
type simulatedUploader struct{}

func (simulatedUploader) Upload(ctx context.Context, path string, item PubItem, s PublishSettings) (string, error) {
	return "simulado (no se subió a ninguna plataforma)", nil
}

type publisher struct {
	dir string
	up  uploader
	now func() time.Time

	mu    sync.Mutex
	state pubState
	probe map[string]mediaInfo // by name|size|mtime
	busy  map[string]float64   // being published now (the upload runs unlocked): how much is up

	editable func(side map[string]any) bool // whether a video's source can be found (set by the server)
}

func newPublisher(dir string, up uploader) (*publisher, error) {
	if err := os.MkdirAll(filepath.Join(dir, publishedDir), 0o755); err != nil {
		return nil, err
	}
	p := &publisher{dir: dir, up: up, now: time.Now, probe: map[string]mediaInfo{}, busy: map[string]float64{}}
	p.state.Settings = defaultSettings()
	if b, err := os.ReadFile(filepath.Join(dir, stateFile)); err == nil {
		if err := json.Unmarshal(b, &p.state); err != nil {
			log.Printf("publisher: ignoring unreadable %s: %v", stateFile, err)
		}
		if p.state.Settings.validate() != nil {
			p.state.Settings = defaultSettings()
		}
	}
	return p, nil
}

func (p *publisher) saveLocked() error {
	b, _ := json.MarshalIndent(p.state, "", "  ")
	tmp := filepath.Join(p.dir, stateFile+".tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(p.dir, stateFile))
}

func (p *publisher) logLocked(name, action, detail string) {
	p.state.Log = append(p.state.Log, pubLogEntry{Time: p.now(), Name: name, Action: action, Detail: detail})
	if len(p.state.Log) > maxLog {
		p.state.Log = p.state.Log[len(p.state.Log)-maxLog:]
	}
	log.Printf("publisher: %s %s %s", action, name, detail)
}

// validName accepts the plain name of a video file (no paths, not hidden).
func validName(name string) bool {
	return name != "" && name == filepath.Base(name) && !strings.HasPrefix(name, ".") &&
		videoExts[strings.ToLower(filepath.Ext(name))]
}

func sidecarOf(path string) string { return strings.TrimSuffix(path, filepath.Ext(path)) + ".json" }

// readSidecar reads the JSON next to a video (empty if there's none).
func readSidecar(path string) map[string]any {
	m := map[string]any{}
	if b, err := os.ReadFile(sidecarOf(path)); err == nil {
		json.Unmarshal(b, &m)
	}
	return m
}

func writeSidecar(path string, m map[string]any) error {
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(sidecarOf(path), b, 0o644)
}

// item describes the video at path.
func (p *publisher) item(path string, fi os.FileInfo) PubItem {
	name := fi.Name()
	it := PubItem{Name: name, Size: fi.Size(), Modified: fi.ModTime()}
	// An edited reel keeps its date: its JSON's date tells the versions apart.
	it.Version = fmt.Sprintf("%x-%x", fi.Size(), fi.ModTime().UnixNano())
	if si, err := os.Stat(sidecarOf(path)); err == nil {
		it.Version += fmt.Sprintf("-%x", si.ModTime().UnixNano())
	}
	key := path + "|" + it.Version
	info, ok := p.probe[key]
	if !ok {
		var err error
		if info, err = probeMedia(context.Background(), path); err == nil {
			info.Audio, _ = hasAudio(context.Background(), path)
			p.probe[key] = info
		}
	}
	it.Duration, it.Width, it.Height = info.Duration, info.Width, info.Height

	m := readSidecar(path)
	it.Title, _ = m["title"].(string)
	it.Description, _ = m["description"].(string)
	if tags, ok := m["tags"].([]any); ok {
		for _, t := range tags {
			if s, ok := t.(string); ok {
				it.Tags = append(it.Tags, s)
			}
		}
	}
	if it.Title == "" {
		it.Title = strings.TrimSpace(strings.NewReplacer("_", " ", "-", " ").Replace(strings.TrimSuffix(name, filepath.Ext(name))))
	}
	if it.Description == "" {
		it.Description = "#Shorts"
	}
	if s, ok := m["publishedAt"].(string); ok {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			it.PublishedAt = &t
		}
	}
	it.Result, _ = m["result"].(string)
	if u, ok := m["uploads"]; ok {
		b, _ := json.Marshal(u)
		json.Unmarshal(b, &it.Uploads)
	}
	// Without sound: it was made with a song but the file lost it, or it
	// never had one (a reel without audio does worse on every platform).
	it.Silent = info.Duration > 0 && !info.Audio
	hadSound := false
	if s, ok := m["settings"].(map[string]any); ok {
		a, _ := s["audio"].(map[string]any)
		hadSound = a != nil && a["mode"] != nil && a["mode"] != "none"
	}
	switch {
	case it.Silent && hadSound:
		it.Warnings = append(it.Warnings, "no tiene sonido, aunque se armó con sonido: abrilo con Editar reel y guardalo de nuevo")
	case it.Silent:
		it.Warnings = append(it.Warnings, "sin audio: agregale una canción antes de publicarlo (editor de reels, sección Audio)")
	}
	if s, ok := m["settings"].(map[string]any); ok {
		it.Brand, _ = s["brand"].(string)
		it.Brand = strings.TrimSpace(it.Brand)
		src, _ := m["source"].(string)
		it.Duplicable = m["reel"] == nil && validSourceName(src)
		it.Editable = p.editable != nil && p.editable(m)
	}

	// What would keep it from being a Short.
	if info.Duration > 60.5 {
		it.Warnings = append(it.Warnings, fmt.Sprintf("dura %.0f s: como Short conviene hasta 60 s", info.Duration))
	}
	if info.Width > info.Height {
		it.Warnings = append(it.Warnings, "es horizontal: un Short tiene que ser vertical o cuadrado")
	}
	if info.Duration == 0 {
		it.Warnings = append(it.Warnings, "no se pudo leer el video")
	}
	return it
}

// videosIn lists the videos directly inside dir.
func (p *publisher) videosIn(dir string) []PubItem {
	entries, _ := os.ReadDir(dir)
	var out []PubItem
	for _, e := range entries {
		if e.IsDir() || !validName(e.Name()) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, p.item(filepath.Join(dir, e.Name()), fi))
	}
	return out
}

// queueLocked is the queue in publishing order: the preferred order first,
// then the rest from oldest to newest. Skipped videos stay in place but get
// no time.
func (p *publisher) queueLocked() []PubItem {
	items := p.videosIn(p.dir)
	for i := range items {
		items[i].Progress, items[i].Publishing = p.busy[items[i].Name]
	}
	rank := map[string]int{}
	for i, n := range p.state.Order {
		rank[n] = i
	}
	sort.SliceStable(items, func(i, j int) bool {
		ri, iok := rank[items[i].Name]
		rj, jok := rank[items[j].Name]
		if iok != jok {
			return iok
		}
		if iok {
			return ri < rj
		}
		if !items[i].Modified.Equal(items[j].Modified) {
			return items[i].Modified.Before(items[j].Modified)
		}
		return items[i].Name < items[j].Name
	})
	skipped := map[string]bool{}
	for _, n := range p.state.Skipped {
		skipped[n] = true
	}
	var slots []time.Time
	if p.state.Settings.Enabled {
		slots = p.state.Settings.slotsAfter(p.now(), len(items))
	}
	k := 0
	for i := range items {
		items[i].Skipped = skipped[items[i].Name]
		if !items[i].Skipped && k < len(slots) {
			t := slots[k]
			items[i].ScheduledAt = &t
			k++
		}
	}
	return items
}

// Overview is everything the publishing page shows.
type Overview struct {
	Dir       string          `json:"dir"`
	Settings  PublishSettings `json:"settings"`
	Now       time.Time       `json:"now"`
	NextSlot  *time.Time      `json:"nextSlot,omitempty"`
	Queue     []PubItem       `json:"queue"`
	Published []PubItem       `json:"published"`
	Discarded []PubItem       `json:"discarded"` // last discarded first
	Log       []pubLogEntry   `json:"log"`
	Uploader  string          `json:"uploader"`
}

func (p *publisher) Overview() Overview {
	p.mu.Lock()
	defer p.mu.Unlock()
	dir := p.dir
	if host := os.Getenv("PUBLISH_HOST_DIR"); host != "" { // in Docker: the folder on the computer
		dir = host
	}
	o := Overview{Dir: dir, Settings: p.state.Settings, Now: p.now(), Queue: p.queueLocked(), Uploader: fmt.Sprintf("%T", p.up)}
	if o.Queue == nil {
		o.Queue = []PubItem{}
	}
	if next := p.state.Settings.slotsAfter(p.now(), 1); len(next) > 0 && p.state.Settings.Enabled {
		o.NextSlot = &next[0]
	}
	o.Published = p.videosIn(filepath.Join(p.dir, publishedDir))
	sort.Slice(o.Published, func(i, j int) bool { // newest first
		a, b := o.Published[i].PublishedAt, o.Published[j].PublishedAt
		if a == nil || b == nil {
			return b == nil && a != nil
		}
		return a.After(*b)
	})
	if o.Published == nil {
		o.Published = []PubItem{}
	}
	o.Discarded = p.discardedLocked()
	o.Log = make([]pubLogEntry, 0, len(p.state.Log))
	for i := len(p.state.Log) - 1; i >= 0; i-- {
		o.Log = append(o.Log, p.state.Log[i])
	}
	return o
}

// SetSettings changes the schedule. Turning it on (or changing it) starts
// counting from now: earlier times are not caught up.
func (p *publisher) SetSettings(s PublishSettings) error {
	if err := s.validate(); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state.Settings = s
	p.state.LastSlot = p.now()
	if s.Enabled {
		p.logLocked("", "regla", fmt.Sprintf("activa: %s, días %v (%s)", strings.Join(s.Times, ", "), s.Days, s.TZ))
	} else {
		p.logLocked("", "regla", "pausada")
	}
	return p.saveLocked()
}

func (p *publisher) SetOrder(names []string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state.Order = nil
	for _, n := range names {
		if validName(n) {
			p.state.Order = append(p.state.Order, n)
		}
	}
	return p.saveLocked()
}

func (p *publisher) SetSkipped(name string, skip bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, err := p.queuedPath(name); err != nil {
		return err
	}
	out := p.state.Skipped[:0]
	for _, n := range p.state.Skipped {
		if n != name {
			out = append(out, n)
		}
	}
	if skip {
		out = append(out, name)
	}
	p.state.Skipped = out
	return p.saveLocked()
}

var errNotFound = errors.New("no está en la carpeta")

func (p *publisher) queuedPath(name string) (string, error) {
	if !validName(name) {
		return "", errors.New("nombre inválido")
	}
	path := filepath.Join(p.dir, name)
	if _, err := os.Stat(path); err != nil {
		return "", errNotFound
	}
	return path, nil
}

func (p *publisher) publishedPath(name string) (string, error) {
	if !validName(name) {
		return "", errors.New("nombre inválido")
	}
	path := filepath.Join(p.dir, publishedDir, name)
	if _, err := os.Stat(path); err != nil {
		return "", errNotFound
	}
	return path, nil
}

// SetMeta writes title, description and tags to the video's JSON (keeping
// whatever else is in it).
func (p *publisher) SetMeta(name, title, description string, tags []string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	path, err := p.queuedPath(name)
	if err != nil {
		return err
	}
	m := readSidecar(path)
	m["title"], m["description"], m["tags"] = strings.TrimSpace(title), strings.TrimSpace(description), tags
	return writeSidecar(path, m)
}

// Publish publishes one video now (by name), or the next in the queue if
// name is "". It returns the published name ("" if the queue was empty).
func (p *publisher) Publish(ctx context.Context, name, why string) (string, error) {
	return p.PublishWith(ctx, name, why, nil)
}

// PublishWith publishes as Publish, with what was confirmed for this post.
func (p *publisher) PublishWith(ctx context.Context, name, why string, choice *PostChoice) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var it *PubItem
	for _, q := range p.queueLocked() {
		if _, busy := p.busy[q.Name]; (name == "" && !q.Skipped && !busy) || q.Name == name {
			q := q
			it = &q
			break
		}
	}
	if it == nil {
		if name != "" {
			return "", errNotFound
		}
		p.logLocked("", "sin videos", why+": la cola está vacía")
		p.saveLocked()
		return "", nil
	}
	path := filepath.Join(p.dir, it.Name)
	// Uploading takes a while: the queue stays usable meanwhile, and this
	// video can't be moved or edited until it's done.
	if _, busy := p.busy[it.Name]; busy {
		return "", errors.New("ya se está publicando")
	}
	p.busy[it.Name] = 0
	settings := p.state.Settings
	if choice != nil {
		for _, pl := range choice.Platforms {
			if !slices.Contains(platformIDs, pl) {
				return "", fmt.Errorf("plataforma desconocida %q", pl)
			}
		}
		settings.Platforms, settings.Choice = choice.Platforms, choice
	}
	p.logLocked(it.Name, "publicando", why)
	p.mu.Unlock()
	result, err := func() (result string, err error) {
		defer func() { // whatever happens, the lock is taken back
			if r := recover(); r != nil {
				err = fmt.Errorf("falló la subida: %v", r)
			}
			p.mu.Lock()
		}()
		name := it.Name
		return p.up.Upload(withProgress(ctx, func(f float64) {
			p.mu.Lock()
			if _, ok := p.busy[name]; ok {
				p.busy[name] = f
			}
			p.mu.Unlock()
		}), path, *it, settings)
	}()
	delete(p.busy, it.Name)
	if err != nil {
		p.logLocked(it.Name, "error", err.Error())
		p.saveLocked()
		return "", err
	}
	// Move it (and its JSON) to publicados/, noting when and where.
	dest := filepath.Join(p.dir, publishedDir, it.Name)
	for i := 2; ; i++ {
		if _, err := os.Stat(dest); errors.Is(err, os.ErrNotExist) {
			break
		}
		ext := filepath.Ext(it.Name)
		dest = filepath.Join(p.dir, publishedDir, fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(it.Name, ext), i, ext))
	}
	m := readSidecar(path)
	m["title"], m["description"], m["tags"] = it.Title, it.Description, it.Tags
	m["publishedAt"], m["result"] = p.now().Format(time.RFC3339), result
	if err := os.Rename(path, dest); err != nil {
		p.logLocked(it.Name, "error", "no se pudo mover: "+err.Error())
		p.saveLocked()
		return "", err
	}
	os.Remove(sidecarOf(path))
	writeSidecar(dest, m)
	p.forgetLocked(it.Name)
	p.logLocked(filepath.Base(dest), "publicado", why+" · "+result)
	return filepath.Base(dest), p.saveLocked()
}

func (p *publisher) forgetLocked(name string) {
	drop := func(list []string) []string {
		out := list[:0]
		for _, n := range list {
			if n != name {
				out = append(out, n)
			}
		}
		return out
	}
	p.state.Order = drop(p.state.Order)
	p.state.Skipped = drop(p.state.Skipped)
}

// Requeue moves a published video back to the queue (at the front).
func (p *publisher) Requeue(name string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	path, err := p.publishedPath(name)
	if err != nil {
		return err
	}
	dest := filepath.Join(p.dir, name)
	if _, err := os.Stat(dest); err == nil {
		return errors.New("ya hay un video con ese nombre en la cola")
	}
	m := readSidecar(path)
	delete(m, "publishedAt")
	delete(m, "result")
	// Back in the queue it's a new post: what it was uploaded as before
	// (and maybe deleted since) goes to the history, not in its way.
	if u, ok := m["uploads"]; ok {
		hist, _ := m["previousUploads"].([]any)
		m["previousUploads"] = append(hist, u)
		delete(m, "uploads")
	}
	if err := os.Rename(path, dest); err != nil {
		return err
	}
	os.Remove(sidecarOf(path))
	writeSidecar(dest, m)
	p.state.Order = append([]string{name}, p.state.Order...)
	p.logLocked(name, "devuelto a la cola", "")
	return p.saveLocked()
}

// Add copies a video into the queue folder (e.g. from the reel library),
// with its JSON. It returns the name it got.
func (p *publisher) Add(src, name string, meta map[string]any, from string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !validName(name) {
		return "", errors.New("nombre inválido")
	}
	dest := filepath.Join(p.dir, name)
	for i := 2; ; i++ {
		if _, err := os.Stat(dest); errors.Is(err, os.ErrNotExist) {
			break
		}
		ext := filepath.Ext(name)
		dest = filepath.Join(p.dir, fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(name, ext), i, ext))
	}
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	if err := saveFile(dest, in); err != nil {
		return "", err
	}
	if err := writeSidecar(dest, meta); err != nil {
		return "", err
	}
	p.logLocked(filepath.Base(dest), "agregado", from)
	return filepath.Base(dest), p.saveLocked()
}

// tick publishes the next video if a publishing time has come since the
// last one handled. If the server was off over several times, only one
// video goes out (no bursts).
func (p *publisher) tick(ctx context.Context) {
	p.mu.Lock()
	s := p.state.Settings
	slot := s.lastSlot(p.now())
	due := s.Enabled && !slot.IsZero() && slot.After(p.state.LastSlot)
	if due {
		p.state.LastSlot = slot
		p.saveLocked()
	}
	p.mu.Unlock()
	if due {
		if _, err := p.Publish(ctx, "", "regla de las "+slot.In(s.loc()).Format("15:04")); err != nil {
			log.Printf("publisher: %v", err)
		}
	}
}

// Run checks the schedule every interval until ctx ends.
func (p *publisher) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		p.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ---------- HTTP ----------

func (srv *server) withPublisher(h func(http.ResponseWriter, *http.Request, *publisher)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if srv.pub == nil {
			http.Error(w, "publisher not configured", http.StatusServiceUnavailable)
			return
		}
		h(w, r, srv.pub)
	}
}

func pubError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, errNotFound) {
		status = http.StatusNotFound
	}
	http.Error(w, err.Error(), status)
}

func (srv *server) pubOverview(w http.ResponseWriter, r *http.Request, p *publisher) {
	writeJSON(w, p.Overview())
}

func (srv *server) pubSettings(w http.ResponseWriter, r *http.Request, p *publisher) {
	var s PublishSettings
	if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
		http.Error(w, "bad settings: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := p.SetSettings(s); err != nil {
		pubError(w, err)
		return
	}
	writeJSON(w, p.Overview())
}

func (srv *server) pubOrder(w http.ResponseWriter, r *http.Request, p *publisher) {
	var body struct {
		Names []string `json:"names"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad order: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := p.SetOrder(body.Names); err != nil {
		pubError(w, err)
		return
	}
	writeJSON(w, p.Overview())
}

func (srv *server) pubSkip(w http.ResponseWriter, r *http.Request, p *publisher) {
	var body struct {
		Skip bool `json:"skip"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	if err := p.SetSkipped(r.PathValue("name"), body.Skip); err != nil {
		pubError(w, err)
		return
	}
	writeJSON(w, p.Overview())
}

func (srv *server) pubMeta(w http.ResponseWriter, r *http.Request, p *publisher) {
	var body struct {
		Title       string   `json:"title"`
		Description string   `json:"description"`
		Tags        []string `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad meta: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := p.SetMeta(r.PathValue("name"), body.Title, body.Description, body.Tags); err != nil {
		pubError(w, err)
		return
	}
	writeJSON(w, p.Overview())
}

func (srv *server) pubNow(w http.ResponseWriter, r *http.Request, p *publisher) {
	var choice *PostChoice
	if r.ContentLength != 0 {
		choice = &PostChoice{}
		if err := json.NewDecoder(r.Body).Decode(choice); err != nil {
			http.Error(w, "bad choice: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	if _, err := p.PublishWith(r.Context(), r.PathValue("name"), "a mano", choice); err != nil {
		pubError(w, err)
		return
	}
	writeJSON(w, p.Overview())
}

func (srv *server) pubRequeue(w http.ResponseWriter, r *http.Request, p *publisher) {
	if err := p.Requeue(r.PathValue("name")); err != nil {
		pubError(w, err)
		return
	}
	writeJSON(w, p.Overview())
}

// pubVideo serves a video of the queue, or (where) of publicados/ or
// descartados/.
func (srv *server) pubVideo(where string) func(http.ResponseWriter, *http.Request, *publisher) {
	return func(w http.ResponseWriter, r *http.Request, p *publisher) {
		find := p.queuedPath
		switch where {
		case publishedDir:
			find = p.publishedPath
		case discardedDir:
			find = p.discardedPath
		}
		path, err := find(r.PathValue("name"))
		if err != nil {
			pubError(w, err)
			return
		}
		if r.URL.Query().Has("download") {
			name := filepath.Base(path)
			ascii := strings.Map(func(r rune) rune {
				if r < 32 || r > 126 || r == '"' || r == '\\' {
					return '_'
				}
				return r
			}, name)
			w.Header().Set("Content-Disposition", `attachment; filename="`+ascii+`"; filename*=UTF-8''`+url.PathEscape(name))
		}
		serveFresh(w, r, path)
	}
}

// serveFresh serves a video that can be replaced in place: an edited
// reel keeps its old date (to keep its place in the queue), so the
// browser must check it by content (ETag: size and the dates of the video
// and its JSON, which is rewritten on every edit) instead of trusting a
// copy it has.
func serveFresh(w http.ResponseWriter, r *http.Request, path string) {
	if fi, err := os.Stat(path); err == nil {
		tag := fmt.Sprintf("%x-%x", fi.Size(), fi.ModTime().UnixNano())
		if si, err := os.Stat(sidecarOf(path)); err == nil {
			tag += fmt.Sprintf("-%x", si.ModTime().UnixNano())
		}
		w.Header().Set("ETag", `"`+tag+`"`)
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, path)
}

// reelQueue sends a reel from the library to the publishing folder, with a
// title and description taken from its phrases.
func (srv *server) reelQueue(w http.ResponseWriter, r *http.Request, p *publisher) {
	vpath, mpath, err := srv.lib.paths(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := os.Stat(vpath); err != nil {
		http.Error(w, "reel not found", http.StatusNotFound)
		return
	}
	var entry ReelEntry
	if b, err := os.ReadFile(mpath); err == nil {
		json.Unmarshal(b, &entry)
	}
	meta := map[string]any{"source": "editor de reels", "reel": entry.ID, "settings": entry.Meta}
	title, desc := reelTexts(entry.Meta)
	if title != "" {
		meta["title"] = title
	}
	meta["description"] = strings.TrimSpace(desc + "\n\n#Shorts")
	name := "reel " + entry.Created.In(time.Local).Format("2006-01-02 15.04") + " " + r.PathValue("id")[:6] + ".mp4"
	if entry.Created.IsZero() {
		name = "reel " + r.PathValue("id") + ".mp4"
	}
	got, err := p.Add(vpath, name, meta, "desde el editor de reels")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"name": got})
}

// reelTexts picks a title (the title or the question) and a description
// (the closing) from a reel's record, filling {formas} and {tipo} from its
// settings (other variables change along the reel and are dropped).
func reelTexts(meta json.RawMessage) (title, desc string) {
	var m struct {
		Phrases []struct{ Role, Text string } `json:"phrases"`
		Shapes  struct{ Shapes, Mode int }    `json:"shapes"`
	}
	json.Unmarshal(meta, &m)
	shapeNames := map[int]string{0: "formas", 1: "triángulos", 2: "rectángulos", 3: "elipses", 4: "círculos", 5: "rectángulos", 6: "curvas", 7: "elipses", 8: "polígonos"}
	vars := map[string]string{}
	if m.Shapes.Shapes > 0 {
		vars["formas"], vars["tipo"] = strconv.Itoa(m.Shapes.Shapes), shapeNames[m.Shapes.Mode]
	}
	fill := regexp.MustCompile(`\s*\{(\w+)\}`)
	for _, ph := range m.Phrases {
		t := strings.TrimSpace(fill.ReplaceAllStringFunc(ph.Text, func(v string) string {
			k := strings.Trim(strings.TrimSpace(v), "{}")
			if val, ok := vars[k]; ok {
				return strings.Replace(v, "{"+k+"}", val, 1)
			}
			return ""
		}))
		switch {
		case (ph.Role == "title" || ph.Role == "question") && title == "":
			title = t
		case ph.Role == "closing" && desc == "":
			desc = t
		}
	}
	return title, desc
}
