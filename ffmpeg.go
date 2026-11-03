package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/png"
	"os/exec"
	"strconv"
	"strings"
)

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

// Crop is a region of the frame, as fractions of its width and height.
type Crop struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// valid reports whether the crop is a non-empty region inside the frame.
func (c *Crop) valid() bool {
	return c != nil && c.W > 0 && c.H > 0 && c.X >= 0 && c.Y >= 0 && c.X+c.W <= 1.0001 && c.Y+c.H <= 1.0001
}

// filter is the ffmpeg crop filter for c, or "" for none.
func (c *Crop) filter() string {
	if !c.valid() {
		return ""
	}
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', 5, 64) }
	return fmt.Sprintf("crop=iw*%s:ih*%s:iw*%s:ih*%s", f(c.W), f(c.H), f(c.X), f(c.Y))
}

// chain joins ffmpeg filters, skipping empty ones.
func chain(filters ...string) string {
	var out []string
	for _, f := range filters {
		if f != "" {
			out = append(out, f)
		}
	}
	return strings.Join(out, ",")
}

// extractFrame decodes the frame at t seconds, scaled to fit size (0 = as is).
func extractFrame(ctx context.Context, path string, t float64, size int) (image.Image, error) {
	return extractCroppedFrame(ctx, path, t, nil, size)
}

// extractCroppedFrame is extractFrame on a region of the frame (nil: all of it).
func extractCroppedFrame(ctx context.Context, path string, t float64, crop *Crop, size int) (image.Image, error) {
	args := []string{"-v", "error", "-ss", strconv.FormatFloat(t, 'f', 3, 64), "-i", path, "-frames:v", "1"}
	fit := ""
	if size > 0 {
		fit = fitFilter(size)
	}
	if vf := chain(crop.filter(), fit); vf != "" {
		args = append(args, "-vf", vf)
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
