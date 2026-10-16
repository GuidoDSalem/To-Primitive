package main

import (
	"fmt"
	"sync"

	"github.com/fogleman/primitive/primitive"
)

// recFromModel captures shape i of model as an immutable record.
func recFromModel(model *primitive.Model, i int) shapeRec {
	c := model.Colors[i]
	attrs := fmt.Sprintf(`fill="#%02x%02x%02x" fill-opacity="%.3f"`, c.R, c.G, c.B, float64(c.A)/255)
	return shapeRec{SVG: model.Shapes[i].SVG(attrs), Shape: model.Shapes[i], Color: c, Score: model.Scores[i]}
}

// rebind copies a shape and points it at another worker. Shapes keep a
// pointer to the worker that created them (for its size, RNG and scanline
// buffer), so a shape from a previous frame must be re-homed before it is
// mutated or rasterised against the new frame.
func rebind(s primitive.Shape, w *primitive.Worker) primitive.Shape {
	c := s.Copy()
	switch t := c.(type) {
	case *primitive.Triangle:
		t.Worker = w
	case *primitive.Rectangle:
		t.Worker = w
	case *primitive.RotatedRectangle:
		t.Worker = w
	case *primitive.Ellipse:
		t.Worker = w
	case *primitive.RotatedEllipse:
		t.Worker = w
	case *primitive.Quadratic:
		t.Worker = w
	case *primitive.Polygon:
		t.Worker = w
	default:
		panic(fmt.Sprintf("rebind: unknown shape %T", s))
	}
	return c
}

// buildFrame fills model (a fresh model of the current frame) with p.Shapes
// shapes. Without prev it runs primitive's normal search. With prev, each of
// the previous frame's shapes is carried over in order and nudged by `adapt`
// hill-climbing steps per worker, so shapes follow the motion instead of being
// re-drawn from scratch: that keeps the video from flickering and is much
// cheaper than a full search. A carried shape that no longer improves the
// frame is replaced, at the same index, by a freshly searched one.
//
// fresh[i] reports whether model.Shapes[i] is new rather than carried over
// from prev[i].
func buildFrame(model *primitive.Model, p Params, prev []shapeRec, adapt int) (fresh []bool) {
	for _, rec := range prev {
		if len(model.Shapes) >= p.Shapes {
			break
		}
		state := adaptShape(model, rec, p.Alpha, adapt)
		if state.Energy() < model.Score {
			model.Add(state.Shape, state.Alpha)
			fresh = append(fresh, false)
		} else {
			// Repeat 0: exactly one shape, keeping indices aligned with prev.
			model.Step(primitive.ShapeType(p.Mode), p.Alpha, 0)
			fresh = append(fresh, true)
		}
	}
	for len(model.Shapes) < p.Shapes {
		model.Step(primitive.ShapeType(p.Mode), p.Alpha, p.Repeat)
	}
	for len(fresh) < len(model.Shapes) {
		fresh = append(fresh, true)
	}
	return fresh
}

// adaptShape hill-climbs rec's shape against the model's current canvas on
// every worker in parallel and returns the best resulting state.
func adaptShape(model *primitive.Model, rec shapeRec, alpha, adapt int) *primitive.State {
	mutateAlpha := alpha == 0
	if mutateAlpha {
		alpha = clampInt(rec.Color.A, 1, 255)
	}
	workers := model.Workers
	if adapt == 0 {
		workers = workers[:1]
	}
	states := make([]*primitive.State, len(workers))
	var wg sync.WaitGroup
	for i, w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.Init(model.Current, model.Score)
			st := &primitive.State{Worker: w, Shape: rebind(rec.Shape, w), Alpha: alpha, MutateAlpha: mutateAlpha, Score: -1}
			best := st.Energy()
			for range adapt {
				undo := st.DoMove()
				if e := st.Energy(); e < best {
					best = e
				} else {
					st.UndoMove(undo)
				}
			}
			states[i] = st
		}()
	}
	wg.Wait()
	best := states[0]
	for _, st := range states[1:] {
		if st.Energy() < best.Energy() {
			best = st
		}
	}
	return best
}
