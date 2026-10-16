package main

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"runtime"
	"sync"
	"time"

	"github.com/fogleman/primitive/primitive"
	"github.com/nfnt/resize"
)

// Params are the user-tweakable settings. Everything except Shapes and Paused
// changes the optimisation itself, so changing it restarts the run; Shapes and
// Paused only move the target of the current run.
type Params struct {
	Shapes     int    `json:"shapes"`
	Mode       int    `json:"mode"`
	Alpha      int    `json:"alpha"`
	Repeat     int    `json:"repeat"`
	InputSize  int    `json:"inputSize"`
	Background string `json:"background"` // "" = average colour of the image
	Paused     bool   `json:"paused"`
}

func defaultParams() Params {
	return Params{Shapes: 200, Mode: 1, Alpha: 128, Repeat: 0, InputSize: 256}
}

func (p *Params) clamp() {
	p.Shapes = clampInt(p.Shapes, 1, 10000)
	p.Mode = clampInt(p.Mode, 0, 8)
	p.Alpha = clampInt(p.Alpha, 0, 255)
	p.Repeat = clampInt(p.Repeat, 0, 20)
	p.InputSize = clampInt(p.InputSize, 32, 1024)
}

func (p Params) sameRun(q Params) bool {
	return p.Mode == q.Mode && p.Alpha == q.Alpha && p.Repeat == q.Repeat &&
		p.InputSize == q.InputSize && p.Background == q.Background
}

func clampInt(x, lo, hi int) int {
	return max(lo, min(hi, x))
}

// shapeRec is one shape added by the model. Records are immutable once
// appended, so readers may keep a sub-slice after releasing the lock.
type shapeRec struct {
	SVG   string
	Shape primitive.Shape
	Color primitive.Color
	Score float64
}

// run is one optimisation of the session's image with fixed Params.
type run struct {
	gen    int
	w, h   int
	bg     primitive.Color
	shapes []shapeRec
	score  float64
	cancel context.CancelFunc
	wake   chan struct{}
}

type Session struct {
	ID      string
	Created time.Time
	src     image.Image

	// stepMu serialises model.Step across runs: a cancelled run can't be
	// interrupted mid-step, so this stops it competing with its replacement.
	stepMu sync.Mutex

	mu      sync.Mutex
	params  Params
	gen     int
	run     *run
	running bool
	subs    map[chan []byte]struct{}
}

func newSession(id string, src image.Image) *Session {
	s := &Session{ID: id, Created: time.Now(), src: src, params: defaultParams(), subs: map[chan []byte]struct{}{}}
	s.mu.Lock()
	s.startRunLocked()
	s.mu.Unlock()
	return s
}

// SetParams applies new params, restarting the run when needed.
func (s *Session) SetParams(p Params, restart bool) {
	p.clamp()
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.params
	s.params = p
	if restart || !old.sameRun(p) {
		s.startRunLocked()
		return
	}
	select {
	case s.run.wake <- struct{}{}:
	default:
	}
	s.broadcastStatusLocked()
}

func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run != nil {
		s.run.cancel()
	}
	for ch := range s.subs {
		close(ch)
		delete(s.subs, ch)
	}
}

func (s *Session) startRunLocked() {
	if s.run != nil {
		s.run.cancel()
	}
	p := s.params
	size := uint(p.InputSize)
	img := resize.Thumbnail(size, size, s.src, resize.Bilinear)
	var bg primitive.Color
	if p.Background == "" {
		bg = primitive.MakeColor(primitive.AverageImageColor(img))
	} else {
		bg = primitive.MakeHexColor(p.Background)
	}
	b := img.Bounds()
	// Render the model's own context at working size: we never read it, and
	// exports re-render the shapes at the requested size.
	model := primitive.NewModel(img, bg, max(b.Dx(), b.Dy()), runtime.NumCPU())

	ctx, cancel := context.WithCancel(context.Background())
	s.gen++
	r := &run{gen: s.gen, w: b.Dx(), h: b.Dy(), bg: bg, score: model.Score, cancel: cancel, wake: make(chan struct{}, 1)}
	s.run = r
	s.running = false
	s.broadcastLocked("snapshot", s.snapshotLocked())
	go s.loop(ctx, r, model, p)
}

