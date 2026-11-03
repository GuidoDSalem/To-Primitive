//go:build !js

package main

import (
	"context"
	"embed"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

//go:embed web
var webFS embed.FS

// static serves the pages.
func (srv *server) static(mux *http.ServeMux) {
	web, _ := fs.Sub(webFS, "web")
	mux.Handle("GET /", http.FileServerFS(web))
}

func main() {
	addr := flag.String("addr", envOr("ADDR", ":8080"), "listen address")
	dataDir := flag.String("data", envOr("DATA_DIR", filepath.Join(os.TempDir(), "to-primitive")), "directory for uploaded videos")
	reelsDir := flag.String("reels", envOr("REELS_DIR", "reels"), "directory where finished reels are kept")
	publishDir := flag.String("publish", envOr("PUBLISH_DIR", "publicar"), "folder of videos waiting to be published")
	inputDir := flag.String("input", envOr("INPUT_DIR", "entrada"), "folder of photos and videos to turn into reels automatically")
	generatedDir := flag.String("generated", envOr("GENERATED_DIR", "generados"), "folder where automatically generated reels go")
	audiosDir := flag.String("audios", envOr("AUDIOS_DIR", "audios"), "folder of songs to pick from in the reel editor")
	flag.Parse()

	// Videos don't survive a restart (sessions live in memory), so start clean.
	os.RemoveAll(*dataDir)
	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		log.Fatal(err)
	}
	// Finished reels do survive: never cleaned up here.
	if err := os.MkdirAll(*reelsDir, 0o755); err != nil {
		log.Fatal(err)
	}

	srv := newServer(*dataDir, *reelsDir)
	srv.audios = *audiosDir
	// The accounts live with the library (a Docker volume), not in the
	// publishing folder.
	os.MkdirAll(*reelsDir, 0o755)
	srv.acc = newAccounts(filepath.Join(*reelsDir, ".cuentas.json"))
	pub, err := newPublisher(*publishDir, &platformUploader{acc: srv.acc, poll: 3 * time.Second, wait: 90 * time.Second})
	if err != nil {
		log.Fatal(err)
	}
	srv.pub = pub
	go pub.Run(context.Background(), 30*time.Second)
	// The generator drives the editor in a headless browser, through this server.
	port := *addr
	if i := strings.LastIndex(port, ":"); i >= 0 {
		port = port[i+1:]
	}
	gen, err := newGenerator(*inputDir, *generatedDir, findChrome(), "http://127.0.0.1:"+port)
	if err != nil {
		log.Fatal(err)
	}
	srv.gen = gen
	gen.pub = pub
	if gen.chrome == "" {
		log.Printf("generator: no headless browser found (install Chromium or set CHROME_BIN); reels won't be generated")
	}
	go gen.Run(context.Background())
	log.Printf("To-Primitive listening on %s (reels in %s, publishing from %s)", *addr, *reelsDir, *publishDir)
	log.Fatal(http.ListenAndServe(*addr, srv.routes()))
}
