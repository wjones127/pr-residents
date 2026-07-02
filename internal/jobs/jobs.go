// Package jobs runs named background jobs (refresh, dispatch) and fans their
// progress out to subscribers — the web server bridges those to SSE. A job name
// runs at most once concurrently, and a running job can be cancelled by name.
package jobs

import (
	"context"
	"sync"
)

// Event is a job progress update, JSON-serialized straight to SSE clients.
type Event struct {
	Job       string `json:"job"`
	Phase     string `json:"phase"`
	Repo      string `json:"repo"`
	Done      int    `json:"done"`
	Total     int    `json:"total"`
	TokensIn  int    `json:"tokens_in"`
	TokensOut int    `json:"tokens_out"`
	Message   string `json:"message"`
	Status    string `json:"status"` // "running" | "done" | "error"
}

// Manager runs jobs and broadcasts their events.
type Manager struct {
	mu      sync.Mutex
	running map[string]context.CancelFunc
	subs    map[chan Event]struct{}
}

// New returns an empty Manager.
func New() *Manager {
	return &Manager{running: map[string]context.CancelFunc{}, subs: map[chan Event]struct{}{}}
}

// Running reports whether a job with this name is in flight.
func (m *Manager) Running(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.running[name]
	return ok
}

// Cancel cancels a running job's context (no-op if it isn't running).
func (m *Manager) Cancel(name string) {
	m.mu.Lock()
	cancel := m.running[name]
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Run starts fn in a goroutine under name and returns true; returns false
// without starting if a job of that name is already running. fn receives a
// cancellable context and an emit callback; a final done/error event is
// broadcast for it.
func (m *Manager) Run(name string, fn func(ctx context.Context, emit func(Event)) error) bool {
	m.mu.Lock()
	if _, ok := m.running[name]; ok {
		m.mu.Unlock()
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.running[name] = cancel
	m.mu.Unlock()

	go func() {
		emit := func(ev Event) {
			ev.Job = name
			if ev.Status == "" {
				ev.Status = "running"
			}
			m.broadcast(ev)
		}
		err := fn(ctx, emit)
		final := Event{Job: name, Status: "done", Message: "complete"}
		if err != nil {
			final.Status = "error"
			final.Message = err.Error()
		}
		m.broadcast(final)

		m.mu.Lock()
		cancel()
		delete(m.running, name)
		m.mu.Unlock()
	}()
	return true
}

// Subscribe returns a channel of events and an unsubscribe function. The
// channel is buffered and lossy: a slow consumer drops events rather than
// stalling the job.
func (m *Manager) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 64)
	m.mu.Lock()
	m.subs[ch] = struct{}{}
	m.mu.Unlock()

	return ch, func() {
		m.mu.Lock()
		if _, ok := m.subs[ch]; ok {
			delete(m.subs, ch)
			close(ch)
		}
		m.mu.Unlock()
	}
}

func (m *Manager) broadcast(ev Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for ch := range m.subs {
		select {
		case ch <- ev:
		default: // drop for a slow subscriber
		}
	}
}
