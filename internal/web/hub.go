// Package web serves the --web UI: a chat pane driving the same agent
// loop the terminal uses, and an iframe onto the project's own dev
// server, wired so server and browser errors flow back into the agent
// automatically.
package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
)

// hub broadcasts server-sent events to every connected browser tab. A
// client that isn't listening yet simply misses events published before it
// subscribed; chat history is replayed separately from the session log, so
// a late tab still catches up on what was said, if not on the exact
// progress trail of a run already in flight.
type hub struct {
	mutex   sync.Mutex
	clients map[chan event]struct{}
}

type event struct {
	Name string
	Data any
}

func newHub() *hub {
	return &hub{clients: map[chan event]struct{}{}}
}

func (h *hub) publish(name string, data any) {
	h.mutex.Lock()
	defer h.mutex.Unlock()
	for client := range h.clients {
		select {
		case client <- event{Name: name, Data: data}:
		default:
			// A slow or stuck client must not block every other tab, or the
			// agent run itself; dropping its event is the right trade.
		}
	}
}

func (h *hub) subscribe() (chan event, func()) {
	client := make(chan event, 64)
	h.mutex.Lock()
	h.clients[client] = struct{}{}
	h.mutex.Unlock()
	unsubscribe := func() {
		h.mutex.Lock()
		delete(h.clients, client)
		h.mutex.Unlock()
		close(client)
	}
	return client, unsubscribe
}

// ServeHTTP streams every published event to this one connection as
// Server-Sent Events, which is all a local, single-page UI needs from the
// server: push in one direction, plain POSTs for the other.
func (h *hub) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	flusher, ok := response.(http.Flusher)
	if !ok {
		http.Error(response, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("Cache-Control", "no-cache")
	response.Header().Set("Connection", "keep-alive")
	response.WriteHeader(http.StatusOK)
	flusher.Flush()

	client, unsubscribe := h.subscribe()
	defer unsubscribe()

	for {
		select {
		case <-request.Context().Done():
			return
		case evt := <-client:
			body, err := json.Marshal(evt.Data)
			if err != nil {
				continue
			}
			fmt.Fprintf(response, "event: %s\ndata: %s\n\n", evt.Name, body)
			flusher.Flush()
		}
	}
}
