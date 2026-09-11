package terminal

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

func newTestFileLog(t *testing.T, capacity int64) *fileLog {
	t.Helper()
	f := &fileLog{
		path:     filepath.Join(t.TempDir(), "stream.log"),
		capacity: capacity,
	}
	file, err := os.OpenFile(f.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.file = file
	t.Cleanup(func() { _ = file.Close() })
	return f
}

// TestFileLogRingKeepsTailAndMonotonicOffsets verifies that once the ring
// wraps the most recent bytes stay readable at their logical offsets while
// evicted head offsets clamp forward monotonically. This is the contract that
// keeps a long task's failure root cause (the end of the log) readable.
func TestFileLogRingKeepsTailAndMonotonicOffsets(t *testing.T) {
	const capacity = 64
	f := newTestFileLog(t, capacity)
	truncated := false

	// logical stream: "012345...89" repeated, 200 bytes total.
	var written []byte
	for i := 0; i < 200; i += 10 {
		chunk := []byte("0123456789")
		chunk[0] = byte('A' + (i/10)%26)
		if err := f.write(chunk, &truncated); err != nil {
			t.Fatal(err)
		}
		written = append(written, chunk...)
	}
	if !truncated {
		t.Fatal("ring did not mark truncation after wrap")
	}
	if f.size != 200 {
		t.Fatalf("logical size=%d, want 200", f.size)
	}
	if dropped := f.dropped(); dropped != 200-64 {
		t.Fatalf("dropped=%d, want %d", dropped, 200-64)
	}

	// Reading the whole readable window returns exactly the last 64 bytes.
	data, next, ok := f.read(0, 256<<10)
	if !ok {
		t.Fatal("read failed")
	}
	wantTail := written[200-64:]
	if string(data) != string(wantTail) {
		t.Fatalf("window content=%q, want tail %q", data, wantTail)
	}
	if next != 200 {
		t.Fatalf("next=%d, want 200", next)
	}

	// Paginate monotonically through the logical stream from an evicted offset.
	prev := int64(0)
	paginated := ""
	for offset := int64(0); ; {
		data, next, ok := f.read(offset, 16)
		if !ok {
			t.Fatal("paged read failed")
		}
		if next < prev {
			t.Fatalf("next offset regressed: %d -> %d", prev, next)
		}
		paginated += string(data)
		prev = next
		if next == 200 {
			break
		}
		if next == offset {
			t.Fatalf("stuck at offset %d", offset)
		}
		offset = next
	}
	if paginated != string(wantTail) {
		t.Fatalf("paginated=%q, want tail %q", paginated, wantTail)
	}

	// An offset beyond the logical end clamps to the end.
	data, next, ok = f.read(1000, 16)
	if !ok || data != nil || next != 200 {
		t.Fatalf("past-end read: data=%q next=%d ok=%v", data, next, ok)
	}
}

// TestFileLogRestoredWrappedFileExposesPrefixOnly verifies the restart policy:
// without in-memory ring state a wrapped file exposes its pre-wrap prefix in
// physical order rather than returning rotated bytes as logical content.
func TestFileLogRestoredWrappedFileExposesPrefixOnly(t *testing.T) {
	f := newTestFileLog(t, 64)
	truncated := false
	if err := f.write(make([]byte, 100), &truncated); err != nil {
		t.Fatal(err)
	}
	// Simulate a process restart: ring state lost, logical size from DB.
	restored := &fileLog{path: f.path, size: 100, capacity: 64}
	base, end := restored.window()
	if base != 0 || end != 64 {
		t.Fatalf("restored window=(%d,%d), want (0,64)", base, end)
	}
	data, next, ok := restored.read(0, 256<<10)
	if !ok || len(data) != 64 || next != 64 {
		t.Fatalf("restored read len=%d next=%d ok=%v", len(data), next, ok)
	}
}

// TestTaskFinishLockedIsIdempotent calls finishLocked twice and asserts the
// FinishedAt guard: the first finish wins, later calls never overwrite the
// terminal state (Kill racing the Wait goroutine).
func TestTaskFinishLockedIsIdempotent(t *testing.T) {
	task := &Task{ID: "task_finish", Status: TaskRunning}
	task.mu.Lock()
	task.finishLocked(TaskKilled, -1)
	first := *task.FinishedAt
	task.finishLocked(TaskExited, 0)
	task.mu.Unlock()
	if task.Status != TaskKilled {
		t.Fatalf("status=%s, want killed (second finish must not win)", task.Status)
	}
	if task.ExitCode == nil || *task.ExitCode != -1 {
		t.Fatalf("exit code=%v, want -1", task.ExitCode)
	}
	if !task.FinishedAt.Equal(first) {
		t.Fatalf("FinishedAt overwritten: %v -> %v", first, *task.FinishedAt)
	}
}

// TestKillIsIdempotentAndTerminatesProcess kills a live task twice: the first
// call terminates the process and records terminal state, the second is a
// no-op. It also proves the FinishedAt guard: Kill and the Wait goroutine both
// call finish, and FinishedAt is never overwritten.
func TestKillIsIdempotentAndTerminatesProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-specific")
	}
	manager := NewTaskManager()
	task, err := manager.StartRemote(context.Background(), "rs_kill", "project", t.TempDir(), "sleep 5")
	if err != nil {
		t.Fatal(err)
	}
	if err := task.Kill(); err != nil {
		t.Fatal(err)
	}
	first := task.StatusView()["finished_at"].(time.Time)
	if task.StatusView()["status"] != TaskKilled {
		t.Fatalf("status=%v", task.StatusView()["status"])
	}
	if err := task.Kill(); err != nil {
		t.Fatal(err)
	}
	if second := task.StatusView()["finished_at"].(time.Time); !first.Equal(second) {
		t.Fatalf("FinishedAt overwritten by second kill: %v -> %v", first, second)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if !task.Wait(waitCtx) {
		t.Fatal("killed task did not reach done")
	}
	if got := task.StatusView()["status"]; got != TaskKilled {
		t.Fatalf("status after wait=%v", got)
	}
}

