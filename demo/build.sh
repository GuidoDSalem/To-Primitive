#!/bin/sh
# Builds the browser-only demo into docs/app (published by GitHub Pages).
set -e
cd "$(dirname "$0")/.."
out=docs/app
rm -rf "$out"
mkdir -p "$out"
cp -R web/. "$out/"
cp demo/demo.js demo/worker.js "$out/"
cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" "$out/"
GOOS=js GOARCH=wasm go build -trimpath -ldflags="-s -w" -o "$out/to-primitive.wasm" .
# demo.js has to run before the app's scripts.
perl -0pi -e 's|(\s*)<script src="mp4.js"></script>|$1<script src="demo.js"></script>$1<script src="mp4.js"></script>|' "$out/index.html"
grep -q 'src="demo.js"' "$out/index.html"
echo "demo built in $out"
