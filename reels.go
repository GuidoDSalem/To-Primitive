package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------- audio ----------

// audioSpec says where a reel's sound comes from.
type audioSpec struct {
	File    string  // audio (or video) file to take the sound from; "" for none
	Start   float64 // offset into File, seconds
	Volume  float64 // gain, 1 = unchanged
	FadeOut float64 // seconds of fade at the end
}

var errNoAudio = errors.New("the audio source has no audio track")

// muxReel turns the video-only MP4 encoded in the browser into the final
// reel: H.264 copied as is (anything else, e.g. VP9 from browsers without an
// H.264 encoder, is re-encoded to H.264, which every platform accepts), the
// audio (if any) cut to the video's length, padded with silence if shorter,
// faded out, and AAC-encoded, with the index at the front so it plays while
// downloading.
func muxReel(ctx context.Context, videoPath, outPath string, a audioSpec) error {
	vcodec, err := videoCodec(ctx, videoPath)
	if err != nil {
		return err
	}
	cv := []string{"-c:v", "copy"}
	if vcodec != "h264" {
		cv = []string{"-c:v", "libx264", "-preset", "veryfast", "-crf", "18", "-pix_fmt", "yuv420p"}
	}
	if a.File == "" {
		args := append([]string{"-v", "error", "-y", "-i", videoPath, "-map", "0:v"}, cv...)
		_, err := runFFmpeg(ctx, "ffmpeg", append(args, "-movflags", "+faststart", outPath)...)
		return err
	}
	ok, err := hasAudio(ctx, a.File)
	if err != nil {
		return err
	}
	if !ok {
		return errNoAudio
	}
	dur, err := probeMedia(ctx, videoPath)
	if err != nil {
		return err
	}
	if a.Volume <= 0 {
		a.Volume = 1
	}
	fade := clamp(a.FadeOut, 0, dur.Duration)
	af := fmt.Sprintf("volume=%.3f,apad", a.Volume)
	if fade > 0 {
		af += fmt.Sprintf(",afade=t=out:st=%.3f:d=%.3f", dur.Duration-fade, fade)
	}
	args := []string{"-v", "error", "-y",
		"-i", videoPath,
		"-ss", strconv.FormatFloat(max(0, a.Start), 'f', 3, 64), "-i", a.File,
		"-map", "0:v", "-map", "1:a:0"}
	args = append(args, cv...)
	args = append(args, "-af", af, "-t", strconv.FormatFloat(dur.Duration, 'f', 3, 64),
		"-c:a", "aac", "-b:a", "192k", "-movflags", "+faststart", outPath)
	_, err = runFFmpeg(ctx, "ffmpeg", args...)
	return err
}

