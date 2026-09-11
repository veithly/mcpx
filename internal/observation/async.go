package observation

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"mcpx/internal/logging"
)

const (
	// defaultAsyncBuffer rides out multi-second SQLite contention bursts: at
	// observed tool-call rates a 256-slot queue overflowed within ~13 minutes.
	defaultAsyncBuffer = 2048
	// defaultAsyncDrain is the minimum Close drain window. Callers may pass a
	// smaller hint; the window is clamped up so a full queue can flush.
	defaultAsyncDrain = 5 * time.Second
	// Slightly above observationWriteTimeout; request path never waits here.
	asyncWriteTimeout = 7 * time.Second
)

// Queue pressure thresholds as fractions of capacity. The pressure signal
// fires once when depth crosses highWater and re-arms below lowWater.
const (
	queuePressureHighNum = 4 // 80%
	queuePressureHighDen = 5
	queuePressureLowNum  = 1 // 50%
	queuePressureLowDen  = 2
)

// AsyncRecorderStats is a point-in-time snapshot of queue health. Every
// counter is monotonic except QueueDepth.
type AsyncRecorderStats struct {
	Enqueued      int64
	Dropped       int64
	PersistFailed int64
	QueueDepth    int64
	Capacity      int
}

// AsyncRecorder writes observation events off the tools/call hot path.
// Enqueue never waits on Store.Append; overflow drops with a log line and is
// counted, as are persistence failures, so saturation is observable.
type AsyncRecorder struct {
	ch         chan Event
	record     func(context.Context, Event) error
	wg         sync.WaitGroup
	mu         sync.Mutex
	closed     bool
	onPressure func(depth, capacity int)

	enqueued      atomic.Int64
	dropped       atomic.Int64
	persistFailed atomic.Int64
	queueDepth    atomic.Int64
	pressured     atomic.Bool
}

// NewAsyncRecorder starts a single worker. record is typically bridge.Record.
func NewAsyncRecorder(buffer int, record func(context.Context, Event) error) *AsyncRecorder {
	if buffer <= 0 {
		buffer = defaultAsyncBuffer
	}
	if record == nil {
		record = func(context.Context, Event) error { return nil }
	}
	a := &AsyncRecorder{
		ch:     make(chan Event, buffer),
		record: record,
	}
	a.wg.Add(1)
	go a.loop()
	return a
}

// SetPressureCallback registers a callback invoked at most once per saturation
// episode (depth above 80% of capacity) until depth falls back under 50%.
// It is typically used to emit one observer.notice instead of per-event logs.
func (a *AsyncRecorder) SetPressureCallback(callback func(depth, capacity int)) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.onPressure = callback
	a.mu.Unlock()
}

// Enqueue queues an event without blocking tools/call. Returns false if dropped.
func (a *AsyncRecorder) Enqueue(event Event) bool {
	if a == nil {
		return false
	}
	// The closed check and the send must hold the same mutex as Close's
	// channel close, or a sender that passed the check can race the close.
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return false
	}
	select {
	case a.ch <- event:
		a.mu.Unlock()
		a.enqueued.Add(1)
		// Read onPressure under the lock, fire outside it: the callback emits
		// observation events and must not run while Close waits for the mutex.
		a.mu.Lock()
		onPressure := a.onPressure
		a.mu.Unlock()
		a.reportPressure(int(a.queueDepth.Add(1)), cap(a.ch), onPressure)
		return true
	default:
		a.mu.Unlock()
		a.dropped.Add(1)
		fields := []any{"type", event.Type, "tool", event.Tool, "workspace", event.Workspace}
		if event.Type == TypeCommandOutput && event.OperationID != "" {
			fields = append(fields, "operation_id", event.OperationID, "recovery", "observe(view=logs)")
		}
		logging.With("component", "workspace_observer").Error("observation queue full; dropping event; durable task log remains the recovery source", fields...)
		return false
	}
}

// reportPressure transitions the saturation episode state. It fires the
// callback (or a fallback log line) once on crossing the high watermark.
func (a *AsyncRecorder) reportPressure(depth, capacity int, onPressure func(int, int)) {
	if capacity <= 0 {
		return
	}
	if depth*queuePressureHighDen > capacity*queuePressureHighNum {
		if a.pressured.CompareAndSwap(false, true) {
			if onPressure != nil {
				onPressure(depth, capacity)
				return
			}
			logging.With("component", "workspace_observer").Error("observation queue above 80% capacity; events will drop when full", "depth", depth, "capacity", capacity)
		}
		return
	}
	if depth*queuePressureLowDen <= capacity*queuePressureLowNum {
		a.pressured.CompareAndSwap(true, false)
	}
}

func (a *AsyncRecorder) loop() {
	defer a.wg.Done()
	for event := range a.ch {
		a.queueDepth.Add(-1)
		// Record is best-effort (publish + optional persist). Never let a slow
		// SQLite write stall the whole observation pipeline for long.
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					a.persistFailed.Add(1)
					logging.With("component", "workspace_observer").Error("observation recorder panic recovered", "type", event.Type, "tool", event.Tool, "panic", recovered)
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), asyncWriteTimeout)
			err := a.record(ctx, event)
			cancel()
			if err != nil {
				a.persistFailed.Add(1)
			}
		}()
		a.rearmPressure()
	}
}

// rearmPressure re-arms the saturation episode once the queue drains below the
// low watermark.
func (a *AsyncRecorder) rearmPressure() {
	if depth := len(a.ch); depth*queuePressureLowDen <= cap(a.ch)*queuePressureLowNum {
		a.pressured.CompareAndSwap(true, false)
	}
}

// Enqueued returns the number of events accepted into the queue.
func (a *AsyncRecorder) Enqueued() int64 {
	if a == nil {
		return 0
	}
	return a.enqueued.Load()
}

// Dropped returns the number of events discarded because the queue was full.
func (a *AsyncRecorder) Dropped() int64 {
	if a == nil {
		return 0
	}
	return a.dropped.Load()
}

// PersistFailed returns the number of events whose record call failed or
// panicked.
func (a *AsyncRecorder) PersistFailed() int64 {
	if a == nil {
		return 0
	}
	return a.persistFailed.Load()
}

// QueueDepth returns the number of events queued or in flight.
func (a *AsyncRecorder) QueueDepth() int64 {
	if a == nil {
		return 0
	}
	return a.queueDepth.Load()
}

// Capacity returns the queue capacity.
func (a *AsyncRecorder) Capacity() int {
	if a == nil {
		return 0
	}
	return cap(a.ch)
}

// Stats returns a snapshot of queue health for status endpoints.
func (a *AsyncRecorder) Stats() AsyncRecorderStats {
	return AsyncRecorderStats{
		Enqueued:      a.Enqueued(),
		Dropped:       a.Dropped(),
		PersistFailed: a.PersistFailed(),
		QueueDepth:    a.QueueDepth(),
		Capacity:      a.Capacity(),
	}
}

// Close stops accepting events, closes the queue so the worker drains remaining
// items, and waits up to timeout for the worker to finish. The drain window is
// a floor of defaultAsyncDrain: a full 2048-slot queue needs longer than the
// old 2s hint to flush through SQLite under contention.
func (a *AsyncRecorder) Close(timeout time.Duration) {
	if a == nil {
		return
	}
	if timeout < defaultAsyncDrain {
		timeout = defaultAsyncDrain
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return
	}
	a.closed = true
	close(a.ch)
	a.mu.Unlock()

	done := make(chan struct{})
	go func() {
		a.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		logging.With("component", "workspace_observer").Error("async observation drain timed out")
	}
}