// TestFinalOutputChunkDeliveredAfterDataChunks pins the happens-before
// contract: the sink runs under the task mutex for data writes and for the
// final chunks, so a stream's final marker can never overtake its last data
// chunk in the observation stream.
func TestFinalOutputChunkDeliveredAfterDataChunks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-specific")
	}
	var mu sync.Mutex
	var sequence []OutputChunk
	manager := NewTaskManager()
	manager.SetOutputSink(func(chunk OutputChunk) {
		mu.Lock()
		sequence = append(sequence, chunk)
		mu.Unlock()
	})
	task, err := manager.StartRemote(context.Background(), "rs_final_order", "demo", t.TempDir(), "printf out-1; printf out-2; printf err-1 >&2")
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if !task.Wait(waitCtx) {
		t.Fatal("task did not exit")
	}
	// task.Wait publishes done after the final chunks are emitted, so every
	// sink call is visible here.
	mu.Lock()
	defer mu.Unlock()
	lastDataOffset := map[string]int64{}
	for _, chunk := range sequence {
		if chunk.Final {
			if chunk.Offset != lastDataOffset[chunk.Stream] {
				t.Fatalf("final chunk for %s at offset %d, want %d (data chunks must be delivered first)", chunk.Stream, chunk.Offset, lastDataOffset[chunk.Stream])
			}
			continue
		}
		lastDataOffset[chunk.Stream] = chunk.Offset + int64(len(chunk.Data))
	}
	var stdoutFinal, stderrFinal bool
	for _, chunk := range sequence {
		if chunk.Final && chunk.Stream == "stdout" {
			stdoutFinal = true
		}
		if chunk.Final && chunk.Stream == "stderr" {
			stderrFinal = true
		}
	}
	if !stdoutFinal || !stderrFinal {
		t.Fatalf("missing final chunks: stdout=%v stderr=%v", stdoutFinal, stderrFinal)
	}
}
