// Command to-primitive serves a web editor around fogleman/primitive: upload
// an image or a video, tweak the parameters and watch the shapes being added
// live, then compute the whole video as keyframes that the browser animates
// and exports.
package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"
)

//go:embed web
var webFS embed.FS

const (
	maxUpload   = 64 << 20
	maxSessions = 12
	maxVideos   = 2
	maxLives    = 2
	maxFrame    = 8 << 20 // one live frame (JPEG)
	// Frames used to tune parameters are capped at this size: the working
	// resolution never goes above 1024 anyway.
	maxFrameSize = 1024
)

var numWorkers = runtime.NumCPU()

type server struct {
	dataDir string
	lib     *library
	pub     *publisher // nil: publishing not configured
	gen     *generator // nil: automatic generation not configured
	acc     *accounts  // connected YouTube / TikTok accounts

	mu       sync.Mutex
	sessions map[string]*Session
	videos   map[string]*Video
	lives    map[string]*LiveSession
}

func main() {
	addr := flag.String("addr", envOr("ADDR", ":8080"), "listen address")
	dataDir := flag.String("data", envOr("DATA_DIR", filepath.Join(os.TempDir(), "to-primitive")), "directory for uploaded videos")
	reelsDir := flag.String("reels", envOr("REELS_DIR", "reels"), "directory where finished reels are kept")
	publishDir := flag.String("publish", envOr("PUBLISH_DIR", "publicar"), "folder of videos waiting to be published")
	inputDir := flag.String("input", envOr("INPUT_DIR", "entrada"), "folder of photos and videos to turn into reels automatically")
	generatedDir := flag.String("generated", envOr("GENERATED_DIR", "generados"), "folder where automatically generated reels go")
	flag.Parse()

	// Videos don't survive a restart (sessions live in memory), so start clean.
	os.RemoveAll(*dataDir)
	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		log.Fatal(err)
	}
	// Finished reels do survive: never cleaned up here.
	if err := os.MkdirAll(*reelsDir, 0o755); err != nil {
		log.Fatal(err)
	}

	srv := newServer(*dataDir, *reelsDir)
	// The accounts live with the library (a Docker volume), not in the
	// publishing folder.
	os.MkdirAll(*reelsDir, 0o755)
	srv.acc = newAccounts(filepath.Join(*reelsDir, ".cuentas.json"))
	pub, err := newPublisher(*publishDir, &platformUploader{acc: srv.acc, poll: 3 * time.Second, wait: 90 * time.Second})
	if err != nil {
		log.Fatal(err)
	}
	srv.pub = pub
	go pub.Run(context.Background(), 30*time.Second)
	// The generator drives the editor in a headless browser, through this server.
	port := *addr
	if i := strings.LastIndex(port, ":"); i >= 0 {
		port = port[i+1:]
	}
	gen, err := newGenerator(*inputDir, *generatedDir, findChrome(), "http://127.0.0.1:"+port)
	if err != nil {
		log.Fatal(err)
	}
	srv.gen = gen
	gen.pub = pub
	if gen.chrome == "" {
		log.Printf("generator: no headless browser found (install Chromium or set CHROME_BIN); reels won't be generated")
	}
	go gen.Run(context.Background())
	log.Printf("To-Primitive listening on %s (reels in %s, publishing from %s)", *addr, *reelsDir, *publishDir)
	log.Fatal(http.ListenAndServe(*addr, srv.routes()))
}

func newServer(dataDir, reelsDir string) *server {
	return &server{dataDir: dataDir, lib: &library{dir: reelsDir},
		sessions: map[string]*Session{}, videos: map[string]*Video{}, lives: map[string]*LiveSession{}}
}

