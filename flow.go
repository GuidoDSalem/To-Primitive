package main

import (
	"image"
	"math"
	"sort"

	"github.com/fogleman/primitive/primitive"
)

// flowField is a coarse optical-flow estimate: how far each cell of the
// previous frame moved to reach the current one, in pixels.
type flowField struct {
	cell   int
	gw, gh int
	vx, vy []float64
}

// gray is a luma plane.
type gray struct {
	w, h int
	p    []float64
}

func grayOf(img *image.RGBA) gray {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	g := gray{w, h, make([]float64, w*h)}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*img.Stride + 4*x
			g.p[y*w+x] = 0.299*float64(img.Pix[i]) + 0.587*float64(img.Pix[i+1]) + 0.114*float64(img.Pix[i+2])
		}
	}
	return g
}

// half is a 2x box downsample.
func (g gray) half() gray {
	w, h := max(1, g.w/2), max(1, g.h/2)
	out := gray{w, h, make([]float64, w*h)}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			x0, y0 := min(2*x, g.w-1), min(2*y, g.h-1)
			x1, y1 := min(x0+1, g.w-1), min(y0+1, g.h-1)
			out.p[y*w+x] = (g.p[y0*g.w+x0] + g.p[y0*g.w+x1] + g.p[y1*g.w+x0] + g.p[y1*g.w+x1]) / 4
		}
	}
	return out
}

func (g gray) at(x, y int) float64 {
	return g.p[clampInt(y, 0, g.h-1)*g.w+clampInt(x, 0, g.w-1)]
}

// sad is the mean absolute difference between the block of a at (x, y) and
// the block of b displaced by (dx, dy).
func sad(a, b gray, x, y, size, dx, dy int) float64 {
	var s float64
	for j := 0; j < size; j++ {
		for i := 0; i < size; i++ {
			s += math.Abs(a.at(x+i, y+j) - b.at(x+i+dx, y+j+dy))
		}
	}
	return s / float64(size*size)
}

const (
	flowCell   = 8 // grid cell, in pixels of the working image
	flowLevels = 3 // pyramid levels: catches motion up to ~4·2^(levels-1) px
)

// estimateFlow matches blocks of prev in cur, coarse to fine: the search
// starts on 1/4-size images (big motions), and each finer level refines the
// coarser estimate. The result is median-filtered, and cells without enough
// texture to tell (flat areas) take their neighbours' motion.
func estimateFlow(prev, cur *image.RGBA) *flowField {
	if prev == nil || cur == nil || prev.Bounds() != cur.Bounds() {
		return nil
	}
	pa, pb := []gray{grayOf(prev)}, []gray{grayOf(cur)}
	for l := 1; l < flowLevels; l++ {
		pa, pb = append(pa, pa[l-1].half()), append(pb, pb[l-1].half())
	}
	w, h := pa[0].w, pa[0].h
	f := &flowField{cell: flowCell, gw: (w + flowCell - 1) / flowCell, gh: (h + flowCell - 1) / flowCell}
	n := f.gw * f.gh
	f.vx, f.vy = make([]float64, n), make([]float64, n)
	textured := make([]bool, n)

	for l := flowLevels - 1; l >= 0; l-- {
		a, b := pa[l], pb[l]
		size := max(4, flowCell>>l)
		radius := 1
		if l == flowLevels-1 {
			radius = 4
		}
		scale := float64(int(1) << l)
		for cy := 0; cy < f.gh; cy++ {
			for cx := 0; cx < f.gw; cx++ {
				k := cy*f.gw + cx
				// Block at this level, centred on the cell.
				x := int(float64(cx*flowCell+flowCell/2)/scale) - size/2
				y := int(float64(cy*flowCell+flowCell/2)/scale) - size/2
				gx, gy := int(math.Round(f.vx[k]/scale)), int(math.Round(f.vy[k]/scale))
				best, bx, by := math.Inf(1), gx, gy
				for dy := -radius; dy <= radius; dy++ {
					for dx := -radius; dx <= radius; dx++ {
						if s := sad(a, b, x, y, size, gx+dx, gy+dy); s < best {
							best, bx, by = s, gx+dx, gy+dy
						}
					}
				}
				// Don't move without a clear reason (noise, flat areas).
				if s0 := sad(a, b, x, y, size, 0, 0); s0 <= best*1.05+0.5 {
					bx, by, best = 0, 0, s0
				}
				fx, fy := float64(bx), float64(by)
				if l == 0 {
					// Sub-pixel: fit a parabola through the neighbours.
					fx += parabola(sad(a, b, x, y, size, bx-1, by), best, sad(a, b, x, y, size, bx+1, by))
					fy += parabola(sad(a, b, x, y, size, bx, by-1), best, sad(a, b, x, y, size, bx, by+1))
					textured[k] = contrast(a, x, y, size) > 4
				}
				f.vx[k], f.vy[k] = fx*scale, fy*scale
			}
		}
	}
	f.fillFlat(textured)
	f.vx, f.vy = medianGrid(f.vx, f.gw, f.gh), medianGrid(f.vy, f.gw, f.gh)
	return f
}

func parabola(left, mid, right float64) float64 {
	d := left - 2*mid + right
	if d <= 0 {
		return 0
	}
	return clamp((left-right)/(2*d), -0.5, 0.5)
}

func clamp(x, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, x)) }

// contrast is the mean absolute deviation of a block: how much texture it has.
func contrast(g gray, x, y, size int) float64 {
	var sum float64
	for j := 0; j < size; j++ {
		for i := 0; i < size; i++ {
			sum += g.at(x+i, y+j)
		}
	}
	mean := sum / float64(size*size)
	var dev float64
	for j := 0; j < size; j++ {
		for i := 0; i < size; i++ {
			dev += math.Abs(g.at(x+i, y+j) - mean)
		}
	}
	return dev / float64(size*size)
}

