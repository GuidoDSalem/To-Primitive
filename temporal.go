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
	rec := shapeRec{SVG: model.Shapes[i].SVG(attrs), Shape: model.Shapes[i], Color: c, Score: model.Scores[i]}
	rec.Data = encodeShape(rec, true, nil)
	return rec
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
	return buildFrameStick(model, p, prev, adapt, 0)
}

// buildFrameStick is buildFrame with the adapted shapes kept near their
// starting place (see adaptShapeAnchored), for shapes moved by the flow.
func buildFrameStick(model *primitive.Model, p Params, prev []shapeRec, adapt int, stick float64) (fresh []bool) {
	for _, rec := range prev {
		if len(model.Shapes) >= p.Shapes {
			break
		}
		state := adaptShapeAnchored(model, rec, p.Alpha, adapt, stick)
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
	return adaptShapeAnchored(model, rec, alpha, adapt, 0)
}

// adaptShapeAnchored is adaptShape where moving the shape's centre away from
// where it started costs stick·distance² on top of the image error, so the
// search refines the shape without dragging it off a position it was put in
// on purpose (by the optical flow). stick 0 is plain adaptShape.
func adaptShapeAnchored(model *primitive.Model, rec shapeRec, alpha, adapt int, stick float64) *primitive.State {
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
			ax, ay := shapeCenter(st.Shape)
			cost := func() float64 {
				e := st.Energy()
				if stick > 0 {
					x, y := shapeCenter(st.Shape)
					e += stick * ((x-ax)*(x-ax) + (y-ay)*(y-ay))
				}
				return e
			}
			best := cost()
			for range adapt {
				undo := st.DoMove()
				if e := cost(); e < best {
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

// shapeCenter is the centre of a shape (vertex average for polygons).
func shapeCenter(s primitive.Shape) (float64, float64) {
	switch t := s.(type) {
	case *primitive.Triangle:
		return float64(t.X1+t.X2+t.X3) / 3, float64(t.Y1+t.Y2+t.Y3) / 3
	case *primitive.Rectangle:
		return float64(t.X1+t.X2) / 2, float64(t.Y1+t.Y2) / 2
	case *primitive.RotatedRectangle:
		return float64(t.X), float64(t.Y)
	case *primitive.Ellipse:
		return float64(t.X), float64(t.Y)
	case *primitive.RotatedEllipse:
		return t.X, t.Y
	case *primitive.Quadratic:
		return (t.X1 + t.X2 + t.X3) / 3, (t.Y1 + t.Y2 + t.Y3) / 3
	case *primitive.Polygon:
		var x, y float64
		for i := range t.Order {
			x += t.X[i]
			y += t.Y[i]
		}
		return x / float64(t.Order), y / float64(t.Order)
	}
	return 0, 0
}
