package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// hub fans server-sent events out to subscribers. It has no lock of its own:
// the owner calls every method under its own mutex, so a snapshot taken while
// subscribing is ordered consistently with later broadcasts.
type hub struct {
	subs map[chan []byte]struct{}
}

func sseMessage(event string, data any) []byte {
	b, _ := json.Marshal(data)
	return fmt.Appendf(nil, "event: %s\ndata: %s\n\n", event, b)
}

func (h *hub) add() chan []byte {
	if h.subs == nil {
		h.subs = map[chan []byte]struct{}{}
	}
	ch := make(chan []byte, 4096)
	h.subs[ch] = struct{}{}
	return ch
}

func (h *hub) remove(ch chan []byte) {
	delete(h.subs, ch)
}

func (h *hub) broadcast(event string, data any) {
	if len(h.subs) == 0 {
		return
	}
	msg := sseMessage(event, data)
	for ch := range h.subs {
		select {
		case ch <- msg:
		default:
			// Subscriber can't keep up: drop it. The browser's EventSource
			// reconnects and gets a fresh snapshot.
			close(ch)
			delete(h.subs, ch)
		}
	}
}

func (h *hub) closeAll() {
	for ch := range h.subs {
		close(ch)
		delete(h.subs, ch)
	}
}

// serveSSE writes the snapshot and then every message from ch until the
// client goes away or ch is closed.
func serveSSE(w http.ResponseWriter, r *http.Request, snapshot []byte, ch chan []byte) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Write(snapshot)
	flusher.Flush()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			w.Write([]byte(": ping\n\n"))
			flusher.Flush()
		case msg, ok := <-ch:
			if !ok {
				return
			}
			w.Write(msg)
			// Drain whatever else is queued before flushing.
			for drained := false; !drained; {
				select {
				case more, ok := <-ch:
					if !ok {
						flusher.Flush()
						return
					}
					w.Write(more)
				default:
					drained = true
				}
			}
			flusher.Flush()
		}
	}
}
