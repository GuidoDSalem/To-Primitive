//go:build !js

package main

// yield is only needed on WebAssembly (see wasm.go).
func yield() {}
