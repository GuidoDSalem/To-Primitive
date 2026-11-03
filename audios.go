package main

import (
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// The audio bank: a folder of songs (subfolders too) to pick from in the reel
// editor. It's only read; a song picked for a reel or a preset is kept with
// it (keepAudio), so it stays even if it leaves the folder.

var audioExts = []string{".mp3", ".m4a", ".aac", ".wav", ".ogg", ".oga", ".opus", ".flac", ".weba", ".webm", ".mp4", ".mov"}

type BankAudio struct {
	Name     string    `json:"name"` // path inside the folder, with "/"
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

type AudioBank struct {
	Dir    string      `json:"dir"`    // as the person knows it (the folder on the computer, in Docker)
	Exists bool        `json:"exists"` // false: the folder isn't there (not mounted, or a typo)
	Items  []BankAudio `json:"items"`
}

func (srv *server) audioList(w http.ResponseWriter, r *http.Request) {
	bank := AudioBank{Dir: hostDir("AUDIOS_HOST_DIR", srv.audios), Items: []BankAudio{}}
	if fi, err := os.Stat(srv.audios); srv.audios != "" && err == nil && fi.IsDir() {
		bank.Exists = true
		filepath.WalkDir(srv.audios, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // what can't be read is left out
			}
			if d.IsDir() {
				if p != srv.audios && strings.HasPrefix(d.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasPrefix(d.Name(), ".") || !slices.Contains(audioExts, strings.ToLower(filepath.Ext(p))) {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(srv.audios, p)
			bank.Items = append(bank.Items, BankAudio{Name: filepath.ToSlash(rel), Size: info.Size(), Modified: info.ModTime()})
			return nil
		})
		slices.SortFunc(bank.Items, func(a, b BankAudio) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	}
	writeJSON(w, bank)
}

func (srv *server) audioFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	clean := path.Clean("/" + name)[1:]
	if srv.audios == "" || clean == "" || clean != name || !slices.Contains(audioExts, strings.ToLower(path.Ext(clean))) {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, filepath.Join(srv.audios, filepath.FromSlash(clean)))
}
