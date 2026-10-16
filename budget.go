package main

import (
	"image"
	"math"
	"sort"
	"time"

	"github.com/fogleman/primitive/primitive"
)

// changeMap answers "how much did this rectangle change between two frames"
// in constant time, from a summed-area table of per-pixel differences.
type changeMap struct {
	w, h int
	sat  []float64 // (w+1) x (h+1)
}

// newChangeMap compares two frames of the same size (nil if they differ).
func newChangeMap(a, b *image.RGBA) *changeMap {
	if a == nil || b == nil || a.Bounds() != b.Bounds() {
		return nil
	}
	w, h := a.Bounds().Dx(), a.Bounds().Dy()
	c := &changeMap{w: w, h: h, sat: make([]float64, (w+1)*(h+1))}
	for y := 0; y < h; y++ {
		row := 0.0
		for x := 0; x < w; x++ {
			i := y*a.Stride + 4*x
			d := absDiff(a.Pix[i], b.Pix[i]) + absDiff(a.Pix[i+1], b.Pix[i+1]) + absDiff(a.Pix[i+2], b.Pix[i+2])
			row += float64(d)
			c.sat[(y+1)*(w+1)+x+1] = c.sat[y*(w+1)+x+1] + row
		}
	}
	return c
}

func absDiff(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

// mean is the average difference (0..765) over the rectangle, clipped to the frame.
func (c *changeMap) mean(x0, y0, x1, y1 float64) float64 {
	ix0, iy0 := clampInt(int(math.Floor(x0)), 0, c.w), clampInt(int(math.Floor(y0)), 0, c.h)
	ix1, iy1 := clampInt(int(math.Ceil(x1)), 0, c.w), clampInt(int(math.Ceil(y1)), 0, c.h)
	if ix1 <= ix0 || iy1 <= iy0 {
		return 0
	}
	W := c.w + 1
	sum := c.sat[iy1*W+ix1] - c.sat[iy0*W+ix1] - c.sat[iy1*W+ix0] + c.sat[iy0*W+ix0]
	return sum / float64((ix1-ix0)*(iy1-iy0))
}

// shapeBounds is a bounding box of an encoded shape (see encode.go).
func shapeBounds(s []float64) (x0, y0, x1, y1 float64) {
	p := s[shapeHeader:]
	switch int(s[0]) {
	case kindRect, kindEllipse:
		// Conservative: the circle that contains it at any rotation.
		r := math.Hypot(p[2], p[3])
		if int(s[0]) == kindRect {
			r /= 2
		}
		return p[0] - r, p[1] - r, p[0] + r, p[1] + r
	case kindQuad:
		x0, y0, x1, y1 = math.Min(p[0], math.Min(p[2], p[4])), math.Min(p[1], math.Min(p[3], p[5])),
			math.Max(p[0], math.Max(p[2], p[4])), math.Max(p[1], math.Max(p[3], p[5]))
		return x0 - p[6]/2, y0 - p[6]/2, x1 + p[6]/2, y1 + p[6]/2
	}
	x0, y0, x1, y1 = math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for j := 0; j+1 < len(p); j += 2 {
		x0, x1 = math.Min(x0, p[j]), math.Max(x1, p[j])
		y0, y1 = math.Min(y0, p[j+1]), math.Max(y1, p[j+1])
	}
	return
}

type budgetStats struct {
	Adapted   int `json:"adapted"`   // carried over and hill-climbed
	Recolored int `json:"recolored"` // carried over, only the colour recomputed
	Replaced  int `json:"replaced"`  // carried over but no longer useful: searched anew
	Added     int `json:"added"`     // new shapes towards the target count
}

// budgetState carries measurements and progress from one live keyframe to
// the next.
type budgetState struct {
	adaptCost time.Duration // time per adapted shape (moving average)
	stepCost  time.Duration // time per fastStep (moving average)
	cursor    int           // round-robin position for refining still regions
	stick     float64       // keep adapted shapes near where they start (see adaptShapeAnchored)
}

func average(avg *time.Duration, sample time.Duration) {
	if *avg == 0 {
		*avg = sample
	} else {
		*avg = (*avg*3 + sample) / 4
	}
}

// minChange is the mean per-pixel difference (out of 765) below which a
// region counts as unchanged.
const minChange = 6

// fastStep adds one shape like model.Step, with a much smaller search
// (primitive's is ~100 ms per shape): each worker hill-climbs from the best
// of a few random candidates and the best result wins.
func fastStep(model *primitive.Model, mode, alpha int) {
	ch := make(chan *primitive.State, len(model.Workers))
	for _, w := range model.Workers {
		w.Init(model.Current, model.Score)
		go func() { ch <- w.BestHillClimbState(primitive.ShapeType(mode), alpha, 60, 40, 1) }()
	}
	var best *primitive.State
	for range model.Workers {
		st := <-ch
		if best == nil || st.Energy() < best.Energy() {
			best = st
		}
	}
	model.Add(best.Shape, best.Alpha)
}

// buildFrameBudget is buildFrame for live video: it must finish by deadline.
// Carried-over shapes are kept in order (indices stay aligned with prev);
// those over the regions that changed most get `adapt` hill-climbing steps,
// as many as fit in the time left, and spare capacity refines the others in
// turns; the rest only get their colour recomputed, which is cheap. Time
// left at the end adds shapes up to p.Shapes, so a stream starts with a few
// shapes and fills in.
func buildFrameBudget(model *primitive.Model, p Params, prev []shapeRec, adapt int, change *changeMap,
	deadline time.Time, bs *budgetState) (fresh []bool, st budgetStats) {
	n := min(len(prev), p.Shapes)

	// Which shapes to adapt: the most changed ones, then others in turns,
	// as many as the budget allows (keeping some for recolouring).
	pick := make([]bool, n)
	if adapt > 0 && n > 0 {
		per := max(bs.adaptCost, 50*time.Microsecond)
		k := min(n, int(float64(time.Until(deadline))*0.6/float64(per)))
		if change != nil {
			scores := make([]float64, n)
			idx := make([]int, 0, n)
			for i := range n {
				scores[i] = change.mean(shapeBounds(prev[i].Data))
				if scores[i] >= minChange {
					idx = append(idx, i)
				}
			}
			sort.Slice(idx, func(a, b int) bool { return scores[idx[a]] > scores[idx[b]] })
			for _, i := range idx[:min(k, len(idx))] {
				pick[i] = true
				k--
			}
		}
		for ; k > 0; k-- {
			bs.cursor = (bs.cursor + 1) % n
			if pick[bs.cursor] {
				break // went all the way round
			}
			pick[bs.cursor] = true
		}
	}

	var adaptTime time.Duration
	for i := range n {
		rec := prev[i]
		var state *primitive.State
		if pick[i] && time.Now().Before(deadline) {
			t0 := time.Now()
			state = adaptShapeAnchored(model, rec, p.Alpha, adapt, bs.stick)
			adaptTime += time.Since(t0)
			st.Adapted++
		} else {
			state = adaptShape(model, rec, p.Alpha, 0) // just the energy with a fresh colour
			st.Recolored++
		}
		if state.Energy() < model.Score || time.Until(deadline) < bs.stepCost {
			// No time to replace it: keep it even if it no longer helps, a
			// later keyframe can replace it.
			model.Add(state.Shape, state.Alpha)
			fresh = append(fresh, false)
		} else {
			t0 := time.Now()
			fastStep(model, p.Mode, p.Alpha)
			average(&bs.stepCost, time.Since(t0))
			fresh = append(fresh, true)
			st.Replaced++
		}
	}
	if st.Adapted > 0 {
		average(&bs.adaptCost, adaptTime/time.Duration(st.Adapted))
	}

	for len(model.Shapes) < p.Shapes && time.Until(deadline) > bs.stepCost {
		t0 := time.Now()
		fastStep(model, p.Mode, p.Alpha)
		average(&bs.stepCost, time.Since(t0))
		st.Added++
	}
	for len(fresh) < len(model.Shapes) {
		fresh = append(fresh, true)
	}
	return fresh, st
}
