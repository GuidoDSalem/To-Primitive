package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func needFFmpeg(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}
}

// makeMedia runs ffmpeg with args to create dir/name and returns its path.
func makeMedia(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	out := filepath.Join(dir, name)
	cmd := exec.Command("ffmpeg", append(append([]string{"-v", "error", "-y"}, args...), out)...)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v: %s", err, b)
	}
	return out
}

func silentVideo(t *testing.T, dir string, secs int) string {
	return makeMedia(t, dir, "video.mp4", "-f", "lavfi", "-i", "testsrc=size=320x240:rate=30:duration="+itoa(secs),
		"-c:v", "libx264", "-pix_fmt", "yuv420p")
}

func sine(t *testing.T, dir, name string, secs float64) string {
	return makeMedia(t, dir, name, "-f", "lavfi", "-i", "sine=frequency=440:duration="+ftoa(secs), "-c:a", "pcm_s16le")
}

func itoa(i int) string     { return strconv.Itoa(i) }
func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// rms decodes `length` seconds of audio from start, mono at 8 kHz.
func rms(t *testing.T, path string, start, length float64) float64 {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-v", "error", "-ss", ftoa(start), "-t", ftoa(length), "-i", path,
		"-vn", "-f", "s16le", "-ac", "1", "-ar", "8000", "-").Output()
	if err != nil {
		t.Fatalf("decoding audio: %v", err)
	}
	samples := make([]int16, len(out)/2)
	binary.Read(bytes.NewReader(out), binary.LittleEndian, samples)
	var sum float64
	for _, s := range samples {
		sum += float64(s) * float64(s)
	}
	return math.Sqrt(sum / float64(max(1, len(samples))))
}

