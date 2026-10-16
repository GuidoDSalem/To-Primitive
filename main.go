// Command to-primitive serves a web editor around fogleman/primitive: upload
// an image, tweak the parameters and watch the shapes being added live.
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
	_ "image/jpeg"
	"image/png"
	"io/fs"
	"log"
	"net/http"
	"os"
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
	maxSessions = 4
)

type server struct {
	mu       sync.Mutex
	sessions map[string]*Session
}

func main() {
	addr := flag.String("addr", envOr("ADDR", ":8080"), "listen address")
	flag.Parse()

	srv := &server{sessions: map[string]*Session{}}
	web, _ := fs.Sub(webFS, "web")

	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(web))
	mux.HandleFunc("POST /api/sessions", srv.create)
	mux.HandleFunc("GET /api/sessions/{id}/events", srv.withSession(srv.events))
	mux.HandleFunc("PUT /api/sessions/{id}/params", srv.withSession(srv.params))
	mux.HandleFunc("GET /api/sessions/{id}/export", srv.withSession(srv.export))

	log.Printf("To-Primitive listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

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

	var id [8]byte
	rand.Read(id[:])
	s := newSession(hex.EncodeToString(id[:]), img)

	srv.mu.Lock()
	srv.sessions[s.ID] = s
	srv.evictLocked()
	srv.mu.Unlock()

	b := img.Bounds()
	writeJSON(w, map[string]any{"id": s.ID, "width": b.Dx(), "height": b.Dy(), "params": defaultParams()})
}

// evictLocked keeps only the newest sessions: this is a single-user local tool.
func (srv *server) evictLocked() {
	if len(srv.sessions) <= maxSessions {
		return
	}
	all := make([]*Session, 0, len(srv.sessions))
	for _, s := range srv.sessions {
		all = append(all, s)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Created.Before(all[j].Created) })
	for _, s := range all[:len(all)-maxSessions] {
		s.Close()
		delete(srv.sessions, s.ID)
	}
}

func (srv *server) events(w http.ResponseWriter, r *http.Request, s *Session) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	snapshot, ch := s.Subscribe()
	defer s.Unsubscribe(ch)
	w.Write(snapshot)
	flusher.Flush()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			w.Write([]byte(": ping\n\n"))
			flusher.Flush()
		case msg, ok := <-ch:
			if !ok {
				return
			}
			w.Write(msg)
			// Drain whatever else is queued before flushing.
			for drained := false; !drained; {
				select {
				case more, ok := <-ch:
					if !ok {
						flusher.Flush()
						return
					}
					w.Write(more)
				default:
					drained = true
				}
			}
			flusher.Flush()
		}
	}
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
		png.Encode(w, renderPNG(iw, ih, size, bg, shapes))
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
