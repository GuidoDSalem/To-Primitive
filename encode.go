package main

import (
	"fmt"
	"math"

	"github.com/fogleman/primitive/primitive"
)

// Shapes are sent to the browser as flat number arrays so it can interpolate
// them between keyframes and draw them at any resolution:
//
//	[kind, r, g, b, a, fresh, params...]
//
// fresh is 1 when the shape was not carried over from the previous keyframe
// (the browser cross-fades instead of morphing). Params are in working-size
// pixels, before primitive's half-pixel offset:
//
//	kindPoly    x1, y1, x2, y2, ...      triangles, polygons
//	kindRect    cx, cy, w, h, angle      rectangles (rotated or not), degrees
//	kindEllipse cx, cy, rx, ry, angle    ellipses and circles, degrees
//	kindQuad    x1, y1, cx, cy, x2, y2, width   quadratic Bézier strokes
const (
	kindPoly = iota
	kindRect
	kindEllipse
	kindQuad
)

const shapeHeader = 6 // kind, r, g, b, a, fresh

func round2(x float64) float64 { return math.Round(x*100) / 100 }

// encodeShape flattens rec. prev is the same shape's encoding in the previous
// keyframe (nil if fresh); it is used to unwrap angles so interpolation turns
// the short way round.
func encodeShape(rec shapeRec, fresh bool, prev []float64) []float64 {
	var kind int
	var p []float64
	switch s := rec.Shape.(type) {
	case *primitive.Triangle:
		kind, p = kindPoly, []float64{float64(s.X1), float64(s.Y1), float64(s.X2), float64(s.Y2), float64(s.X3), float64(s.Y3)}
	case *primitive.Polygon:
		kind = kindPoly
		for i := range s.Order {
			p = append(p, s.X[i], s.Y[i])
		}
	case *primitive.Rectangle:
		x1, x2 := min(s.X1, s.X2), max(s.X1, s.X2)
		y1, y2 := min(s.Y1, s.Y2), max(s.Y1, s.Y2)
		w, h := float64(x2-x1+1), float64(y2-y1+1)
		kind, p = kindRect, []float64{float64(x1) + w/2, float64(y1) + h/2, w, h, 0}
	case *primitive.RotatedRectangle:
		kind, p = kindRect, []float64{float64(s.X), float64(s.Y), float64(s.Sx), float64(s.Sy), float64(s.Angle)}
	case *primitive.Ellipse:
		kind, p = kindEllipse, []float64{float64(s.X), float64(s.Y), float64(s.Rx), float64(s.Ry), 0}
	case *primitive.RotatedEllipse:
		kind, p = kindEllipse, []float64{s.X, s.Y, s.Rx, s.Ry, s.Angle}
	case *primitive.Quadratic:
		kind, p = kindQuad, []float64{s.X1, s.Y1, s.X2, s.Y2, s.X3, s.Y3, s.Width}
	default:
		panic(fmt.Sprintf("encodeShape: unknown shape %T", rec.Shape))
	}

	if (kind == kindRect || kind == kindEllipse) && !fresh && len(prev) == shapeHeader+5 && int(prev[0]) == kind {
		// Both shapes look the same rotated by 180°: pick the equivalent
		// angle closest to the previous one.
		a, pa := p[4], prev[shapeHeader+4]
		p[4] = a + 180*math.Round((pa-a)/180)
	}

	c := rec.Color
	out := make([]float64, 0, shapeHeader+len(p))
	out = append(out, float64(kind), float64(c.R), float64(c.G), float64(c.B), float64(c.A), 0)
	if fresh {
		out[5] = 1
	}
	for _, x := range p {
		out = append(out, round2(x))
	}
	return out
}