func (srv *server) routes() http.Handler {
	if srv.pub != nil {
		srv.pub.editable = func(side map[string]any) bool {
			_, err := srv.queuedSource(side)
			return err == nil
		}
	}
	web, _ := fs.Sub(webFS, "web")
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(web))
	mux.HandleFunc("POST /api/sessions", srv.create)
	mux.HandleFunc("GET /api/sessions/{id}/events", srv.withSession(srv.events))
	mux.HandleFunc("PUT /api/sessions/{id}/params", srv.withSession(srv.params))
	mux.HandleFunc("GET /api/sessions/{id}/export", srv.withSession(srv.export))

	mux.HandleFunc("POST /api/videos", srv.createVideo)
	mux.HandleFunc("GET /api/videos/{id}/frame", srv.withVideo(srv.frame))
	mux.HandleFunc("POST /api/videos/{id}/frames", srv.withVideo(srv.frameSession))
	mux.HandleFunc("GET /api/videos/{id}/events", srv.withVideo(srv.videoEvents))
	mux.HandleFunc("POST /api/videos/{id}/compute", srv.withVideo(srv.compute))
	mux.HandleFunc("DELETE /api/videos/{id}/compute", srv.withVideo(srv.cancelCompute))
	mux.HandleFunc("GET /api/videos/{id}/keyframes", srv.withVideo(srv.keyframes))
	mux.HandleFunc("GET /api/videos/{id}/source", srv.withVideo(srv.source))
	mux.HandleFunc("POST /api/videos/{id}/mux", srv.withVideo(srv.mux))

	mux.HandleFunc("POST /api/live", srv.createLive)
	mux.HandleFunc("GET /api/live/{id}/events", srv.withLive(srv.liveEvents))
	mux.HandleFunc("POST /api/live/{id}/frame", srv.withLive(srv.liveFrame))
	mux.HandleFunc("PUT /api/live/{id}/params", srv.withLive(srv.liveParams))
	mux.HandleFunc("DELETE /api/live/{id}", srv.withLive(srv.closeLive))

	mux.HandleFunc("POST /api/reels/mux", srv.reelMux)
	mux.HandleFunc("POST /api/reels", srv.reelSave)
	mux.HandleFunc("GET /api/reels", srv.reelList)
	mux.HandleFunc("GET /api/reels/{id}/video", srv.reelVideo)
	mux.HandleFunc("DELETE /api/reels/{id}", srv.reelDelete)
	mux.HandleFunc("POST /api/reels/{id}/queue", srv.withPublisher(srv.reelQueue))
	mux.HandleFunc("GET /api/reels/{id}/spec", srv.reelSpec)
	mux.HandleFunc("GET /api/reels/{id}/source", srv.reelSource)
	mux.HandleFunc("GET /api/reels/files/{hash}/{name}", srv.reelFile)

	mux.HandleFunc("GET /api/publish", srv.withPublisher(srv.pubOverview))
	mux.HandleFunc("PUT /api/publish/settings", srv.withPublisher(srv.pubSettings))
	mux.HandleFunc("PUT /api/publish/order", srv.withPublisher(srv.pubOrder))
	mux.HandleFunc("PUT /api/publish/queue/{name}/meta", srv.withPublisher(srv.pubMeta))
	mux.HandleFunc("POST /api/publish/queue/{name}/skip", srv.withPublisher(srv.pubSkip))
	mux.HandleFunc("POST /api/publish/queue/{name}/now", srv.withPublisher(srv.pubNow))
	mux.HandleFunc("GET /api/publish/queue/{name}/video", srv.withPublisher(srv.pubVideo("")))
	mux.HandleFunc("POST /api/publish/published/{name}/requeue", srv.withPublisher(srv.pubRequeue))
	mux.HandleFunc("POST /api/publish/queue/{name}/duplicate", srv.withPublisher(srv.pubDuplicate))
	mux.HandleFunc("GET /api/publish/accounts", srv.withAccounts(srv.accountsOverview))
	mux.HandleFunc("GET /api/publish/accounts/{platform}/connect", srv.withAccounts(srv.accountConnect))
	mux.HandleFunc("POST /api/publish/accounts/{platform}/disconnect", srv.withAccounts(srv.accountDisconnect))
	mux.HandleFunc("GET /api/publish/oauth/{platform}/callback", srv.withAccounts(srv.accountCallback))
	mux.HandleFunc("POST /api/publish/queue/{name}/discard", srv.withPublisher(srv.pubAction((*publisher).Discard)))
	mux.HandleFunc("POST /api/publish/discarded/{name}/restore", srv.withPublisher(srv.pubAction((*publisher).Undiscard)))
	mux.HandleFunc("GET /api/publish/discarded/{name}/video", srv.withPublisher(srv.pubVideo(discardedDir)))
	mux.HandleFunc("GET /api/publish/queue/{name}/spec", srv.withPublisher(srv.pubSpec))
	mux.HandleFunc("GET /api/publish/queue/{name}/source", srv.withPublisher(srv.pubSource))
	mux.HandleFunc("GET /api/publish/copies", srv.withPublisher(srv.pubCopies))
	mux.HandleFunc("GET /api/publish/published/{name}/video", srv.withPublisher(srv.pubVideo(publishedDir)))

	mux.HandleFunc("GET /api/auto", srv.withGenerator(srv.genOverview))
	mux.HandleFunc("PUT /api/auto/settings", srv.withGenerator(srv.genSettings))
	mux.HandleFunc("POST /api/auto/inputs", srv.withGenerator(srv.genUpload))
	mux.HandleFunc("GET /api/auto/inputs/{name}", srv.withGenerator(srv.genInput))
	mux.HandleFunc("GET /api/auto/inputs/{name}/spec", srv.withGenerator(srv.genInputSpec))
	mux.HandleFunc("POST /api/auto/inputs/{name}/start", srv.withGenerator(srv.genStart))
	mux.HandleFunc("POST /api/auto/inputs/{name}/dismiss", srv.withGenerator(srv.genDismiss))
	mux.HandleFunc("GET /api/auto/jobs/{id}", srv.withGenerator(srv.genJob))
	mux.HandleFunc("GET /api/auto/jobs/{id}/source", srv.withGenerator(srv.genSource))
	mux.HandleFunc("POST /api/auto/jobs/{id}/progress", srv.withGenerator(srv.genProgress))
	mux.HandleFunc("POST /api/auto/jobs/{id}/fail", srv.withGenerator(srv.genFail))
	mux.HandleFunc("POST /api/auto/jobs/{id}/retry", srv.withGenerator(srv.genRetry))
	mux.HandleFunc("GET /api/auto/outputs/{name}/video", srv.withGenerator(srv.genVideo))
	mux.HandleFunc("POST /api/auto/outputs/{name}/queue", srv.withGenerator(srv.genToQueue))
	mux.HandleFunc("DELETE /api/auto/outputs/{name}", srv.withGenerator(srv.genDelete))

	mux.HandleFunc("GET /api/review", srv.withGenerator(srv.reviewOverview))
	mux.HandleFunc("POST /api/review/{name}/choose", srv.withGenerator(srv.reviewAction((*generator).Choose)))
	mux.HandleFunc("POST /api/review/{name}/discard", srv.withGenerator(srv.reviewAction((*generator).Discard)))
	mux.HandleFunc("POST /api/review/{name}/restore", srv.withGenerator(srv.reviewAction((*generator).Restore)))
	mux.HandleFunc("PUT /api/review/{name}/meta", srv.withGenerator(srv.reviewMeta))
	mux.HandleFunc("GET /api/review/{name}/spec", srv.withGenerator(srv.reviewSpec))
	mux.HandleFunc("GET /api/review/{name}/source", srv.withGenerator(srv.reviewSource))
	mux.HandleFunc("GET /api/review/discarded/{name}/video", srv.withGenerator(srv.reviewDiscardedVideo))
	mux.HandleFunc("POST /api/review/{name}/queue", srv.withGenerator(srv.reviewQueue))
	return mux
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func newID() string {
	var id [8]byte
	rand.Read(id[:])
	return hex.EncodeToString(id[:])
}

