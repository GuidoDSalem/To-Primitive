package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/png"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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

// RenderOpts are the video-only settings, on top of the shape Params.
type RenderOpts struct {
	Params
	FPS   float64 `json:"fps"`   // 0 = the video's own frame rate
	Adapt int     `json:"adapt"` // hill-climb steps per carried-over shape
	Size  int     `json:"size"`  // longest side of the output, px
	Audio bool    `json:"audio"`
}

func (o *RenderOpts) clamp(info videoInfo) {
	o.Params.clamp()
	if o.FPS <= 0 || o.FPS > info.FPS {
		o.FPS = info.FPS
	}
	o.FPS = min(o.FPS, 60)
	o.Adapt = clampInt(o.Adapt, 0, 2000)
	o.Size = clampInt(o.Size, 64, 3840)
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

type Video struct {
	ID      string
	Created time.Time
	dir     string
	path    string
	info    videoInfo

	mu  sync.Mutex
	job *job
	hub hub
}

func (v *Video) output() string { return filepath.Join(v.dir, "output.mp4") }

func (v *Video) Close() {
	v.mu.Lock()
	if v.job != nil && v.job.cancel != nil {
		v.job.cancel()
	}
	v.hub.closeAll()
	v.mu.Unlock()
	os.RemoveAll(v.dir)
}

// ---------- ffmpeg helpers ----------

func runFFmpeg(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s: %v: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func probeVideo(ctx context.Context, path string) (videoInfo, error) {
	out, err := runFFmpeg(ctx, "ffprobe", "-v", "error", "-show_entries",
		"stream=codec_type,avg_frame_rate,r_frame_rate:format=duration", "-of", "json", path)
	if err != nil {
		return videoInfo{}, err
	}
	var probe struct {
		Streams []struct {
			CodecType    string `json:"codec_type"`
			AvgFrameRate string `json:"avg_frame_rate"`
			RFrameRate   string `json:"r_frame_rate"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		return videoInfo{}, err
	}
	var info videoInfo
	hasVideo := false
	for _, s := range probe.Streams {
		switch s.CodecType {
		case "video":
			if !hasVideo {
				hasVideo = true
				info.FPS = parseRate(s.AvgFrameRate)
				if info.FPS <= 0 {
					info.FPS = parseRate(s.RFrameRate)
				}
			}
		case "audio":
			info.HasAudio = true
		}
	}
	if !hasVideo {
		return videoInfo{}, errors.New("no video stream")
	}
	if info.FPS <= 0 || info.FPS > 240 {
		info.FPS = 30
	}
	info.Duration, _ = strconv.ParseFloat(probe.Format.Duration, 64)
	// Width and height come from a decoded frame so rotation metadata is
	// already applied.
	img, err := extractFrame(ctx, path, 0, 0)
	if err != nil {
		return videoInfo{}, err
	}
	info.Width, info.Height = img.Bounds().Dx(), img.Bounds().Dy()
	return info, nil
}

func parseRate(s string) float64 {
	num, den, ok := strings.Cut(s, "/")
	n, _ := strconv.ParseFloat(num, 64)
	if !ok {
		return n
	}
	d, _ := strconv.ParseFloat(den, 64)
	if d == 0 {
		return 0
	}
	return n / d
}

// fitFilter shrinks (never enlarges) so the longest side is at most size,
// like resize.Thumbnail does for still images.
func fitFilter(size int) string {
	return fmt.Sprintf("scale='min(%d,iw)':'min(%d,ih)':force_original_aspect_ratio=decrease:flags=bilinear", size, size)
}

// extractFrame decodes the frame at t seconds, scaled to fit size (0 = as is).
func extractFrame(ctx context.Context, path string, t float64, size int) (image.Image, error) {
	args := []string{"-v", "error", "-ss", strconv.FormatFloat(t, 'f', 3, 64), "-i", path, "-frames:v", "1"}
	if size > 0 {
		args = append(args, "-vf", fitFilter(size))
	}
	args = append(args, "-f", "image2pipe", "-c:v", "png", "-")
	out, err := runFFmpeg(ctx, "ffmpeg", args...)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no frame at %.2fs", t)
	}
	img, _, err := image.Decode(bytes.NewReader(out))
	return img, err
}

// ---------- rendering ----------

// StartRender begins rendering the whole video; it fails if one is running.
func (v *Video) StartRender(o RenderOpts) error {
	o.clamp(v.info)
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.job != nil && v.job.State == "running" {
		return errors.New("a render is already running")
	}
	ctx, cancel := context.WithCancel(context.Background())
	total := max(1, int(math.Round(v.info.Duration*o.FPS)))
	v.job = &job{State: "running", Total: total, started: time.Now(), cancel: cancel}
	v.hub.broadcast("job", v.job)
	go v.render(ctx, v.job, o)
	return nil
}

func (v *Video) CancelRender() {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.job != nil && v.job.State == "running" {
		v.job.cancel()
	}
}

func (v *Video) render(ctx context.Context, j *job, o RenderOpts) {
	err := v.renderFrames(ctx, j, o)
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

func (v *Video) renderFrames(ctx context.Context, j *job, o RenderOpts) error {
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

	// Output dimensions must be even for yuv420p.
	scale := float64(o.Size) / float64(max(w, h))
	ow := max(2, int(math.Round(float64(w)*scale/2))*2)
	oh := max(2, int(math.Round(float64(h)*scale/2))*2)

	dec := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-i", v.path,
		"-vf", fmt.Sprintf("fps=%g,%s", o.FPS, fitFilter(o.InputSize)),
		"-f", "rawvideo", "-pix_fmt", "rgba", "-")
	var decErr bytes.Buffer
	dec.Stderr = &decErr
	frames, err := dec.StdoutPipe()
	if err != nil {
		return err
	}

	encArgs := []string{"-v", "error", "-y",
		"-f", "rawvideo", "-pix_fmt", "rgba", "-s", fmt.Sprintf("%dx%d", ow, oh), "-framerate", fmt.Sprintf("%g", o.FPS), "-i", "-"}
	if o.Audio && v.info.HasAudio {
		encArgs = append(encArgs, "-i", v.path, "-map", "0:v", "-map", "1:a?", "-c:a", "aac", "-shortest")
	}
	encArgs = append(encArgs, "-c:v", "libx264", "-pix_fmt", "yuv420p", "-crf", "18", "-preset", "medium",
		"-movflags", "+faststart", v.output())
	enc := exec.CommandContext(ctx, "ffmpeg", encArgs...)
	var encErr bytes.Buffer
	enc.Stderr = &encErr
	sink, err := enc.StdinPipe()
	if err != nil {
		return err
	}

	if err := dec.Start(); err != nil {
		return err
	}
	defer dec.Wait()
	if err := enc.Start(); err != nil {
		return err
	}
	defer enc.Wait()
	defer sink.Close()

	target := image.NewRGBA(image.Rect(0, 0, w, h))
	var prev []shapeRec
	lastPreview := time.Time{}
	for {
		if _, err := io.ReadFull(frames, target.Pix); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			if errors.Is(err, io.ErrUnexpectedEOF) {
				break // trailing partial frame
			}
			return fmt.Errorf("decoding: %v: %s", err, decErr.String())
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}

		model := primitive.NewModel(target, bg, max(w, h), numWorkers)
		replaced := buildFrame(model, o.Params, prev, o.Adapt)
		cur := make([]shapeRec, len(model.Shapes))
		for i := range model.Shapes {
			cur[i] = recFromModel(model, i)
		}
		prev = cur

		img := renderImage(w, h, ow, oh, bg, cur)
		if _, err := sink.Write(img.Pix); err != nil {
			return fmt.Errorf("encoding: %v: %s", err, encErr.String())
		}

		v.mu.Lock()
		j.Frame++
		j.Total = max(j.Total, j.Frame)
		j.Replaced += replaced
		j.Elapsed = time.Since(j.started).Seconds()
		v.hub.broadcast("job", j)
		if time.Since(lastPreview) > 300*time.Millisecond {
			lastPreview = time.Now()
			v.hub.broadcast("preview", previewData(w, h, bg, cur))
		}
		v.mu.Unlock()
	}
	if j.Frame == 0 {
		return fmt.Errorf("no frames decoded: %s", decErr.String())
	}
	sink.Close()
	if err := enc.Wait(); err != nil {
		return fmt.Errorf("encoding: %v: %s", err, encErr.String())
	}
	return nil
}

func previewData(w, h int, bg primitive.Color, shapes []shapeRec) map[string]any {
	svgs := make([]string, len(shapes))
	for i, rec := range shapes {
		svgs[i] = rec.SVG
	}
	return map[string]any{
		"width": w, "height": h,
		"background": fmt.Sprintf("#%02x%02x%02x", bg.R, bg.G, bg.B),
		"svg":        strings.Join(svgs, ""),
	}
}

// Done reports whether the last render finished successfully.
func (v *Video) Done() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.job != nil && v.job.State == "done"
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
