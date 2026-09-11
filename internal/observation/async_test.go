package observation

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestAsyncRecorderEnqueueDoesNotBlockOnSlowRecord(t *testing.T) {
	var started atomic.Int32
	var finished atomic.Int32
	block := make(chan struct{})
	rec := NewAsyncRecorder(8, func(ctx context.Context, event Event) error {
		started.Add(1)
		select {
		case <-block:
		case <-ctx.Done():
		}
		finished.Add(1)
		return nil
	})

	// Fill one in-flight + buffer without waiting for slow record.
	deadline := time.Now().Add(200 * time.Millisecond)
	for i := 0; i < 8; i++ {
		if time.Now().After(deadline) {
			t.Fatal("Enqueue blocked on slow recorder")
		}
		if !rec.Enqueue(Event{Type: TypeToolCompleted, Tool: "t", Workspace: "w"}) {
			// queue may fill after worker takes one; non-blocking is enough
			break
		}
	}
	// Must return promptly even while record is blocked.
	_ = rec.Enqueue(Event{Type: TypeToolStarted, Tool: "t2", Workspace: "w"})
	close(block)
	rec.Close(2 * time.Second)
	if finished.Load() == 0 && started.Load() == 0 {
		t.Fatal("worker never processed events")
	}
}

func TestAsyncRecorderDropsWhenFull(t *testing.T) {
	block := make(chan struct{})
	rec := NewAsyncRecorder(1, func(ctx context.Context, event Event) error {
		<-block
		return nil
	})
	// First event may be taken by worker or sit in buffer.
	_ = rec.Enqueue(Event{Type: TypeToolStarted, Workspace: "w"})
	// Fill buffer.
	_ = rec.Enqueue(Event{Type: TypeToolStarted, Workspace: "w"})
	// Further enqueue should drop rather than block.
	done := make(chan bool, 1)
	go func() {
		done <- rec.Enqueue(Event{Type: TypeToolCompleted, Workspace: "w"})
	}()
	select {
	case <-done:
		// returned (true or false) without hanging
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Enqueue blocked when queue full")
	}
	close(block)
	rec.Close(time.Second)
}

func TestAsyncRecorderRecoversFromRecordPanic(t *testing.T) {
	var calls atomic.Int32
	rec := NewAsyncRecorder(2, func(context.Context, Event) error {
		if calls.Add(1) == 1 {
			panic("synthetic observer panic")
		}
		return nil
	})
	if !rec.Enqueue(Event{Type: TypeToolCompleted, Tool: "panic", Workspace: "demo"}) {
		t.Fatal("failed to enqueue panic event")
	}
	if !rec.Enqueue(Event{Type: TypeToolCompleted, Tool: "after", Workspace: "demo"}) {
		t.Fatal("failed to enqueue recovery event")
	}
	rec.Close(time.Second)
	if calls.Load() != 2 {
		t.Fatalf("recorder stopped after panic; calls=%d", calls.Load())
	}
}

func TestAsyncRecorderCountersTrackEnqueueAndPersistFailure(t *testing.T) {
	rec := NewAsyncRecorder(8, func(ctx context.Context, event Event) error {
		if event.Tool == "fail" {
			return errors.New("synthetic persist failure")
		}
		return nil
	})
	for i := 0; i < 3; i++ {
		if !rec.Enqueue(Event{Type: TypeToolCompleted, Workspace: "demo"}) {
			t.Fatalf("enqueue %d was rejected", i)
		}
	}
	for i := 0; i < 2; i++ {
		if !rec.Enqueue(Event{Type: TypeToolCompleted, Tool: "fail", Workspace: "demo"}) {
			t.Fatalf("failure enqueue %d was rejected", i)
		}
	}
	rec.Close(0)
	stats := rec.Stats()
	if stats.Enqueued != 5 {
		t.Fatalf("enqueued=%d, want 5", stats.Enqueued)
	}
	if stats.Dropped != 0 {
		t.Fatalf("dropped=%d, want 0", stats.Dropped)
	}
	if stats.PersistFailed != 2 {
		t.Fatalf("persistFailed=%d, want 2", stats.PersistFailed)
	}
	if stats.QueueDepth != 0 {
		t.Fatalf("queueDepth=%d, want 0 after drain", stats.QueueDepth)
	}
	if stats.Capacity != 8 {
		t.Fatalf("capacity=%d, want 8", stats.Capacity)
	}
}

func TestAsyncRecorderPressureCallbackFiresOncePerEpisode(t *testing.T) {
	block := make(chan struct{})
	rec := NewAsyncRecorder(4, func(ctx context.Context, event Event) error {
		select {
		case <-block:
		case <-ctx.Done():
		}
		return nil
	})
	var fires atomic.Int32
	rec.SetPressureCallback(func(depth, capacity int) {
		fires.Add(1)
		if capacity != 4 {
			t.Errorf("callback capacity=%d, want 4", capacity)
		}
		if depth < 4 {
			t.Errorf("callback depth=%d, want >= 4 (80%% of 4)", depth)
		}
	})
	for i := 0; i < 8; i++ {
		rec.Enqueue(Event{Type: TypeCommandOutput, Workspace: "demo"})
	}
	if got := fires.Load(); got != 1 {
		t.Fatalf("pressure callbacks=%d, want exactly one per episode", got)
	}
	if rec.Dropped() < 1 {
		t.Fatalf("dropped=%d, want at least one after saturation", rec.Dropped())
	}
	close(block)
	rec.Close(0)
	if rec.Enqueued()+rec.Dropped() != 8 {
		t.Fatalf("enqueued=%d + dropped=%d, want 8", rec.Enqueued(), rec.Dropped())
	}
}