// videoCodec is the codec name of a file's first video stream ("" if none).
func videoCodec(ctx context.Context, path string) (string, error) {
	out, err := runFFmpeg(ctx, "ffprobe", "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=codec_name", "-of", "csv=p=0", path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func hasAudio(ctx context.Context, path string) (bool, error) {
	out, err := runFFmpeg(ctx, "ffprobe", "-v", "error", "-select_streams", "a",
		"-show_entries", "stream=index", "-of", "csv=p=0", path)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) != "", nil
}

type mediaInfo struct {
	Duration      float64
	Width, Height int
}

// probeMedia reads a file's duration and video size.
func probeMedia(ctx context.Context, path string) (mediaInfo, error) {
	out, err := runFFmpeg(ctx, "ffprobe", "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=width,height:format=duration", "-of", "json", path)
	if err != nil {
		return mediaInfo{}, err
	}
	var p struct {
		Streams []struct{ Width, Height int } `json:"streams"`
		Format  struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &p); err != nil {
		return mediaInfo{}, err
	}
	var m mediaInfo
	m.Duration, _ = strconv.ParseFloat(p.Format.Duration, 64)
	if len(p.Streams) > 0 {
		m.Width, m.Height = p.Streams[0].Width, p.Streams[0].Height
	}
	if m.Duration <= 0 {
		return m, fmt.Errorf("%s: unknown duration", filepath.Base(path))
	}
	return m, nil
}

// ---------- library ----------

// ReelEntry describes a reel kept in the library. Meta is whatever the
// editor sent (template, settings...), kept to compare what works best.
type ReelEntry struct {
	ID       string          `json:"id"`
	Created  time.Time       `json:"created"`
	Size     int64           `json:"size"`
	Duration float64         `json:"duration"`
	Width    int             `json:"width"`
	Height   int             `json:"height"`
	Meta     json.RawMessage `json:"meta"`
}

// library keeps finished reels on disk (unlike uploads, it survives restarts):
// <id>.mp4 and <id>.json side by side.
type library struct {
	dir string
	mu  sync.Mutex
}

var reelID = regexp.MustCompile(`^[0-9a-f]{16}$`)

func (l *library) paths(id string) (video, meta string, err error) {
	if !reelID.MatchString(id) {
		return "", "", errors.New("bad reel id")
	}
	return filepath.Join(l.dir, id+".mp4"), filepath.Join(l.dir, id+".json"), nil
}

// Save stores a finished reel with its metadata.
func (l *library) Save(ctx context.Context, video io.Reader, meta json.RawMessage) (ReelEntry, error) {
	if len(meta) == 0 {
		meta = json.RawMessage("{}")
	}
	if !json.Valid(meta) {
		return ReelEntry{}, errors.New("meta is not valid JSON")
	}
	id := newID()
	vpath, mpath, _ := l.paths(id)
	tmp := vpath + ".part"
	if err := saveFile(tmp, video); err != nil {
		os.Remove(tmp)
		return ReelEntry{}, err
	}
	info, err := probeMedia(ctx, tmp)
	if err == nil && (info.Width == 0 || info.Height == 0) {
		err = errors.New("no video track")
	}
	if err != nil {
		os.Remove(tmp)
		return ReelEntry{}, fmt.Errorf("not a playable video: %v", err)
	}
	st, err := os.Stat(tmp)
	if err != nil {
		return ReelEntry{}, err
	}
	e := ReelEntry{ID: id, Created: time.Now().UTC(), Size: st.Size(), Duration: info.Duration,
		Width: info.Width, Height: info.Height, Meta: meta}
	b, _ := json.MarshalIndent(e, "", "  ")
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.Rename(tmp, vpath); err != nil {
		return ReelEntry{}, err
	}
	if err := os.WriteFile(mpath, b, 0o644); err != nil {
		os.Remove(vpath)
		return ReelEntry{}, err
	}
	return e, nil
}

// List returns the reels, newest first.
func (l *library) List() ([]ReelEntry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	files, err := filepath.Glob(filepath.Join(l.dir, "*.json"))
	if err != nil {
		return nil, err
	}
	out := []ReelEntry{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var e ReelEntry
		if json.Unmarshal(b, &e) == nil && reelID.MatchString(e.ID) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out, nil
}

func (l *library) Delete(id string) error {
	vpath, mpath, err := l.paths(id)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := os.Stat(mpath); err != nil {
		return os.ErrNotExist
	}
	os.Remove(vpath)
	return os.Remove(mpath)
}

// ---------- handlers ----------

const maxReelUpload = 1 << 30

// saveFormFile writes an uploaded file to path; false if the field is absent.
func saveFormFile(r *http.Request, field, path string) (bool, error) {
	f, _, err := r.FormFile(field)
	if errors.Is(err, http.ErrMissingFile) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	return true, saveFile(path, f)
}

func formFloat(r *http.Request, field string, def float64) float64 {
	if v, err := strconv.ParseFloat(r.FormValue(field), 64); err == nil {
		return v
	}
	return def
}

// reelMux receives the browser-encoded video and the sound settings and
// returns the final reel. Form fields: video (file), audio (file, optional),
// source (id of an uploaded video whose sound to use, optional),
// sourceStart, audioStart, volume, fadeOut; with save=1 the reel goes to the
// library (with meta) and the answer is its entry instead of the file.
func (srv *server) reelMux(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxReelUpload)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "bad form: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer r.MultipartForm.RemoveAll()
	dir, err := os.MkdirTemp(srv.dataDir, "mux-")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer os.RemoveAll(dir)

	video := filepath.Join(dir, "video.mp4")
	if ok, err := saveFormFile(r, "video", video); err != nil || !ok {
		http.Error(w, "missing video", http.StatusBadRequest)
		return
	}
	a := audioSpec{Volume: formFloat(r, "volume", 1), FadeOut: formFloat(r, "fadeOut", 1)}
	audio := filepath.Join(dir, "audio")
	if ok, err := saveFormFile(r, "audio", audio); err != nil {
		http.Error(w, "bad audio: "+err.Error(), http.StatusBadRequest)
		return
	} else if ok {
		a.File, a.Start = audio, formFloat(r, "audioStart", 0)
	} else if id := r.FormValue("source"); id != "" {
		srv.mu.Lock()
		v := srv.videos[id]
		srv.mu.Unlock()
		if v == nil {
			http.Error(w, "source video not found", http.StatusNotFound)
			return
		}
		a.File, a.Start = v.sourcePath(), formFloat(r, "sourceStart", 0)
	}

	out := filepath.Join(dir, "reel.mp4")
	if err := muxReel(r.Context(), video, out, a); err != nil {
		status := http.StatusUnprocessableEntity
		if errors.Is(err, errNoAudio) {
			status = http.StatusBadRequest
		}
		http.Error(w, err.Error(), status)
		return
	}
	// refine=<name>: a generated version exported again from the editor.
	if name := r.FormValue("refine"); name != "" && srv.gen != nil {
		if err := srv.gen.Refine(name, out, json.RawMessage(r.FormValue("meta"))); err != nil {
			pubError(w, err)
			return
		}
		writeJSON(w, map[string]string{"name": name})
		return
	}
	// job=<id>: a reel the generator asked for (saved in its output folder).
	if job := r.FormValue("job"); job != "" && srv.gen != nil {
		name, err := srv.gen.Complete(job, out, json.RawMessage(r.FormValue("meta")))
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, map[string]string{"name": name})
		return
	}
	// save=1: keep it in the library (with meta) and answer with its entry.
	if r.FormValue("save") != "" {
		f, err := os.Open(out)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer f.Close()
		e, err := srv.lib.Save(r.Context(), f, json.RawMessage(r.FormValue("meta")))
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		writeJSON(w, e)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	http.ServeFile(w, r, out)
}

// reelSave stores a finished reel (form fields: video, meta) in the library.
func (srv *server) reelSave(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxReelUpload)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "bad form: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer r.MultipartForm.RemoveAll()
	f, _, err := r.FormFile("video")
	if err != nil {
		http.Error(w, "missing video", http.StatusBadRequest)
		return
	}
	defer f.Close()
	e, err := srv.lib.Save(r.Context(), f, json.RawMessage(r.FormValue("meta")))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, e)
}

func (srv *server) reelList(w http.ResponseWriter, r *http.Request) {
	list, err := srv.lib.List()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, list)
}

func (srv *server) reelVideo(w http.ResponseWriter, r *http.Request) {
	vpath, _, err := srv.lib.paths(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := os.Stat(vpath); err != nil {
		http.Error(w, "reel not found", http.StatusNotFound)
		return
	}
	if r.URL.Query().Has("download") {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="reel-%s.mp4"`, r.PathValue("id")))
	}
	http.ServeFile(w, r, vpath)
}

func (srv *server) reelDelete(w http.ResponseWriter, r *http.Request) {
	switch err := srv.lib.Delete(r.PathValue("id")); {
	case errors.Is(err, os.ErrNotExist):
		http.Error(w, "reel not found", http.StatusNotFound)
	case err != nil:
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
