package jobs

import (
	"context"
	"errors"
	"testing"
	"time"
)

func recv(t *testing.T, ch <-chan Event) Event {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
		return Event{}
	}
}

func TestRunEmitsProgressAndDone(t *testing.T) {
	m := New()
	ch, unsub := m.Subscribe()
	defer unsub()

	if !m.Run("refresh", func(ctx context.Context, emit func(Event)) error {
		emit(Event{Phase: "search", Done: 1, Total: 2})
		return nil
	}) {
		t.Fatal("Run should start")
	}

	ev := recv(t, ch)
	if ev.Job != "refresh" || ev.Phase != "search" || ev.Status != "running" || ev.Done != 1 || ev.Total != 2 {
		t.Errorf("progress event: %+v", ev)
	}
	ev = recv(t, ch)
	if ev.Status != "done" {
		t.Errorf("final event should be done: %+v", ev)
	}
}

func TestRunReportsError(t *testing.T) {
	m := New()
	ch, unsub := m.Subscribe()
	defer unsub()

	m.Run("x", func(ctx context.Context, emit func(Event)) error { return errors.New("boom") })
	ev := recv(t, ch)
	if ev.Status != "error" || ev.Message != "boom" {
		t.Errorf("error event: %+v", ev)
	}
}

func TestRunDedupsWhileRunning(t *testing.T) {
	m := New()
	release := make(chan struct{})
	started := make(chan struct{})

	if !m.Run("job", func(ctx context.Context, emit func(Event)) error {
		close(started)
		<-release
		return nil
	}) {
		t.Fatal("first Run should start")
	}
	<-started
	if m.Run("job", func(ctx context.Context, emit func(Event)) error { return nil }) {
		t.Error("second Run of a running job should return false")
	}
	if !m.Running("job") {
		t.Error("job should report running")
	}
	close(release)

	// After completion, Running clears (poll briefly).
	deadline := time.After(2 * time.Second)
	for m.Running("job") {
		select {
		case <-deadline:
			t.Fatal("job never cleared running state")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestCancelPropagates(t *testing.T) {
	m := New()
	started := make(chan struct{})
	cancelled := make(chan struct{})
	m.Run("job", func(ctx context.Context, emit func(Event)) error {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return ctx.Err()
	})
	<-started
	m.Cancel("job")
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not reach the job's context")
	}
}