// The video job can take a portion of the video and a crop of the frame:
// the keyframes cover exactly that, at that framing.
func TestComputePortionAndCrop(t *testing.T) {
	needFFmpeg(t)
	dir := t.TempDir()
	// Left quarter red; green from 1 s to 3 s, blue after; 4 s long.
	path := makeMedia(t, dir, "src.mp4", "-f", "lavfi", "-i", "color=black:size=320x240:rate=30:duration=4",
		"-vf", "format=rgb24,geq=r='255*lt(X\\,80)':g='255*gte(T\\,1)*lt(T\\,3)':b='255*gte(T\\,3)'",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-crf", "10")
	info, err := probeVideo(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	v := &Video{ID: "test", dir: dir, path: path, info: info}
	o := ComputeOpts{Params: Params{Shapes: 4, Mode: 1, Alpha: 128, InputSize: 64}, KFPS: 4,
		Start: 1.2, Length: 1.5, Crop: &Crop{X: 0.25, Y: 0, W: 0.5, H: 1}}
	if err := v.StartCompute(o); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Minute)
	for {
		v.mu.Lock()
		state, msg := v.job.State, v.job.Error
		v.mu.Unlock()
		if state != "running" {
			if state != "done" {
				t.Fatalf("job %s: %s", state, msg)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(50 * time.Millisecond)
	}
	a := v.Animation()
	t.Logf("%dx%d, %d keyframes, start %.2f, duration %.2f, background %s", a.Width, a.Height, len(a.Keyframes), a.Start, a.Duration, a.Background)
	if a.Height != 64 || a.Width < 42 || a.Width > 43 {
		t.Errorf("working size %dx%d, want ~43x64 (the middle half of 320x240, fit in 64)", a.Width, a.Height)
	}
	if n := len(a.Keyframes); n < 5 || n > 7 {
		t.Errorf("%d keyframes, want ~6 (1.5 s at 4 per second)", n)
	}
	if a.Start != 1.2 || a.Duration != 1.5 || a.Crop == nil {
		t.Errorf("animation should record the portion and crop: start %v, duration %v, crop %v", a.Start, a.Duration, a.Crop)
	}
	// Background is the average colour of the first frame: green (portion
	// starts at 1.2 s), without the red quarter (cropped away).
	if !strings.HasPrefix(a.Background, "#00") || a.Background[3:5] < "e0" {
		t.Errorf("background %s, want pure green", a.Background)
	}
}

func TestMuxReel(t *testing.T) {
	needFFmpeg(t)
	dir := t.TempDir()
	video := silentVideo(t, dir, 3)
	long := sine(t, dir, "long.wav", 5)
	short := sine(t, dir, "short.wav", 1)
	withSound := makeMedia(t, dir, "withsound.mp4", "-f", "lavfi", "-i", "testsrc=size=160x120:duration=4",
		"-f", "lavfi", "-i", "sine=frequency=220:duration=4", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest")
	ctx := context.Background()

	for _, c := range []struct {
		name  string
		audio audioSpec
	}{
		{"no audio", audioSpec{}},
		{"own audio, faded", audioSpec{File: long, Volume: 1, FadeOut: 1}},
		{"short audio, padded", audioSpec{File: short, Volume: 1}},
		{"sound of a video, from 1 s", audioSpec{File: withSound, Start: 1, Volume: 0.5, FadeOut: 0.5}},
	} {
		t.Run(c.name, func(t *testing.T) {
			out := filepath.Join(dir, strings.ReplaceAll(c.name, " ", "_")+".mp4")
			if err := muxReel(ctx, video, out, c.audio); err != nil {
				t.Fatal(err)
			}
			info, err := probeMedia(ctx, out)
			if err != nil {
				t.Fatal(err)
			}
			if math.Abs(info.Duration-3) > 0.1 {
				t.Errorf("duration %.2f, want 3 (the video's)", info.Duration)
			}
			sound, _ := hasAudio(ctx, out)
			if sound != (c.audio.File != "") {
				t.Errorf("has audio = %v, want %v", sound, c.audio.File != "")
			}
			if !sound {
				return
			}
			mid, end := rms(t, out, 1.0, 0.2), rms(t, out, 2.85, 0.15)
			t.Logf("rms: middle %.0f, end %.0f", mid, end)
			switch {
			case c.audio.FadeOut > 0 && end > mid*0.25:
				t.Errorf("should fade out: end %.0f vs middle %.0f", end, mid)
			case c.audio.File == short && (rms(t, out, 0.3, 0.2) < 1000 || end > 1):
				t.Errorf("the short audio should play first (rms %.0f) and then be silence (rms %.0f)", rms(t, out, 0.3, 0.2), end)
			case c.audio.File == long && c.audio.FadeOut == 0 && mid < 1000:
				t.Errorf("audio too quiet: %.0f", mid)
			}
		})
	}

	if err := muxReel(ctx, video, filepath.Join(dir, "x.mp4"), audioSpec{File: video}); err != errNoAudio {
		t.Errorf("a source without sound: got %v, want errNoAudio", err)
	}
}

// multipartBody builds a form with the given files and values.
func multipartBody(t *testing.T, files map[string]string, values map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for field, path := range files {
		fw, _ := w.CreateFormFile(field, filepath.Base(path))
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(fw, f)
		f.Close()
	}
	for k, v := range values {
		w.WriteField(k, v)
	}
	w.Close()
	return &buf, w.FormDataContentType()
}

func TestReelEndpoints(t *testing.T) {
	needFFmpeg(t)
	dir := t.TempDir()
	srv := newServer(filepath.Join(dir, "data"), filepath.Join(dir, "reels"))
	os.MkdirAll(srv.dataDir, 0o755)
	os.MkdirAll(srv.lib.dir, 0o755)
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()
	video := silentVideo(t, dir, 2)
	audio := sine(t, dir, "a.wav", 4)

	post := func(path string, files, values map[string]string) *http.Response {
		body, ct := multipartBody(t, files, values)
		res, err := http.Post(ts.URL+path, ct, body)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	// Mux: video + own audio.
	res := post("/api/reels/mux", map[string]string{"video": video, "audio": audio}, map[string]string{"fadeOut": "0.5"})
	if res.StatusCode != 200 {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("mux: %d %s", res.StatusCode, b)
	}
	muxed := filepath.Join(dir, "muxed.mp4")
	f, _ := os.Create(muxed)
	io.Copy(f, res.Body)
	f.Close()
	if ok, _ := hasAudio(context.Background(), muxed); !ok {
		t.Error("muxed reel has no audio")
	}
	if res := post("/api/reels/mux", nil, nil); res.StatusCode != 400 {
		t.Errorf("mux without video: %d, want 400", res.StatusCode)
	}
	if res := post("/api/reels/mux", map[string]string{"video": video}, map[string]string{"source": "nope"}); res.StatusCode != 404 {
		t.Errorf("mux with unknown source: %d, want 404", res.StatusCode)
	}

	// Library: save, list, fetch, delete.
	res = post("/api/reels", map[string]string{"video": muxed}, map[string]string{"meta": `{"template":"revelado","duration":2}`})
	if res.StatusCode != 200 {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("save: %d %s", res.StatusCode, b)
	}
	var e ReelEntry
	json.NewDecoder(res.Body).Decode(&e)
	if e.Width != 320 || e.Height != 240 || math.Abs(e.Duration-2) > 0.1 {
		t.Errorf("saved entry %+v", e)
	}
	if res := post("/api/reels", map[string]string{"video": muxed}, map[string]string{"meta": `{not json`}); res.StatusCode != 400 {
		t.Errorf("bad meta: %d, want 400", res.StatusCode)
	}
	if res := post("/api/reels", map[string]string{"video": audio}, nil); res.StatusCode != 400 {
		t.Errorf("not a video: %d, want 400", res.StatusCode)
	}

	res, _ = http.Get(ts.URL + "/api/reels")
	var list []ReelEntry
	json.NewDecoder(res.Body).Decode(&list)
	if len(list) != 1 || list[0].ID != e.ID || !strings.Contains(string(list[0].Meta), "revelado") {
		t.Fatalf("list: %+v", list)
	}
	res, _ = http.Get(ts.URL + "/api/reels/" + e.ID + "/video")
	got, _ := io.ReadAll(res.Body)
	want, _ := os.ReadFile(muxed)
	if !bytes.Equal(got, want) {
		t.Errorf("stored video differs: %d vs %d bytes", len(got), len(want))
	}

	del := func(id string) int {
		req, _ := http.NewRequest("DELETE", ts.URL+"/api/reels/"+id, nil)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode
	}
	if c := del(e.ID); c != 204 {
		t.Errorf("delete: %d", c)
	}
	if c := del(e.ID); c != 404 {
		t.Errorf("delete again: %d, want 404", c)
	}
	if c := del("..%2Fetc"); c != 400 {
		t.Errorf("bad id: %d, want 400", c)
	}
	res, _ = http.Get(ts.URL + "/api/reels")
	list = nil
	json.NewDecoder(res.Body).Decode(&list)
	if len(list) != 0 {
		t.Errorf("list after delete: %+v", list)
	}
}

// Browsers without an H.264 encoder hand in VP9: the reel comes out H.264
// anyway, and save=1 stores it in the library in the same request.
func TestMuxTranscodesAndSaves(t *testing.T) {
	needFFmpeg(t)
	dir := t.TempDir()
	srv := newServer(filepath.Join(dir, "data"), filepath.Join(dir, "reels"))
	os.MkdirAll(srv.dataDir, 0o755)
	os.MkdirAll(srv.lib.dir, 0o755)
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()
	vp9 := makeMedia(t, dir, "vp9.mp4", "-f", "lavfi", "-i", "testsrc=size=180x320:rate=30:duration=2",
		"-c:v", "libvpx-vp9", "-b:v", "200k", "-deadline", "realtime")
	audio := sine(t, dir, "a.wav", 4)

	body, ct := multipartBody(t, map[string]string{"video": vp9, "audio": audio},
		map[string]string{"save": "1", "meta": `{"template":"loop"}`})
	res, err := http.Post(ts.URL+"/api/reels/mux", ct, body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("mux+save: %d %s", res.StatusCode, b)
	}
	var e ReelEntry
	json.NewDecoder(res.Body).Decode(&e)
	if e.Width != 180 || e.Height != 320 || math.Abs(e.Duration-2) > 0.1 || !strings.Contains(string(e.Meta), "loop") {
		t.Fatalf("entry %+v", e)
	}
	vpath, _, _ := srv.lib.paths(e.ID)
	ctx := context.Background()
	if c, _ := videoCodec(ctx, vpath); c != "h264" {
		t.Errorf("stored codec %q, want h264", c)
	}
	if ok, _ := hasAudio(ctx, vpath); !ok {
		t.Error("stored reel has no audio")
	}
}

// Test frames for tuning a reel use the same framing as the computation.
func TestFrameSessionCrop(t *testing.T) {
	needFFmpeg(t)
	dir := t.TempDir()
	srv := newServer(filepath.Join(dir, "data"), filepath.Join(dir, "reels"))
	os.MkdirAll(srv.dataDir, 0o755)
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()
	body, ct := multipartBody(t, map[string]string{"video": silentVideo(t, dir, 2)}, nil)
	res, err := http.Post(ts.URL+"/api/videos", ct, body)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("upload: %v %v", err, res.Status)
	}
	var v struct{ ID string }
	json.NewDecoder(res.Body).Decode(&v)

	for _, c := range []struct {
		crop string
		w, h int
	}{
		{`{"x":0.25,"y":0,"w":0.5,"h":1}`, 160, 240},
		{`null`, 320, 240},
		{`{"x":0.8,"y":0,"w":0.5,"h":1}`, 320, 240}, // invalid: ignored
	} {
		res, err := http.Post(ts.URL+"/api/videos/"+v.ID+"/frames", "application/json",
			strings.NewReader(`{"t":1,"crop":`+c.crop+`}`))
		if err != nil || res.StatusCode != 200 {
			t.Fatalf("frames %s: %v %v", c.crop, err, res.Status)
		}
		var f struct{ Width, Height int }
		json.NewDecoder(res.Body).Decode(&f)
		if f.Width != c.w || f.Height != c.h {
			t.Errorf("crop %s: %dx%d, want %dx%d", c.crop, f.Width, f.Height, c.w, c.h)
		}
	}
}