// fillFlat gives untextured cells the average motion of textured neighbours.
func (f *flowField) fillFlat(textured []bool) {
	vx, vy := append([]float64(nil), f.vx...), append([]float64(nil), f.vy...)
	for cy := 0; cy < f.gh; cy++ {
		for cx := 0; cx < f.gw; cx++ {
			k := cy*f.gw + cx
			if textured[k] {
				continue
			}
			var sx, sy, n float64
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					x, y := cx+dx, cy+dy
					if x < 0 || y < 0 || x >= f.gw || y >= f.gh || !textured[y*f.gw+x] {
						continue
					}
					sx += f.vx[y*f.gw+x]
					sy += f.vy[y*f.gw+x]
					n++
				}
			}
			if n > 0 {
				vx[k], vy[k] = sx/n, sy/n
			} else {
				vx[k], vy[k] = 0, 0
			}
		}
	}
	f.vx, f.vy = vx, vy
}

func medianGrid(v []float64, w, h int) []float64 {
	out := make([]float64, len(v))
	buf := make([]float64, 0, 9)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			buf = buf[:0]
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					if xx, yy := x+dx, y+dy; xx >= 0 && yy >= 0 && xx < w && yy < h {
						buf = append(buf, v[yy*w+xx])
					}
				}
			}
			sort.Float64s(buf)
			out[y*w+x] = buf[len(buf)/2]
		}
	}
	return out
}

// at samples the flow at (x, y), bilinearly between cell centres.
func (f *flowField) at(x, y float64) (float64, float64) {
	gx := clamp(x/float64(f.cell)-0.5, 0, float64(f.gw-1))
	gy := clamp(y/float64(f.cell)-0.5, 0, float64(f.gh-1))
	x0, y0 := int(gx), int(gy)
	x1, y1 := min(x0+1, f.gw-1), min(y0+1, f.gh-1)
	tx, ty := gx-float64(x0), gy-float64(y0)
	lerp := func(v []float64) float64 {
		a := v[y0*f.gw+x0]*(1-tx) + v[y0*f.gw+x1]*tx
		b := v[y1*f.gw+x0]*(1-tx) + v[y1*f.gw+x1]*tx
		return a*(1-ty) + b*ty
	}
	return lerp(f.vx), lerp(f.vy)
}

// moveShape returns a copy of s with its points carried along by the flow:
// vertices move independently (so shapes can also stretch and turn), centred
// shapes move by the flow at their centre. Results are clamped to the same
// ranges primitive's mutations keep (rasterising relies on them: a rectangle
// outside the image would index past its buffers).
func moveShape(s primitive.Shape, f *flowField) primitive.Shape {
	c := s.Copy()
	const m = 16 // how far primitive lets vertices go outside the image
	move := func(x, y float64, lo, w, h float64) (float64, float64) {
		dx, dy := f.at(x, y)
		return clamp(x+dx, lo, w-1-lo), clamp(y+dy, lo, h-1-lo)
	}
	movei := func(x, y *int, lo float64, w, h int) {
		fx, fy := move(float64(*x), float64(*y), lo, float64(w), float64(h))
		*x, *y = int(math.Round(fx)), int(math.Round(fy))
	}
	movef := func(x, y *float64, lo float64, w, h int) {
		*x, *y = move(*x, *y, lo, float64(w), float64(h))
	}
	switch t := c.(type) {
	case *primitive.Triangle:
		w, h := t.Worker.W, t.Worker.H
		movei(&t.X1, &t.Y1, -m, w, h)
		movei(&t.X2, &t.Y2, -m, w, h)
		movei(&t.X3, &t.Y3, -m, w, h)
	case *primitive.Rectangle:
		w, h := t.Worker.W, t.Worker.H
		movei(&t.X1, &t.Y1, 0, w, h)
		movei(&t.X2, &t.Y2, 0, w, h)
	case *primitive.RotatedRectangle:
		movei(&t.X, &t.Y, 0, t.Worker.W, t.Worker.H)
	case *primitive.Ellipse:
		movei(&t.X, &t.Y, 0, t.Worker.W, t.Worker.H)
	case *primitive.RotatedEllipse:
		movef(&t.X, &t.Y, 0, t.Worker.W, t.Worker.H)
	case *primitive.Quadratic:
		w, h := t.Worker.W, t.Worker.H
		movef(&t.X1, &t.Y1, -m, w, h)
		movef(&t.X2, &t.Y2, -m, w, h)
		movef(&t.X3, &t.Y3, -m, w, h)
	case *primitive.Polygon:
		for i := range t.Order {
			movef(&t.X[i], &t.Y[i], -m, t.Worker.W, t.Worker.H)
		}
	}
	return c
}

// followFlow returns the shapes carried along by the flow, as the starting
// point for the next frame (nil flow: unchanged).
func followFlow(recs []shapeRec, f *flowField) []shapeRec {
	if f == nil {
		return recs
	}
	out := make([]shapeRec, len(recs))
	for i, r := range recs {
		r.Shape = moveShape(r.Shape, f)
		out[i] = r
	}
	return out
}

// stickFor is how strongly hill-climbing should keep flow-moved shapes in
// place (see adaptShapeAnchored). Measured on a panning clip
// (TestLiveFlowQuality): with 30 steps per shape, 1e-5 per px² at 128 px
// tracks the motion 3x better than no flow at the same image error; with few
// steps a weaker pull works best. Distances are in working-size pixels, so
// the weight is scaled with the working size.
func stickFor(adapt, size int) float64 {
	s := 1e-5 * math.Min(1, float64(adapt)/30)
	r := 128 / float64(max(size, 1))
	return s * r * r
}
