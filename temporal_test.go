package main

import (
	"image"
	"image/color"
	"image/draw"
	"math"
	"testing"

	"github.com/fogleman/primitive/primitive"
)

// testFrame draws a soft scene whose blobs are shifted right by dx pixels.
func testFrame(dx int) *image.RGBA {
	const w, h = 96, 64
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.RGBA{40, 60, 90, 255}}, image.Point{}, draw.Src)
	for y := range h {
		for x := range w {
			fx, fy := float64(x-dx), float64(y)
			r := 255 * math.Exp(-((fx-30)*(fx-30)+(fy-30)*(fy-30))/300)
			g := 200 * math.Exp(-((fx-65)*(fx-65)+(fy-25)*(fy-25))/200)
			b := 160 * math.Exp(-((fx-50)*(fx-50)+(fy-50)*(fy-50))/150)
			img.Set(x, y, color.RGBA{uint8(min(255, 40+r)), uint8(min(255, 60+g)), uint8(min(255, 90+b)), 255})
		}
	}
	return img
}

var testBG = primitive.Color{R: 40, G: 60, B: 90, A: 255}

func frameShapes(target image.Image, p Params, prev []shapeRec, adapt int) []shapeRec {
	b := target.Bounds()
	model := primitive.NewModel(target, testBG, max(b.Dx(), b.Dy()), 2)
	buildFrame(model, p, prev, adapt)
	recs := make([]shapeRec, len(model.Shapes))
	for i := range model.Shapes {
		recs[i] = recFromModel(model, i)
	}
	return recs
}

func meanAbsDiff(a, b *image.RGBA) float64 {
	var sum float64
	for i := range a.Pix {
		sum += math.Abs(float64(a.Pix[i]) - float64(b.Pix[i]))
	}
	return sum / float64(len(a.Pix))
}

// On a small motion, carrying shapes over must change the rendering much less
// than re-running the search from scratch (which is what makes video flicker).
func TestBuildFrameReducesFlicker(t *testing.T) {
	p := Params{Shapes: 40, Mode: 1, Alpha: 128}
	f0, f1 := testFrame(0), testFrame(1)
	render := func(s []shapeRec) *image.RGBA { return renderImage(96, 64, 96, 64, testBG, s) }

	prev := frameShapes(f0, p, nil, 0)
	smooth := frameShapes(f1, p, prev, 20)
	fresh := frameShapes(f1, p, nil, 0)

	dSmooth := meanAbsDiff(render(prev), render(smooth))
	dFresh := meanAbsDiff(render(prev), render(fresh))
	t.Logf("change vs previous frame: input %.2f, smoothed %.2f, from scratch %.2f", meanAbsDiff(f0, f1), dSmooth, dFresh)
	if dSmooth > 0.7*dFresh {
		t.Errorf("smoothed frame changed %.2f, from scratch %.2f: expected well under", dSmooth, dFresh)
	}
}

// On a large motion, adapting the carried shapes must follow it: the result
// must match the new frame better than shapes that only get recoloured.
func TestBuildFrameFollowsMotion(t *testing.T) {
	p := Params{Shapes: 40, Mode: 1, Alpha: 128}
	f0, f1 := testFrame(0), testFrame(6)
	render := func(s []shapeRec) *image.RGBA { return renderImage(96, 64, 96, 64, testBG, s) }

	prev := frameShapes(f0, p, nil, 0)
	frozen := meanAbsDiff(render(frameShapes(f1, p, prev, 0)), f1)
	adapted := meanAbsDiff(render(frameShapes(f1, p, prev, 200)), f1)
	t.Logf("error vs new frame: frozen %.2f, adapted %.2f", frozen, adapted)
	if adapted >= frozen*0.95 {
		t.Errorf("adapting should track motion: error %.2f vs %.2f frozen", adapted, frozen)
	}
}

// Every shape type must survive being carried over to a new frame's worker.
func TestBuildFrameAllModes(t *testing.T) {
	f0, f1 := testFrame(0), testFrame(2)
	for mode := 0; mode <= 8; mode++ {
		for _, alpha := range []int{0, 128} {
			p := Params{Shapes: 6, Mode: mode, Alpha: alpha}
			prev := frameShapes(f0, p, nil, 0)
			next := frameShapes(f1, p, prev, 20)
			if len(next) < p.Shapes {
				t.Errorf("mode %d alpha %d: got %d shapes, want %d", mode, alpha, len(next), p.Shapes)
			}
		}
	}
}
