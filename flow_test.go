package main

import (
	"image"
	"image/color"
	"math"
	"sort"
	"testing"
)

// texturedFrame is a smooth random-ish texture shifted by (dx, dy), so block
// matching has something to lock on to everywhere.
func texturedFrame(w, h int, dx, dy float64) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			fx, fy := float64(x)-dx, float64(y)-dy
			v := 128 + 60*math.Sin(fx*0.21+fy*0.07) + 40*math.Cos(fy*0.19-fx*0.05) + 20*math.Sin((fx+fy)*0.43)
			img.Set(x, y, color.RGBA{uint8(v), uint8(255 - v), uint8(v / 2), 255})
		}
	}
	return img
}

// meanFlow is the median motion of the field: robust to the cells near the
// borders, where content enters the frame and has nothing to match.
func meanFlow(f *flowField) (float64, float64) {
	med := func(v []float64) float64 {
		s := append([]float64(nil), v...)
		sort.Float64s(s)
		return s[len(s)/2]
	}
	return med(f.vx), med(f.vy)
}

func TestFlowFindsShifts(t *testing.T) {
	for _, c := range []struct{ dx, dy float64 }{{0, 0}, {2, 0}, {0, -3}, {5, 4}, {-12, 6}, {1.5, -0.5}} {
		f := estimateFlow(texturedFrame(128, 96, 0, 0), texturedFrame(128, 96, c.dx, c.dy))
		mx, my := meanFlow(f)
		t.Logf("shift (%5.1f, %5.1f): estimated (%5.2f, %5.2f)", c.dx, c.dy, mx, my)
		if math.Abs(mx-c.dx) > 0.75 || math.Abs(my-c.dy) > 0.75 {
			t.Errorf("shift (%v, %v): estimated (%.2f, %.2f)", c.dx, c.dy, mx, my)
		}
	}
}

func TestFlowMovesShapes(t *testing.T) {
	f := estimateFlow(texturedFrame(128, 96, 0, 0), texturedFrame(128, 96, 6, -3))
	p := Params{Shapes: 20, Mode: 0, Alpha: 128}
	recs := frameShapes(testFrame(0), p, nil, 0)
	var dxs, dys []float64
	for _, r := range recs {
		moved := encodeShape(shapeRec{Shape: moveShape(r.Shape, f), Color: r.Color}, true, nil)
		x0, y0, _, _ := shapeBounds(r.Data)
		x1, y1, _, _ := shapeBounds(moved)
		dxs, dys = append(dxs, x1-x0), append(dys, y1-y0)
	}
	// Shapes against the edges are held in range, so look at the typical one.
	sort.Float64s(dxs)
	sort.Float64s(dys)
	if mx, my := dxs[len(dxs)/2], dys[len(dys)/2]; math.Abs(mx-6) > 1 || math.Abs(my+3) > 1 {
		t.Errorf("shapes typically moved by (%.1f, %.1f), want about (6, -3)", mx, my)
	}
}

// Shapes pushed far by the flow must stay within the ranges primitive's
// rasterisers rely on (a rectangle past the edge used to crash).
func TestFlowKeepsShapesInRange(t *testing.T) {
	f := estimateFlow(texturedFrame(96, 64, 0, 0), texturedFrame(96, 64, 0, 0))
	for k := range f.vx {
		f.vx[k], f.vy[k] = 400, -400 // absurdly far
	}
	for mode := 0; mode <= 8; mode++ {
		p := Params{Shapes: 10, Mode: mode, Alpha: 128}
		recs := frameShapes(testFrame(0), p, nil, 0)
		moved := followFlow(recs, f)
		// Building a frame rasterises every moved shape.
		frameShapes(testFrame(0), p, moved, 5)
	}
}
