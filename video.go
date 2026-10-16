package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/fogleman/primitive/primitive"
)

type videoInfo struct {
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	FPS      float64 `json:"fps"`
	Duration float64 `json:"duration"`
	HasAudio bool    `json:"hasAudio"`
}

// ComputeOpts are the video-only settings, on top of the shape Params.
type ComputeOpts struct {
	Params
	KFPS  float64 `json:"kfps"`  // keyframes per second
	Adapt int     `json:"adapt"` // hill-climb steps per carried-over shape
}

func (o *ComputeOpts) clamp(info videoInfo) {
	o.Params.clamp()
	if o.KFPS <= 0 {
		o.KFPS = 8
	}
	o.KFPS = min(o.KFPS, info.FPS, 60)
	o.Adapt = clampInt(o.Adapt, 0, 2000)
}

type job struct {
	State    string  `json:"state"` // running, done, error, canceled
	Frame    int     `json:"frame"`
	Total    int     `json:"total"`
	Elapsed  float64 `json:"elapsed"`
	Replaced int     `json:"replaced"`
	Error    string  `json:"error,omitempty"`

	started time.Time
	cancel  context.CancelFunc
}

// Animation is the result of a compute job: the shapes of every keyframe,
// encoded by encodeShape, in working-size coordinates. The browser
// interpolates between keyframes and renders at any resolution and frame rate.
type Animation struct {
	Width      int           `json:"width"`
	Height     int           `json:"height"`
	Background string        `json:"background"`
	KFPS       float64       `json:"kfps"`
	Duration   float64       `json:"duration"`
	Keyframes  [][][]float64 `json:"keyframes"`
}

type Video struct {
	ID      string
	Created time.Time
	dir     string
	path    string
	info    videoInfo

	mu   sync.Mutex
	job  *job
	anim *Animation
	hub  hub
}

func (v *Video) Close() {
	v.mu.Lock()
	if v.job != nil && v.job.cancel != nil {
		v.job.cancel()
	}
	v.hub.closeAll()
	v.mu.Unlock()
	os.RemoveAll(v.dir)
}

// StartCompute begins computing the keyframes; it fails if a job is running.
func (v *Video) StartCompute(o ComputeOpts) error {
	o.clamp(v.info)
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.job != nil && v.job.State == "running" {
		return errors.New("a computation is already running")
	}
	ctx, cancel := context.WithCancel(context.Background())
	total := max(1, int(math.Round(v.info.Duration*o.KFPS)))
	v.job = &job{State: "running", Total: total, started: time.Now(), cancel: cancel}
	v.anim = nil
	v.hub.broadcast("job", v.job)
	go v.compute(ctx, v.job, o)
	return nil
}

func (v *Video) CancelCompute() {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.job != nil && v.job.State == "running" {
		v.job.cancel()
	}
}

func (v *Video) compute(ctx context.Context, j *job, o ComputeOpts) {
	err := v.computeKeyframes(ctx, j, o)
	v.mu.Lock()
	defer v.mu.Unlock()
	j.Elapsed = time.Since(j.started).Seconds()
	switch {
	case ctx.Err() != nil:
		j.State = "canceled"
	case err != nil:
		j.State, j.Error = "error", err.Error()
	default:
		j.State = "done"
		j.Total = j.Frame
	}
	j.cancel()
	v.hub.broadcast("job", j)
}

func (v *Video) computeKeyframes(ctx context.Context, j *job, o ComputeOpts) error {
	// Working-size dimensions, from a frame decoded with the same scaling.
	first, err := extractFrame(ctx, v.path, 0, o.InputSize)
	if err != nil {
		return err
	}
	w, h := first.Bounds().Dx(), first.Bounds().Dy()
	var bg primitive.Color
	if o.Background == "" {
		bg = primitive.MakeColor(primitive.AverageImageColor(first))
	} else {
		bg = primitive.MakeHexColor(o.Background)
	}

	anim := &Animation{
		Width: w, Height: h,
		Background: fmt.Sprintf("#%02x%02x%02x", bg.R, bg.G, bg.B),
		KFPS:       o.KFPS, Duration: v.info.Duration,
	}
	v.mu.Lock()
	v.anim = anim
	v.hub.broadcast("animation", anim)
	v.mu.Unlock()

	dec := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-i", v.path,
		"-vf", fmt.Sprintf("fps=%g,%s", o.KFPS, fitFilter(o.InputSize)),
		"-f", "rawvideo", "-pix_fmt", "rgba", "-")
	var decErr bytes.Buffer
	dec.Stderr = &decErr
	frames, err := dec.StdoutPipe()
	if err != nil {
		return err
	}
	if err := dec.Start(); err != nil {
		return err
	}
	defer dec.Wait()

	target := image.NewRGBA(image.Rect(0, 0, w, h))
	var prev []shapeRec
	var prevEnc [][]float64
	for {
		if _, err := io.ReadFull(frames, target.Pix); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				break // done (a trailing partial frame is dropped)
			}
			return fmt.Errorf("decoding: %v: %s", err, decErr.String())
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}

		model := primitive.NewModel(target, bg, max(w, h), numWorkers)
		fresh := buildFrame(model, o.Params, prev, o.Adapt)
		cur := make([]shapeRec, len(model.Shapes))
		enc := make([][]float64, len(model.Shapes))
		replaced := 0
		for i := range model.Shapes {
			cur[i] = recFromModel(model, i)
			var p []float64
			if !fresh[i] && i < len(prevEnc) {
				p = prevEnc[i]
			}
			enc[i] = encodeShape(cur[i], fresh[i], p)
			if fresh[i] && prev != nil {
				replaced++
			}
		}
		prev, prevEnc = cur, enc

		v.mu.Lock()
		index := len(anim.Keyframes)
		anim.Keyframes = append(anim.Keyframes, enc)
		j.Frame++
		j.Total = max(j.Total, j.Frame)
		j.Replaced += replaced
		j.Elapsed = time.Since(j.started).Seconds()
		v.hub.broadcast("keyframe", map[string]any{"i": index, "shapes": enc})
		v.hub.broadcast("job", j)
		v.mu.Unlock()
	}
	if j.Frame == 0 {
		return fmt.Errorf("no frames decoded: %s", decErr.String())
	}
	return nil
}

// Animation returns a snapshot of the computed keyframes (nil if none).
func (v *Video) Animation() *Animation {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.anim == nil {
		return nil
	}
	a := *v.anim
	a.Keyframes = a.Keyframes[:len(a.Keyframes):len(a.Keyframes)]
	return &a
}

// MuxAudio copies the original audio into the browser-encoded video at
// videoPath and writes the result to outPath.
func (v *Video) MuxAudio(ctx context.Context, videoPath, outPath string) error {
	_, err := runFFmpeg(ctx, "ffmpeg", "-v", "error", "-y", "-i", videoPath, "-i", v.path,
		"-map", "0:v", "-map", "1:a?", "-c:v", "copy", "-c:a", "aac", "-shortest",
		"-movflags", "+faststart", outPath)
	return err
}

func (v *Video) Subscribe() ([]byte, chan []byte) {
	v.mu.Lock()
	defer v.mu.Unlock()
	ch := v.hub.add()
	return sseMessage("job", v.job), ch
}

func (v *Video) Unsubscribe(ch chan []byte) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.hub.remove(ch)
}

func (v *Video) sourcePath() string { return v.path }

func (v *Video) tempPath(name string) string { return filepath.Join(v.dir, name) }