func (s *Session) targetLocked() int {
	if s.params.Paused {
		return 0
	}
	return s.params.Shapes
}

func (s *Session) loop(ctx context.Context, r *run, model *primitive.Model, p Params) {
	for {
		s.mu.Lock()
		if ctx.Err() != nil {
			s.mu.Unlock()
			return
		}
		idle := len(r.shapes) >= s.targetLocked()
		if s.running == idle {
			s.running = !idle
			s.broadcastStatusLocked()
		}
		s.mu.Unlock()

		if idle {
			select {
			case <-r.wake:
			case <-ctx.Done():
				return
			}
			continue
		}

		s.stepMu.Lock()
		if ctx.Err() != nil {
			s.stepMu.Unlock()
			return
		}
		before := len(model.Shapes)
		model.Step(primitive.ShapeType(p.Mode), p.Alpha, p.Repeat)
		s.stepMu.Unlock()

		s.mu.Lock()
		if ctx.Err() != nil {
			s.mu.Unlock()
			return
		}
		for i := before; i < len(model.Shapes); i++ {
			c := model.Colors[i]
			attrs := fmt.Sprintf(`fill="#%02x%02x%02x" fill-opacity="%.3f"`, c.R, c.G, c.B, float64(c.A)/255)
			rec := shapeRec{SVG: model.Shapes[i].SVG(attrs), Shape: model.Shapes[i], Color: c, Score: model.Scores[i]}
			r.shapes = append(r.shapes, rec)
			s.broadcastLocked("shape", map[string]any{"g": r.gen, "i": len(r.shapes) - 1, "svg": rec.SVG, "score": rec.Score})
		}
		r.score = model.Score
		s.mu.Unlock()
	}
}

func (s *Session) snapshotLocked() map[string]any {
	r := s.run
	svgs := make([]string, len(r.shapes))
	for i, rec := range r.shapes {
		svgs[i] = rec.SVG
	}
	return map[string]any{
		"g": r.gen, "width": r.w, "height": r.h,
		"background": fmt.Sprintf("#%02x%02x%02x", r.bg.R, r.bg.G, r.bg.B),
		"params":     s.params, "shapes": svgs, "score": r.score, "running": s.running,
	}
}

func (s *Session) broadcastStatusLocked() {
	s.broadcastLocked("status", map[string]any{
		"g": s.run.gen, "count": len(s.run.shapes), "score": s.run.score, "running": s.running, "params": s.params,
	})
}

func sseMessage(event string, data any) []byte {
	b, _ := json.Marshal(data)
	return fmt.Appendf(nil, "event: %s\ndata: %s\n\n", event, b)
}

func (s *Session) broadcastLocked(event string, data any) {
	if len(s.subs) == 0 {
		return
	}
	msg := sseMessage(event, data)
	for ch := range s.subs {
		select {
		case ch <- msg:
		default:
			// Subscriber can't keep up: drop it. The browser's EventSource
			// reconnects and gets a fresh snapshot.
			close(ch)
			delete(s.subs, ch)
		}
	}
}

// Subscribe returns the current snapshot plus a channel receiving every
// event after it. The channel is closed if the subscriber is dropped.
func (s *Session) Subscribe() ([]byte, chan []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := make(chan []byte, 4096)
	s.subs[ch] = struct{}{}
	return sseMessage("snapshot", s.snapshotLocked()), ch
}

func (s *Session) Unsubscribe(ch chan []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.subs, ch)
}

// Shapes returns the first n shapes of the current run (all if n <= 0).
func (s *Session) Shapes(n int) (w, h int, bg primitive.Color, shapes []shapeRec) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.run
	shapes = r.shapes
	if n > 0 && n < len(shapes) {
		shapes = shapes[:n]
	}
	return r.w, r.h, r.bg, shapes
}