// ---------- image sessions ----------

func (srv *server) withSession(h func(http.ResponseWriter, *http.Request, *Session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		srv.mu.Lock()
		s := srv.sessions[r.PathValue("id")]
		srv.mu.Unlock()
		if s == nil {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		h(w, r, s)
	}
}

// addSession registers a new session; it starts with p (or the defaults).
func (srv *server) addSession(img image.Image, p *Params) *Session {
	params := defaultParams()
	if p != nil {
		params = *p
	}
	s := newSession(newID(), img, params)
	srv.mu.Lock()
	srv.sessions[s.ID] = s
	evictOldest(srv.sessions, maxSessions)
	srv.mu.Unlock()
	return s
}

func (srv *server) create(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	file, _, err := r.FormFile("image")
	if err != nil {
		http.Error(w, "missing image: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()
	img, _, err := image.Decode(file)
	if err != nil {
		http.Error(w, "unsupported image: "+err.Error(), http.StatusBadRequest)
		return
	}
	p, err := optionalParams(r.FormValue("params"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s := srv.addSession(img, p)
	b := img.Bounds()
	writeJSON(w, map[string]any{"id": s.ID, "width": b.Dx(), "height": b.Dy()})
}

func optionalParams(raw string) (*Params, error) {
	if raw == "" {
		return nil, nil
	}
	var p Params
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return nil, fmt.Errorf("bad params: %v", err)
	}
	return &p, nil
}

type closer interface{ Close() }

type created interface {
	closer
	createdAt() time.Time
}

func (s *Session) createdAt() time.Time { return s.Created }
func (v *Video) createdAt() time.Time   { return v.Created }

// evictOldest keeps only the newest n entries: this is a single-user local tool.
func evictOldest[T created](m map[string]T, n int) {
	if len(m) <= n {
		return
	}
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return m[ids[i]].createdAt().Before(m[ids[j]].createdAt()) })
	for _, id := range ids[:len(ids)-n] {
		m[id].Close()
		delete(m, id)
	}
}

func (srv *server) events(w http.ResponseWriter, r *http.Request, s *Session) {
	snapshot, ch := s.Subscribe()
	defer s.Unsubscribe(ch)
	serveSSE(w, r, snapshot, ch)
}

func (srv *server) params(w http.ResponseWriter, r *http.Request, s *Session) {
	var body struct {
		Params
		Restart bool `json:"restart"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad params: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.SetParams(body.Params, body.Restart)
	w.WriteHeader(http.StatusNoContent)
}

func (srv *server) export(w http.ResponseWriter, r *http.Request, s *Session) {
	q := r.URL.Query()
	format := q.Get("format")
	size := clampInt(atoiOr(q.Get("size"), 1024), 16, 8192)
	n := atoiOr(q.Get("n"), 0)
	iw, ih, bg, shapes := s.Shapes(n)

	filename := fmt.Sprintf("primitive-%d.%s", len(shapes), format)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	switch format {
	case "png":
		w.Header().Set("Content-Type", "image/png")
		ow, oh := fitSize(iw, ih, size)
		png.Encode(w, renderImage(iw, ih, ow, oh, bg, shapes))
	case "svg":
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Write([]byte(renderSVG(iw, ih, size, bg, shapes)))
	case "gif":
		w.Header().Set("Content-Type", "image/gif")
		if err := writeGIF(w, iw, ih, min(size, 1024), bg, shapes, 120); err != nil {
			log.Printf("gif export: %v", err)
		}
	default:
		w.Header().Del("Content-Disposition")
		http.Error(w, "format must be png, svg or gif", http.StatusBadRequest)
	}
}

// ---------- videos ----------

func (srv *server) withVideo(h func(http.ResponseWriter, *http.Request, *Video)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		srv.mu.Lock()
		v := srv.videos[r.PathValue("id")]
		srv.mu.Unlock()
		if v == nil {
			http.Error(w, "video not found", http.StatusNotFound)
			return
		}
		h(w, r, v)
	}
}

func (srv *server) createVideo(w http.ResponseWriter, r *http.Request) {
	mr, err := r.MultipartReader()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var part io.Reader
	var ext string
	for {
		p, err := mr.NextPart()
		if err != nil {
			http.Error(w, "missing video", http.StatusBadRequest)
			return
		}
		if p.FormName() == "video" {
			part = p
			ext = safeExt(p.FileName())
			break
		}
	}

	id := newID()
	dir := filepath.Join(srv.dataDir, id)
	path := filepath.Join(dir, "input"+ext)
	err = os.MkdirAll(dir, 0o755)
	if err == nil {
		err = saveFile(path, part)
	}
	if err != nil {
		os.RemoveAll(dir)
		http.Error(w, "saving video: "+err.Error(), http.StatusInternalServerError)
		return
	}
	info, err := probeVideo(r.Context(), path)
	if err != nil {
		os.RemoveAll(dir)
		http.Error(w, "unsupported video: "+err.Error(), http.StatusBadRequest)
		return
	}

	v := &Video{ID: id, Created: time.Now(), dir: dir, path: path, info: info}
	srv.mu.Lock()
	srv.videos[id] = v
	evictOldest(srv.videos, maxVideos)
	srv.mu.Unlock()
	writeJSON(w, map[string]any{"id": id, "info": info})
}

func saveFile(path string, r io.Reader) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// frame serves the raw frame at ?t= as a JPEG, for previews.
func (srv *server) frame(w http.ResponseWriter, r *http.Request, v *Video) {
	t, _ := strconv.ParseFloat(r.URL.Query().Get("t"), 64)
	size := clampInt(atoiOr(r.URL.Query().Get("size"), 320), 16, maxFrameSize)
	img, err := extractFrame(r.Context(), v.path, t, size)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "max-age=3600")
	jpeg.Encode(w, img, &jpeg.Options{Quality: 85})
}

// frameSession starts an editing session on the frame at t, so the
// parameters can be tuned on a still before rendering the video.
func (srv *server) frameSession(w http.ResponseWriter, r *http.Request, v *Video) {
	var body struct {
		T      float64 `json:"t"`
		Params *Params `json:"params"`
		Crop   *Crop   `json:"crop"` // optional framing, as in compute
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if !body.Crop.valid() {
		body.Crop = nil
	}
	img, err := extractCroppedFrame(r.Context(), v.path, body.T, body.Crop, maxFrameSize)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	s := srv.addSession(img, body.Params)
	b := img.Bounds()
	writeJSON(w, map[string]any{"id": s.ID, "width": b.Dx(), "height": b.Dy(), "t": body.T})
}

func (srv *server) videoEvents(w http.ResponseWriter, r *http.Request, v *Video) {
	snapshot, ch := v.Subscribe()
	defer v.Unsubscribe(ch)
	serveSSE(w, r, snapshot, ch)
}

func (srv *server) compute(w http.ResponseWriter, r *http.Request, v *Video) {
	var o ComputeOpts
	if err := json.NewDecoder(r.Body).Decode(&o); err != nil {
		http.Error(w, "bad options: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := v.StartCompute(o); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (srv *server) cancelCompute(w http.ResponseWriter, r *http.Request, v *Video) {
	v.CancelCompute()
	w.WriteHeader(http.StatusNoContent)
}

func (srv *server) keyframes(w http.ResponseWriter, r *http.Request, v *Video) {
	a := v.Animation()
	if a == nil {
		http.Error(w, "nothing computed yet", http.StatusNotFound)
		return
	}
	writeJSON(w, a)
}

// source serves the original upload: the player uses it for the audio track
// and as the clock that keeps the animation in sync with it.
func (srv *server) source(w http.ResponseWriter, r *http.Request, v *Video) {
	http.ServeFile(w, r, v.sourcePath())
}

// mux receives a video-only MP4 encoded in the browser and returns it with
// the original audio added.
func (srv *server) mux(w http.ResponseWriter, r *http.Request, v *Video) {
	in, out := v.tempPath("browser-"+newID()+".mp4"), v.tempPath("muxed-"+newID()+".mp4")
	defer os.Remove(in)
	defer os.Remove(out)
	if err := saveFile(in, r.Body); err != nil {
		http.Error(w, "receiving video: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := v.MuxAudio(r.Context(), in, out); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	http.ServeFile(w, r, out)
}

// safeExt keeps a short alphanumeric extension of an uploaded file name, so
// the saved file is served with the right content type.
func safeExt(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if len(ext) < 2 || len(ext) > 6 {
		return ""
	}
	for _, c := range ext[1:] {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return ""
		}
	}
	return ext
}

// ---------- live (webcam) ----------

func (srv *server) withLive(h func(http.ResponseWriter, *http.Request, *LiveSession)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		srv.mu.Lock()
		s := srv.lives[r.PathValue("id")]
		srv.mu.Unlock()
		if s == nil {
			http.Error(w, "live session not found", http.StatusNotFound)
			return
		}
		h(w, r, s)
	}
}

func (srv *server) createLive(w http.ResponseWriter, r *http.Request) {
	o := defaultLiveOpts()
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&o); err != nil {
			http.Error(w, "bad options: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	id := newID()
	s := newLiveSession(id, o, func() {
		srv.mu.Lock()
		delete(srv.lives, id)
		srv.mu.Unlock()
	})
	srv.mu.Lock()
	srv.lives[id] = s
	evictOldest(srv.lives, maxLives)
	srv.mu.Unlock()
	writeJSON(w, map[string]any{"id": id, "opts": s.opts})
}

func (srv *server) liveEvents(w http.ResponseWriter, r *http.Request, s *LiveSession) {
	snapshot, ch := s.Subscribe()
	defer s.Unsubscribe(ch)
	serveSSE(w, r, snapshot, ch)
}

// liveFrame receives one webcam frame; X-Capture-Time is the browser's clock
// when it was taken, echoed back with the keyframe built from it.
func (srv *server) liveFrame(w http.ResponseWriter, r *http.Request, s *LiveSession) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFrame)
	img, _, err := image.Decode(r.Body)
	if err != nil {
		http.Error(w, "bad frame: "+err.Error(), http.StatusBadRequest)
		return
	}
	captured, _ := strconv.ParseFloat(r.Header.Get("X-Capture-Time"), 64)
	s.PushFrame(img, captured)
	w.WriteHeader(http.StatusNoContent)
}

func (srv *server) liveParams(w http.ResponseWriter, r *http.Request, s *LiveSession) {
	var body struct {
		LiveOpts
		Restart bool `json:"restart"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad params: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.SetOpts(body.LiveOpts, body.Restart)
	w.WriteHeader(http.StatusNoContent)
}

func (srv *server) closeLive(w http.ResponseWriter, r *http.Request, s *LiveSession) {
	s.Close()
	w.WriteHeader(http.StatusNoContent)
}

func atoiOr(s string, def int) int {
	if v, err := strconv.Atoi(s); err == nil {
		return v
	}
	return def
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
