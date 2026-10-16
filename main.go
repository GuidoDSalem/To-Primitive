// Command to-primitive serves a web editor around fogleman/primitive: upload
// an image or a video, tweak the parameters and watch the shapes being added
// live, then render the whole video frame by frame.
package main

import (
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
	// Frames used to tune parameters are capped at this size: the working
	// resolution never goes above 1024 anyway.
	maxFrameSize = 1024
)

var numWorkers = runtime.NumCPU()

type server struct {
	dataDir string

	mu       sync.Mutex
	sessions map[string]*Session
	videos   map[string]*Video
}

func main() {
	addr := flag.String("addr", envOr("ADDR", ":8080"), "listen address")
	dataDir := flag.String("data", envOr("DATA_DIR", filepath.Join(os.TempDir(), "to-primitive")), "directory for uploaded videos and renders")
	flag.Parse()

	// Videos don't survive a restart (sessions live in memory), so start clean.
	os.RemoveAll(*dataDir)
	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		log.Fatal(err)
	}

	srv := &server{dataDir: *dataDir, sessions: map[string]*Session{}, videos: map[string]*Video{}}
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
	mux.HandleFunc("POST /api/videos/{id}/render", srv.withVideo(srv.render))
	mux.HandleFunc("DELETE /api/videos/{id}/render", srv.withVideo(srv.cancelRender))
	mux.HandleFunc("GET /api/videos/{id}/output", srv.withVideo(srv.output))

	log.Printf("To-Primitive listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
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
	for {
		p, err := mr.NextPart()
		if err != nil {
			http.Error(w, "missing video", http.StatusBadRequest)
			return
		}
		if p.FormName() == "video" {
			part = p
			break
		}
	}

	id := newID()
	dir := filepath.Join(srv.dataDir, id)
	path := filepath.Join(dir, "input")
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
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	img, err := extractFrame(r.Context(), v.path, body.T, maxFrameSize)
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

func (srv *server) render(w http.ResponseWriter, r *http.Request, v *Video) {
	var o RenderOpts
	if err := json.NewDecoder(r.Body).Decode(&o); err != nil {
		http.Error(w, "bad options: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := v.StartRender(o); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (srv *server) cancelRender(w http.ResponseWriter, r *http.Request, v *Video) {
	v.CancelRender()
	w.WriteHeader(http.StatusNoContent)
}

func (srv *server) output(w http.ResponseWriter, r *http.Request, v *Video) {
	if !v.Done() {
		http.Error(w, "no finished render", http.StatusNotFound)
		return
	}
	if r.URL.Query().Has("download") {
		w.Header().Set("Content-Disposition", `attachment; filename="primitive.mp4"`)
	}
	http.ServeFile(w, r, v.output())
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
