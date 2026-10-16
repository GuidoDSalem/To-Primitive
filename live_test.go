package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/fogleman/primitive/primitive"
)

func recsOf(model *primitive.Model) []shapeRec {
	recs := make([]shapeRec, len(model.Shapes))
	for i := range model.Shapes {
		recs[i] = recFromModel(model, i)
	}
	return recs
}

func newTestModel(img *image.RGBA) *primitive.Model {
	b := img.Bounds()
	return primitive.NewModel(img, testBG, max(b.Dx(), b.Dy()), 2)
}

// withSquare returns a copy of img with a bright square in its top-left corner.
func withSquare(img *image.RGBA, size int) *image.RGBA {
	out := image.NewRGBA(img.Bounds())
	draw.Draw(out, out.Bounds(), img, image.Point{}, draw.Src)
	draw.Draw(out, image.Rect(0, 0, size, size), &image.Uniform{color.RGBA{255, 255, 255, 255}}, image.Point{}, draw.Src)
	return out
}

func TestBuildFrameBudgetRespectsDeadline(t *testing.T) {
	p := Params{Shapes: 60, Mode: 1, Alpha: 128}
	f0, f1 := testFrame(0), testFrame(4)
	prev := frameShapes(f0, p, nil, 0)

	// One full search step is the most the deadline can be overrun by.
	m := newTestModel(f1)
	t0 := time.Now()
	m.Step(primitive.ShapeTypeTriangle, 128, 0)
	step := time.Since(t0)

	budget := 30 * time.Millisecond
	var bs budgetState
	t0 = time.Now()
	fresh, st := buildFrameBudget(newTestModel(f1), p, prev, 200, newChangeMap(f0, f1), t0.Add(budget), &bs)
	took := time.Since(t0)
	t.Logf("budget %v, took %v (one step %v); %+v", budget, took, step, st)
	if took > budget+step+20*time.Millisecond {
		t.Errorf("took %v for a %v budget", took, budget)
	}
	if len(fresh) < len(prev) {
		t.Errorf("lost shapes: %d of %d", len(fresh), len(prev))
	}
}

func TestBuildFrameBudgetStaticFrame(t *testing.T) {
	p := Params{Shapes: 40, Mode: 1, Alpha: 128}
	f0 := testFrame(0)
	prev := frameShapes(f0, p, nil, 0)
	var bs budgetState
	_, st := buildFrameBudget(newTestModel(f0), p, prev, 0, newChangeMap(f0, f0), time.Now().Add(time.Second), &bs)
	if st.Adapted != 0 || st.Recolored != len(prev) || st.Replaced != 0 {
		t.Errorf("an unchanged frame without refinement should only recolour: %+v", st)
	}
}

func TestBuildFrameBudgetAdaptsWhatChanged(t *testing.T) {
	p := Params{Shapes: 40, Mode: 1, Alpha: 128}
	f0 := testFrame(0)
	f1 := withSquare(f0, 20)
	prev := frameShapes(f0, p, nil, 0)
	change := newChangeMap(f0, f1)
	touching := 0
	for _, r := range prev {
		if change.mean(shapeBounds(r.Data)) >= minChange {
			touching++
		}
	}
	var bs budgetState
	_, st := buildFrameBudget(newTestModel(f1), p, prev, 50, change, time.Now().Add(time.Second), &bs)
	t.Logf("%d of %d shapes over the changed corner; %+v", touching, len(prev), st)
	// The changed ones come first; spare budget refines others in turns.
	if st.Adapted < touching {
		t.Errorf("adapted %d shapes, want at least the %d over the change", st.Adapted, touching)
	}
	if bs.adaptCost <= 0 {
		t.Errorf("cost per adapted shape not measured")
	}
}

func TestBuildFrameBudgetGrowsProgressively(t *testing.T) {
	p := Params{Shapes: 400, Mode: 1, Alpha: 128}
	f0 := testFrame(0)
	var bs budgetState
	m := newTestModel(f0)
	_, st := buildFrameBudget(m, p, nil, 0, nil, time.Now().Add(15*time.Millisecond), &bs)
	first := len(m.Shapes)
	m2 := newTestModel(f0)
	_, st2 := buildFrameBudget(m2, p, recsOf(m), 0, newChangeMap(f0, f0), time.Now().Add(15*time.Millisecond), &bs)
	t.Logf("first frame %d shapes (%+v), second %d (%+v)", first, st, len(m2.Shapes), st2)
	if first == 0 || first >= p.Shapes {
		t.Errorf("first frame should start with a few shapes, got %d", first)
	}
	if len(m2.Shapes) <= first {
		t.Errorf("second frame should add shapes: %d -> %d", first, len(m2.Shapes))
	}
}

// TestLiveThroughput measures keyframes per second on a real video, as the
// live loop would run them. Set LIVE_BENCH to a video file to run it.
func TestLiveThroughput(t *testing.T) {
	path := os.Getenv("LIVE_BENCH")
	if path == "" {
		t.Skip("set LIVE_BENCH=<video> to measure")
	}
	for _, c := range []struct{ shapes, size, budget, adapt int }{
		{50, 96, 80, 30}, {100, 128, 120, 30}, {100, 128, 200, 30}, {200, 128, 120, 30}, {150, 192, 150, 30},
	} {
		frames := decodeFrames(t, path, c.size, 10, 60)
		p := Params{Shapes: c.shapes, Mode: 1, Alpha: 128}
		var prev []shapeRec
		var prevImg *image.RGBA
		var bs budgetState
		var total time.Duration
		var adapted, count int
		for _, f := range frames {
			t0 := time.Now()
			m := newTestModel(f)
			_, st := buildFrameBudget(m, p, prev, c.adapt, newChangeMap(prevImg, f), t0.Add(time.Duration(c.budget)*time.Millisecond), &bs)
			prev = recsOf(m)
			total += time.Since(t0)
			prevImg = f
			adapted += st.Adapted
			count++
		}
		avg := total / time.Duration(count)
		fmt.Printf("shapes %3d  size %3d  budget %3dms: %5.1f ms/keyframe = %4.1f keyframes/s, %d shapes reached, %.0f adapted per keyframe\n",
			c.shapes, c.size, c.budget, float64(avg.Microseconds())/1000, 1e6/float64(avg.Microseconds()), len(prev), float64(adapted)/float64(count))
	}
}

func decodeFrames(t *testing.T, path string, size, fps, n int) []*image.RGBA {
	t.Helper()
	first, err := extractFrame(t.Context(), path, 0, size)
	if err != nil {
		t.Fatal(err)
	}
	w, h := first.Bounds().Dx(), first.Bounds().Dy()
	cmd := exec.Command("ffmpeg", "-v", "error", "-i", path, "-vf", fmt.Sprintf("fps=%d,%s", fps, fitFilter(size)),
		"-frames:v", fmt.Sprint(n), "-f", "rawvideo", "-pix_fmt", "rgba", "-")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Wait()
	var frames []*image.RGBA
	for {
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		if _, err := io.ReadFull(out, img.Pix); err != nil {
			break
		}
		frames = append(frames, img)
	}
	return frames
}
