package main

import (
	"context"
	"fmt"
	"image"
	"image/draw"
	"sync"
	"time"

	"github.com/fogleman/primitive/primitive"
)

// LiveOpts are the live settings, on top of the shape Params.
type LiveOpts struct {
	Params
	Adapt    int `json:"adapt"`  // hill-climb steps per adapted shape
	BudgetMS int `json:"budget"` // time allowed per keyframe
}

func defaultLiveOpts() LiveOpts {
	p := defaultParams()
	p.Shapes, p.InputSize = 100, 128
	return LiveOpts{Params: p, Adapt: 30, BudgetMS: 120}
}

func (o *LiveOpts) clamp() {
	o.Params.clamp()
	o.Adapt = clampInt(o.Adapt, 0, 500)
	o.BudgetMS = clampInt(o.BudgetMS, 20, 2000)
}

type liveFrame struct {
	img      *image.RGBA
	captured float64 // the browser's clock (ms) when the frame was taken
	received time.Time
}

// LiveSession turns frames pushed by the browser into keyframes, one at a
// time: when idle it asks for a frame ("ready"), so it always works on the
// freshest one and never builds up a backlog.
type LiveSession struct {
	ID      string
	Created time.Time

	mu      sync.Mutex
	opts    LiveOpts
	reset   bool // start over on the next frame (params changed, or asked to)
	pending *liveFrame
	wake    chan struct{}
	hub     hub
	cancel  context.CancelFunc
	onClose func()
}

func newLiveSession(id string, o LiveOpts, onClose func()) *LiveSession {
	o.clamp()
	ctx, cancel := context.WithCancel(context.Background())
	s := &LiveSession{ID: id, Created: time.Now(), opts: o, reset: true, wake: make(chan struct{}, 1), cancel: cancel, onClose: onClose}
	go s.loop(ctx)
	return s
}

func (s *LiveSession) createdAt() time.Time { return s.Created }

func (s *LiveSession) Close() {
	s.cancel()
	s.mu.Lock()
	s.hub.closeAll()
	s.mu.Unlock()
}

func (s *LiveSession) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// PushFrame stores the newest frame, replacing any not yet used.
func (s *LiveSession) PushFrame(img image.Image, captured float64) {
	b := img.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(rgba, rgba.Bounds(), img, b.Min, draw.Src)
	s.mu.Lock()
	s.pending = &liveFrame{img: rgba, captured: captured, received: time.Now()}
	s.mu.Unlock()
	s.signal()
}

func (s *LiveSession) SetOpts(o LiveOpts, restart bool) {
	o.clamp()
	s.mu.Lock()
	if restart || !s.opts.Params.sameRun(o.Params) {
		s.reset = true
	}
	s.opts = o
	s.mu.Unlock()
	s.signal()
}

func (s *LiveSession) Subscribe() ([]byte, chan []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := s.hub.add()
	return sseMessage("ready", map[string]any{}), ch
}

func (s *LiveSession) Unsubscribe(ch chan []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hub.remove(ch)
}

func (s *LiveSession) broadcast(event string, data any) {
	s.mu.Lock()
	s.hub.broadcast(event, data)
	s.mu.Unlock()
}

// idleTimeout closes a session nobody is watching (the tab was closed).
const idleTimeout = 10 * time.Second

func (s *LiveSession) loop(ctx context.Context) {
	defer func() {
		if s.onClose != nil {
			s.onClose()
		}
	}()

	var (
		bg       primitive.Color
		prev     []shapeRec
		prevEnc  [][]float64
		prevImg  *image.RGBA
		bs       budgetState
		seq      int
		lastSeen = time.Now()
		times    []time.Time // keyframe times in the last second, for the rate
		avgMS    float64
	)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()

	for {
		// Ask for a frame (again every second, in case a request was lost).
		s.mu.Lock()
		paused := s.opts.Paused
		if len(s.hub.subs) > 0 {
			lastSeen = time.Now()
		}
		s.mu.Unlock()
		if time.Since(lastSeen) > idleTimeout {
			return
		}
		if !paused {
			s.broadcast("ready", map[string]any{})
		}

		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-s.wake:
		}
		s.mu.Lock()
		f := s.pending
		s.pending = nil
		if s.opts.Paused {
			f = nil // keep showing the last keyframe
		}
		s.mu.Unlock()
		if f == nil {
			continue // nothing new yet: ask again and check idleness
		}

		s.mu.Lock()
		o := s.opts
		reset := s.reset
		s.reset = false
		s.mu.Unlock()

		start := time.Now()
		deadline := start.Add(time.Duration(o.BudgetMS) * time.Millisecond)
		img := f.img
		w, h := img.Bounds().Dx(), img.Bounds().Dy()
		if prevImg == nil || prevImg.Bounds() != img.Bounds() {
			reset = true
		}
		if reset {
			prev, prevEnc, prevImg, bs = nil, nil, nil, budgetState{}
			if o.Background == "" {
				bg = primitive.MakeColor(primitive.AverageImageColor(img))
			} else {
				bg = primitive.MakeHexColor(o.Background)
			}
			s.broadcast("animation", map[string]any{
				"width": w, "height": h, "background": fmt.Sprintf("#%02x%02x%02x", bg.R, bg.G, bg.B),
			})
		}

		model := primitive.NewModel(img, bg, max(w, h), numWorkers)
		fresh, st := buildFrameBudget(model, o.Params, prev, o.Adapt, newChangeMap(prevImg, img), deadline, &bs)
		cur := make([]shapeRec, len(model.Shapes))
		enc := make([][]float64, len(model.Shapes))
		for i := range model.Shapes {
			cur[i] = recFromModel(model, i)
			var p []float64
			if !fresh[i] && i < len(prevEnc) {
				p = prevEnc[i]
			}
			enc[i] = encodeShape(cur[i], fresh[i], p)
		}
		prev, prevEnc, prevImg = cur, enc, img

		took := time.Since(start)
		now := time.Now()
		times = append(times, now)
		for len(times) > 0 && now.Sub(times[0]) > time.Second {
			times = times[1:]
		}
		ms := float64(took.Microseconds()) / 1000
		if avgMS == 0 {
			avgMS = ms
		} else {
			avgMS = 0.8*avgMS + 0.2*ms
		}
		seq++
		s.broadcast("keyframe", map[string]any{"seq": seq, "t": f.captured, "reset": reset, "shapes": enc})
		s.broadcast("stats", map[string]any{
			"ms": round2(ms), "avgMs": round2(avgMS), "kfps": len(times), "shapes": len(enc),
			"adapted": st.Adapted, "recolored": st.Recolored, "replaced": st.Replaced, "added": st.Added,
			"waitMs":  round2(float64(start.Sub(f.received).Microseconds()) / 1000),
			"adaptMs": round2(float64(bs.adaptCost.Microseconds()) / 1000),
			"stepMs":  round2(float64(bs.stepCost.Microseconds()) / 1000),
		})
	}
}
