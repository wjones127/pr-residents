// Package jobs runs named background jobs (refresh, later dispatch) and fans
// their progress out to subscribers — the web server bridges those to SSE.
// A job name runs at most once concurrently.
package jobs

import "sync"

// Event is a job progress update, JSON-serialized straight to SSE clients.
type Event struct {
	Job     string `json:"job"`
	Phase   string `json:"phase"`
	Repo    string `json:"repo"`
	Done    int    `json:"done"`
	Total   int    `json:"total"`
	Message string `json:"message"`
	Status  string `json:"status"` // "running" | "done" | "error"
}

// Manager runs jobs and broadcasts their events.
type Manager struct {
	mu      sync.Mutex
	running map[string]bool
	subs    map[chan Event]struct{}
}

// New returns an empty Manager.
func New() *Manager {
	return &Manager{running: map[string]bool{}, subs: map[chan Event]struct{}{}}
}

// Running reports whether a job with this name is in flight.
func (m *Manager) Running(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running[name]
}

// Run starts fn in a goroutine under name and returns true; returns false
// without starting if a job of that name is already running. fn receives an
// emit callback for progress; a final done/error event is broadcast for it.
func (m *Manager) Run(name string, fn func(emit func(Event)) error) bool {
	m.mu.Lock()
	if m.running[name] {
		m.mu.Unlock()
		return false
	}
	m.running[name] = true
	m.mu.Unlock()

	go func() {
		emit := func(ev Event) {
			ev.Job = name
			if ev.Status == "" {
				ev.Status = "running"
			}
			m.broadcast(ev)
		}
		err := fn(emit)
		final := Event{Job: name, Status: "done", Message: "complete"}
		if err != nil {
			final.Status = "error"
			final.Message = err.Error()
		}
		m.broadcast(final)

		m.mu.Lock()
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
