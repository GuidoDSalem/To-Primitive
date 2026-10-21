//go:build !js

package main

import (
	"embed"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
)

//go:embed web
var webFS embed.FS

func main() {
	addr := flag.String("addr", envOr("ADDR", ":8080"), "listen address")
	dataDir := flag.String("data", envOr("DATA_DIR", filepath.Join(os.TempDir(), "to-primitive")), "directory for uploaded videos")
	flag.Parse()

	// Videos don't survive a restart (sessions live in memory), so start clean.
	os.RemoveAll(*dataDir)
	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		log.Fatal(err)
	}

	srv := newServer(*dataDir)
	web, _ := fs.Sub(webFS, "web")
	mux := srv.routes()
	mux.Handle("GET /", http.FileServerFS(web))

	log.Printf("To-Primitive listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
