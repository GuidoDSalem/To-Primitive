//go:build js

// The browser demo runs the same handlers inside a Web Worker. demo/worker.js
// passes each request to goServe and gets the response back through
// goEmit(id, kind, value): "head" with the status and headers, "chunk" with
// bytes (once per Flush, so server-sent events stream), and "end".
package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"syscall/js"
	"time"
)

var (
	reqMu   sync.Mutex
	cancels = map[int]context.CancelFunc{}
)

func main() {
	mux := newServer("/tmp/to-primitive").routes()
	emit := js.Global().Get("goEmit")

	js.Global().Set("goServe", js.FuncOf(func(_ js.Value, a []js.Value) any {
		id, method, url, headers, body := a[0].Int(), a[1].String(), a[2].String(), a[3], a[4]
		buf := make([]byte, body.Get("length").Int())
		js.CopyBytesToGo(buf, body)
		ctx, cancel := context.WithCancel(context.Background())
		req := httptest.NewRequestWithContext(ctx, method, url, bytes.NewReader(buf))
		keys := js.Global().Get("Object").Call("keys", headers)
		for i := range keys.Length() {
			k := keys.Index(i).String()
			req.Header.Set(k, headers.Get(k).String())
		}
		reqMu.Lock()
		cancels[id] = cancel
		reqMu.Unlock()
		go func() {
			w := &jsWriter{id: id, emit: emit, header: http.Header{}}
			mux.ServeHTTP(w, req)
			w.Flush()
			emit.Invoke(id, "end", nil)
			abort(id)
		}()
		return nil
	}))
	js.Global().Set("goAbort", js.FuncOf(func(_ js.Value, a []js.Value) any {
		abort(a[0].Int())
		return nil
	}))
	js.Global().Call("goReady")
	select {}
}

func abort(id int) {
	reqMu.Lock()
	defer reqMu.Unlock()
	if c := cancels[id]; c != nil {
		c()
		delete(cancels, id)
	}
}

type jsWriter struct {
	id     int
	emit   js.Value
	header http.Header
	status int
	buf    []byte
}

func (w *jsWriter) Header() http.Header { return w.header }

func (w *jsWriter) WriteHeader(code int) {
	if w.status != 0 {
		return
	}
	w.status = code
	h := js.Global().Get("Object").New()
	for k := range w.header {
		h.Set(k, w.header.Get(k))
	}
	w.emit.Invoke(w.id, "head", map[string]any{"status": code, "headers": h})
}

func (w *jsWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		if w.header.Get("Content-Type") == "" {
			w.header.Set("Content-Type", http.DetectContentType(p))
		}
		w.WriteHeader(http.StatusOK)
	}
	w.buf = append(w.buf, p...)
	return len(p), nil
}

func (w *jsWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if len(w.buf) == 0 {
		return
	}
	a := js.Global().Get("Uint8Array").New(len(w.buf))
	js.CopyBytesToJS(a, w.buf)
	w.buf = w.buf[:0]
	w.emit.Invoke(w.id, "chunk", a)
}

// yield hands control back to the browser's event loop so the worker can
// receive messages (new parameters, camera frames) between steps: Go on
// WebAssembly runs on one thread and only returns to JavaScript when every
// goroutine is blocked.
func yield() { time.Sleep(time.Millisecond) }
