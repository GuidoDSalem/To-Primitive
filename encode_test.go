package main

import (
	"slices"
	"testing"

	"github.com/fogleman/primitive/primitive"
)

func TestEncodeRectangle(t *testing.T) {
	// Corners in any order; primitive fills x1..x2 inclusive.
	rec := shapeRec{Shape: &primitive.Rectangle{X1: 10, Y1: 20, X2: 5, Y2: 30}, Color: primitive.Color{R: 1, G: 2, B: 3, A: 128}}
	got := encodeShape(rec, true, nil)
	want := []float64{kindRect, 1, 2, 3, 128, 1, 8, 25.5, 6, 11, 0}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// A carried-over rotated shape must turn the short way round: 10° after 170°
// is the same rectangle as 190°.
func TestEncodeUnwrapsAngles(t *testing.T) {
	prev := []float64{kindRect, 0, 0, 0, 255, 0, 50, 50, 20, 10, 170}
	rec := shapeRec{Shape: &primitive.RotatedRectangle{X: 50, Y: 50, Sx: 20, Sy: 10, Angle: 10}}
	if got := encodeShape(rec, false, prev)[shapeHeader+4]; got != 190 {
		t.Errorf("carried angle = %v, want 190", got)
	}
	// A fresh shape has no previous one to follow.
	if got := encodeShape(rec, true, prev)[shapeHeader+4]; got != 10 {
		t.Errorf("fresh angle = %v, want 10", got)
	}
	// Unwrapping accumulates past 360° rather than snapping back.
	prev[shapeHeader+4] = 350
	rec.Shape = &primitive.RotatedRectangle{X: 50, Y: 50, Sx: 20, Sy: 10, Angle: 5}
	if got := encodeShape(rec, false, prev)[shapeHeader+4]; got != 365 {
		t.Errorf("angle after 350 = %v, want 365", got)
	}
}

// buildFrame must mark exactly the shapes that were not carried over.
func TestBuildFrameFreshFlags(t *testing.T) {
	p := Params{Shapes: 10, Mode: 1, Alpha: 128}
	b := testFrame(0).Bounds()
	model := primitive.NewModel(testFrame(0), testBG, max(b.Dx(), b.Dy()), 2)
	fresh := buildFrame(model, p, nil, 0)
	if len(fresh) != len(model.Shapes) || slices.Contains(fresh, false) {
		t.Fatalf("first frame: every shape must be fresh, got %v", fresh)
	}
	prev := make([]shapeRec, len(model.Shapes))
	for i := range model.Shapes {
		prev[i] = recFromModel(model, i)
	}
	next := primitive.NewModel(testFrame(1), testBG, max(b.Dx(), b.Dy()), 2)
	fresh = buildFrame(next, p, prev, 10)
	if len(fresh) != len(next.Shapes) {
		t.Fatalf("got %d flags for %d shapes", len(fresh), len(next.Shapes))
	}
	if !slices.Contains(fresh, false) {
		t.Errorf("a nearly identical frame should carry shapes over, got %v", fresh)
	}
}
