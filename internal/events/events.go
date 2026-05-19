package events

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Event is a single event emitted onto the bus.
type Event struct {
	TS      time.Time              `json:"ts"`
	Project string                 `json:"project,omitempty"`
	Type    string                 `json:"type"`
	Fields  map[string]interface{} `json:"-"`
}

// MarshalJSON flattens Fields into the top-level JSON object.
func (e Event) MarshalJSON() ([]byte, error) {
	out := map[string]interface{}{
		"ts":   e.TS.UTC().Format(time.RFC3339Nano),
		"type": e.Type,
	}
	if e.Project != "" {
		out["project"] = e.Project
	}
	for k, v := range e.Fields {
		out[k] = v
	}
	return json.Marshal(out)
}

// Bus is a fan-out event bus. Subscribers receive events on their channels.
type Bus struct {
	mu   sync.RWMutex
	subs map[chan Event]string // channel -> project filter ("" = all)
}

func NewBus() *Bus {
	return &Bus{subs: make(map[chan Event]string)}
}

// Subscribe registers a new subscriber. projectFilter "" subscribes to all events.
func (b *Bus) Subscribe(projectFilter string, buf int) chan Event {
	if buf <= 0 {
		buf = 64
	}
	ch := make(chan Event, buf)
	b.mu.Lock()
	b.subs[ch] = projectFilter
	b.mu.Unlock()
	return ch
}

func (b *Bus) Unsubscribe(ch chan Event) {
	b.mu.Lock()
	if _, ok := b.subs[ch]; ok {
		delete(b.subs, ch)
		close(ch)
	}
	b.mu.Unlock()
}

// Publish sends an event to all matching subscribers. Non-blocking — drops on full.
func (b *Bus) Publish(e Event) {
	if e.TS.IsZero() {
		e.TS = time.Now()
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch, filter := range b.subs {
		if filter != "" && filter != e.Project {
			continue
		}
		select {
		case ch <- e:
		default:
			// subscriber too slow — drop
		}
	}
}

// Emit is a convenience helper for emitting an event with typed fields.
func (b *Bus) Emit(project, typ string, fields map[string]interface{}) {
	b.Publish(Event{
		TS:      time.Now(),
		Project: project,
		Type:    typ,
		Fields:  fields,
	})
}

// ServeSSE serves an SSE stream of events. ?project= filters by project root.
func (b *Bus) ServeSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	filter := r.URL.Query().Get("project")
	ch := b.Subscribe(filter, 128)
	defer b.Unsubscribe(ch)

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}
