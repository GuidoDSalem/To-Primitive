package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestAudioBank(t *testing.T) {
	dir := t.TempDir()
	bankDir := filepath.Join(dir, "Audios")
	os.MkdirAll(filepath.Join(bankDir, "Virales"), 0o755)
	os.MkdirAll(filepath.Join(bankDir, ".oculta"), 0o755)
	os.WriteFile(filepath.Join(bankDir, "b tema.mp3"), []byte("mp3"), 0o644)
	os.WriteFile(filepath.Join(bankDir, "Virales", "a ritmo.m4a"), []byte("m4a"), 0o644)
	os.WriteFile(filepath.Join(bankDir, "notas.txt"), []byte("no"), 0o644)
	os.WriteFile(filepath.Join(bankDir, ".oculta", "x.mp3"), []byte("no"), 0o644)
	os.WriteFile(filepath.Join(dir, "afuera.mp3"), []byte("secreto"), 0o644)

	srv := newServer(filepath.Join(dir, "data"), filepath.Join(dir, "reels"))
	srv.audios = bankDir
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()

	res, err := http.Get(ts.URL + "/api/audios")
	if err != nil {
		t.Fatal(err)
	}
	var bank AudioBank
	json.NewDecoder(res.Body).Decode(&bank)
	if !bank.Exists || len(bank.Items) != 2 || bank.Items[0].Name != "b tema.mp3" || bank.Items[1].Name != "Virales/a ritmo.m4a" {
		t.Fatalf("bank %+v", bank)
	}

	get := func(path string) (int, string) {
		res, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	if code, body := get("/api/audios/files/Virales/a%20ritmo.m4a"); code != 200 || body != "m4a" {
		t.Errorf("song: %d %q", code, body)
	}
	for _, p := range []string{"/api/audios/files/notas.txt", "/api/audios/files/..%2Fafuera.mp3", "/api/audios/files/Virales/..%2F..%2Fafuera.mp3"} {
		if code, body := get(p); code == 200 || body == "secreto" {
			t.Errorf("%s: %d %q", p, code, body)
		}
	}

	// A folder that isn't there: said so, nothing listed.
	srv.audios = filepath.Join(dir, "no")
	res, _ = http.Get(ts.URL + "/api/audios")
	bank = AudioBank{}
	json.NewDecoder(res.Body).Decode(&bank)
	if bank.Exists || len(bank.Items) != 0 {
		t.Errorf("missing folder %+v", bank)
	}
}
