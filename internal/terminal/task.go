package terminal

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// TaskStatus is long-running process state.
type TaskStatus string

const (
	TaskRunning     TaskStatus = "running"
	TaskExited      TaskStatus = "exited"
	TaskKilled      TaskStatus = "killed"
	TaskInterrupted TaskStatus = "interrupted"
	TaskFailed      TaskStatus = "failed"
)

// ProcessSpec describes a direct executable invocation. Executable and Args
// never pass through a shell. Stdin is a finite payload and reaches EOF when
// the reader is drained, which is suitable for ephemeral runtimes.
type ProcessSpec struct {
	Executable   string
	Args         []string
	Stdin        string
	WallLimit    time.Duration
	CPUTimeLimit time.Duration
}

// OutputChunk is a single stdout/stderr increment emitted after the task log
// writer has accepted the bytes. Offset is the absolute byte offset before
// this chunk within its stream.
type OutputChunk struct {
	TaskID          string
	RequestID       string
	CallID          string
	Tool            string
	RemoteSessionID string
	WorkspaceName   string
	Command         string
	WorkDir         string
	Stream          string
	Offset          int64
	Final           bool
	Data            []byte
}

// OutputSink observes task output. Implementations must be prepared for
// concurrent calls and should return quickly; the callback runs outside the
// task mutex but on the command's output-copy path.
type OutputSink func(OutputChunk)

// Task is a background command.
type Task struct {
	ID              string
	RequestID       string
	CallID          string
	Tool            string
	RemoteSessionID string
	WorkspaceName   string
	Command         string
	WorkDir         string
	Status          TaskStatus
	PID             int
	ExitCode        *int
	StartedAt       time.Time
	FinishedAt      *time.Time
	LimitReason     string

	mu           sync.Mutex
	logBuf       bytes.Buffer
	logBase      int
	log          fileLog // combined log retained for the Resource endpoint
	stdoutLog    fileLog
	stderrLog    fileLog
	logTruncated bool
	stdoutBuf    bytes.Buffer
	stderrBuf    bytes.Buffer
	stdoutBase   int
	stderrBase   int
	stdoutOffset int64
	stderrOffset int64
	stdin        io.WriteCloser
	done         chan struct{}
	db           *sql.DB
	cmd          *exec.Cmd
	group        *processGroup
	cancel       context.CancelFunc
	outputSink   func(OutputChunk)
}

const maxTaskLogBytes = 1 << 20

// MaxPersistedTaskLogBytes bounds the durable task log and is also the
// default cumulative budget for task output written to observation_events.
// Once a stream fills its budget the file log becomes a ring: head bytes are
// evicted in place and the most recent MaxPersistedTaskLogBytes stay readable,
// so the end of a long task log — where a failure's root cause lives — is
// never lost.
const MaxPersistedTaskLogBytes = 32 << 20

// fileLog is the durable, size-bounded log file for one task output stream.
// size is the logical absolute stream length and may exceed the physical file
// size once the ring wraps. Reads take logical offsets and clamp them into the
// readable window so the returned next offset never regresses.
type fileLog struct {
	path     string
	file     *os.File
	size     int64 // logical bytes accepted
	writePos int64 // physical write cursor
	wrapped  bool  // set once the first byte is overwritten
	capacity int64 // ring capacity; zero falls back to MaxPersistedTaskLogBytes
}

func (f *fileLog) cap() int64 {
	if f.capacity > 0 {
		return f.capacity
	}
	return MaxPersistedTaskLogBytes
}

// write appends content to the ring. truncated is set when eviction first
// discards output.
func (f *fileLog) write(content []byte, truncated *bool) error {
	if f.file == nil || len(content) == 0 {
		return nil
	}
	for len(content) > 0 {
		if f.writePos >= f.cap() {
			if !f.wrapped {
				f.wrapped = true
				if truncated != nil {
					*truncated = true
				}
			}
			f.writePos = 0
		}
		n := int64(len(content))
		if remaining := f.cap() - f.writePos; n > remaining {
			n = remaining
		}
		if _, err := f.file.WriteAt(content[:n], f.writePos); err != nil {
			return err
		}
		f.writePos += n
		f.size += n
		content = content[n:]
	}
	return nil
}

// window returns the readable logical byte range. A wrapped ring keeps the
// most recent capacity bytes. A wrapped file restored after a process restart
// no longer knows its rotation point, so only the pre-wrap prefix is exposed
// instead of returning overwritten bytes in a rotated (misleading) order.
func (f *fileLog) window() (base, end int64) {
	if f.wrapped {
		return f.size - f.cap(), f.size
	}
	if f.size > f.cap() {
		return 0, f.cap()
	}
	return 0, f.size
}

func (f *fileLog) physical(logical int64) int64 {
	if !f.wrapped {
		return logical
	}
	p := f.writePos - (f.size - logical)
	if p < 0 {
		p += f.cap()
	}
	return p
}

// dropped reports how many logical head bytes the ring evicted.
func (f *fileLog) dropped() int64 {
	if f.wrapped {
		return f.size - f.cap()
	}
	return 0
}

// read returns up to limit logical bytes from offset. offset is clamped into
// the readable window; the returned next offset is monotonic. ok is false when
// the backing file cannot be opened.
func (f *fileLog) read(offset, limit int64) (data []byte, next int64, ok bool) {
	base, end := f.window()
	if offset < base {
		offset = base
	}
	if offset > end {
		offset = end
	}
	if limit > end-offset {
		limit = end - offset
	}
	if limit <= 0 {
		return nil, offset, true
	}
	file, err := os.Open(f.path)
	if err != nil {
		return nil, offset, false
	}
	defer file.Close()
	buf := make([]byte, limit)
	total := 0
	if f.wrapped {
		if run := f.cap() - f.physical(offset); run < limit {
			// The range crosses the wrap seam: read to the physical end, then
			// continue at position 0 where the newest bytes were overwritten.
			n, _ := file.ReadAt(buf[:run], f.physical(offset))
			total = int(n)
			if total < int(run) {
				return buf[:total], offset + int64(total), true
			}
			n2, _ := file.ReadAt(buf[total:], 0)
			total += int(n2)
			return buf[:total], offset + int64(total), true
		}
	}
	n, _ := file.ReadAt(buf[total:], f.physical(offset))
	total += int(n)
	return buf[:total], offset + int64(total), true
}

// TaskManager tracks long tasks per process.
type TaskManager struct {
	mu         sync.Mutex
	tasks      map[string]*Task
	seq        uint64
	db         *sql.DB
	logDir     string
	sinkMu     sync.RWMutex
	outputSink OutputSink
}

// NewTaskManager creates an empty manager.
func NewTaskManager() *TaskManager {
	return &TaskManager{tasks: map[string]*Task{}}
}

// SetOutputSink replaces the non-blocking task output observer. Passing nil
// disables observation without changing task execution or log persistence.
func (m *TaskManager) SetOutputSink(sink OutputSink) {
	if m == nil {
		return
	}
	m.sinkMu.Lock()
	m.outputSink = sink
	m.sinkMu.Unlock()
}

func (m *TaskManager) emitOutput(chunk OutputChunk) {
	// Output observation is best-effort. A renderer/store regression must not
	// panic the os/exec copy goroutine and take down the MCP process.
	defer func() { _ = recover() }()
	m.sinkMu.RLock()
	sink := m.outputSink
	m.sinkMu.RUnlock()
	if sink != nil {
		sink(chunk)
	}
}

// NewPersistentTaskManager restores queryable task metadata and marks tasks
// left running by a previous MCPX process as interrupted.
func NewPersistentTaskManager(db *sql.DB, logDir string) (*TaskManager, error) {
	if db == nil || logDir == "" {
		return nil, fmt.Errorf("task database and log directory are required")
	}
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return nil, fmt.Errorf("create task log directory: %w", err)
	}
	if err := os.Chmod(logDir, 0o700); err != nil {
		return nil, fmt.Errorf("secure task log directory: %w", err)
	}
	now := time.Now().UTC().UnixMilli()
	if _, err := db.Exec(`UPDATE terminal_tasks SET status = 'interrupted', finished_at = ?, updated_at = ? WHERE status = 'running'`, now, now); err != nil {
		return nil, fmt.Errorf("recover terminal tasks: %w", err)
	}
	return &TaskManager{tasks: map[string]*Task{}, db: db, logDir: logDir}, nil
}

// StartRemote launches a durable task owned by a Remote Session.
func (m *TaskManager) StartRemote(ctx context.Context, remoteSessionID, workspaceName, workDir, command string) (*Task, error) {
	return m.start(ctx, "", "", "", remoteSessionID, workspaceName, workDir, command)
}

// StartRemoteWithObservation launches a task and carries its originating MCP
// request through output callbacks without changing ordinary task callers.
func (m *TaskManager) StartRemoteWithObservation(ctx context.Context, requestID, tool, remoteSessionID, workspaceName, workDir, command string) (*Task, error) {
	return m.StartRemoteWithObservationContext(ctx, requestID, requestID, tool, remoteSessionID, workspaceName, workDir, command)
}

// StartRemoteWithObservationContext carries call correlation through task
// output events. Empty callID falls back to requestID at the observation boundary.
func (m *TaskManager) StartRemoteWithObservationContext(ctx context.Context, requestID, callID, tool, remoteSessionID, workspaceName, workDir, command string) (*Task, error) {
	return m.start(ctx, requestID, callID, tool, remoteSessionID, workspaceName, workDir, command)
}

func (m *TaskManager) start(_ context.Context, requestID, callID, tool, remoteSessionID, workspaceName, workDir, command string) (*Task, error) {
	ctx, cancel := context.WithCancel(context.Background())
	cmd := commandShell(ctx, command)
	cmd.Dir = workDir
	configureProcess(cmd)
	return m.startPrepared(requestID, callID, tool, remoteSessionID, workspaceName, workDir, command, cmd, cancel, true, "", 0, 0)
}

// StartRemoteProcessWithObservationContext launches an explicit executable and
// a finite stdin payload without shell parsing.
func (m *TaskManager) StartRemoteProcessWithObservationContext(requestID, callID, tool, remoteSessionID, workspaceName, workDir, displayCommand string, spec ProcessSpec) (*Task, error) {
	if strings.TrimSpace(spec.Executable) == "" {
		return nil, fmt.Errorf("process executable is required")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := commandProcess(ctx, spec.Executable, spec.Args...)
	cmd.Dir = workDir
	configureProcess(cmd)
	return m.startPrepared(requestID, callID, tool, remoteSessionID, workspaceName, workDir, displayCommand, cmd, cancel, false, spec.Stdin, spec.WallLimit, spec.CPUTimeLimit)
}

func (m *TaskManager) startPrepared(requestID, callID, tool, remoteSessionID, workspaceName, workDir, command string, cmd *exec.Cmd, cancel context.CancelFunc, interactive bool, stdinContent string, wallLimit, cpuLimit time.Duration) (*Task, error) {
	cmd.Env = commandEnvironment(cmd)
	m.mu.Lock()
	if len(m.tasks) >= 256 {
		m.pruneFinishedLocked(128)
		if len(m.tasks) >= 256 {
			m.mu.Unlock()
			cancel()
			return nil, fmt.Errorf("too many active tasks")
		}
	}
	m.seq++
	id := taskID(m.seq)
	m.mu.Unlock()

	t := &Task{
		ID: id, RequestID: requestID, CallID: callID, Tool: tool,
		RemoteSessionID: remoteSessionID, WorkspaceName: workspaceName, Command: command,
		WorkDir: workDir, Status: TaskRunning, StartedAt: time.Now().UTC(), db: m.db,
		cmd: cmd, cancel: cancel, done: make(chan struct{}), outputSink: m.emitOutput,
	}
	if m.db != nil {
		t.log.path = filepath.Join(m.logDir, id+".log")
		t.stdoutLog.path = filepath.Join(m.logDir, id+".stdout.log")
		t.stderrLog.path = filepath.Join(m.logDir, id+".stderr.log")
		var err error
		if t.log.file, err = os.OpenFile(t.log.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err != nil {
			cancel()
			return nil, fmt.Errorf("create task log: %w", err)
		}
		if t.stdoutLog.file, err = os.OpenFile(t.stdoutLog.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err != nil {
			_ = t.log.file.Close()
			cancel()
			return nil, fmt.Errorf("create stdout log: %w", err)
		}
		if t.stderrLog.file, err = os.OpenFile(t.stderrLog.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err != nil {
			_ = t.stdoutLog.file.Close()
			_ = t.log.file.Close()
			cancel()
			return nil, fmt.Errorf("create stderr log: %w", err)
		}
	}
	var stdin io.WriteCloser
	if interactive {
		var err error
		stdin, err = cmd.StdinPipe()
		if err != nil {
			t.closeFiles()
			cancel()
			return nil, fmt.Errorf("create task stdin: %w", err)
		}
		t.stdin = stdin
	} else {
		cmd.Stdin = strings.NewReader(stdinContent)
	}
	cmd.Stdout = &lockedWriter{t: t, stream: "stdout"}
	cmd.Stderr = &lockedWriter{t: t, stream: "stderr"}

	group, err := startManagedProcess(cmd)
	if err != nil {
		t.closeFiles()
		if stdin != nil {
			_ = stdin.Close()
		}
		cancel()
		return nil, err
	}
	t.group = group
	if cmd.Process != nil {
		t.PID = cmd.Process.Pid
	}
	if m.db != nil {
		_, err := m.db.Exec(`INSERT INTO terminal_tasks
            (id, remote_session_id, workspace_name, workspace_path, command, status, pid, log_path, started_at, updated_at)
            VALUES (?, ?, ?, ?, ?, 'running', ?, ?, ?, ?)`, t.ID, t.RemoteSessionID, t.WorkspaceName,
			t.WorkDir, t.Command, t.PID, t.log.path, t.StartedAt.UnixMilli(), t.StartedAt.UnixMilli())
		if err != nil {
			_ = group.kill()
			_ = cmd.Wait()
			_ = group.wait()
			if t.stdin != nil {
				_ = t.stdin.Close()
			}
			t.closeFiles()
			cancel()
			return nil, fmt.Errorf("persist task: %w", err)
		}
	}
	m.mu.Lock()
	m.tasks[id] = t
	m.mu.Unlock()

	if wallLimit > 0 {
		go t.enforceWallLimit(wallLimit)
	}
	if cpuLimit > 0 {
		go t.enforceCPUTimeLimit(cpuLimit)
	}
	go func() {
		err := cmd.Wait()
		groupErr := group.wait()
		t.mu.Lock()
		if groupErr != nil {
			t.finishLocked(TaskFailed, -1)
			t.mu.Unlock()
			t.emitOutputFinal()
			close(t.done)
			cancel()
			return
		}
		if t.Status == TaskKilled {
			t.finishLocked(TaskKilled, -1)
			t.mu.Unlock()
			t.persistFinish()
			t.emitOutputFinal()
			close(t.done)
			return
		}
		t.Status = TaskExited
		code := 0
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else {
				code = -1
			}
		}
		t.ExitCode = &code
		t.finishLocked(TaskExited, code)
		t.mu.Unlock()
		// Persist completion before publishing done. Runtime.Close can close the
		// shared database immediately after Wait returns; an async update here
		// would race that close and leave SQLite sidecar rows behind.
		t.persistFinish()
		t.emitOutputFinal()
		close(t.done)
		cancel()
	}()
	return t, nil
}

type lockedWriter struct {
	t      *Task
	stream string
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.t.mu.Lock()
	n := len(p)
	if err := w.t.log.write(p, &w.t.logTruncated); err != nil {
		w.t.mu.Unlock()
		return n, err
	}
	var (
		streamLog  *fileLog
		streamBuf  *bytes.Buffer
		streamBase *int
	)
	if w.stream == "stderr" {
		streamLog, streamBuf, streamBase = &w.t.stderrLog, &w.t.stderrBuf, &w.t.stderrBase
	} else {
		streamLog, streamBuf, streamBase = &w.t.stdoutLog, &w.t.stdoutBuf, &w.t.stdoutBase
	}
	offset := w.t.stdoutOffset
	if w.stream == "stderr" {
		offset = w.t.stderrOffset
	}
	if err := streamLog.write(p, &w.t.logTruncated); err != nil {
		w.t.mu.Unlock()
		return n, err
	}
	if w.stream == "stderr" {
		w.t.stderrOffset += int64(n)
	} else {
		w.t.stdoutOffset += int64(n)
	}
	_, _ = streamBuf.Write(p)
	if overflow := streamBuf.Len() - maxTaskLogBytes; overflow > 0 {
		streamBuf.Next(overflow)
		*streamBase += overflow
	}
	_, _ = w.t.logBuf.Write(p)
	if overflow := w.t.logBuf.Len() - maxTaskLogBytes; overflow > 0 {
		w.t.logBuf.Next(overflow)
		w.t.logBase += overflow
	}
	sink := w.t.outputSink
	chunk := OutputChunk{
		TaskID: w.t.ID, RequestID: w.t.RequestID, CallID: w.t.CallID, Tool: w.t.Tool,
		RemoteSessionID: w.t.RemoteSessionID, WorkspaceName: w.t.WorkspaceName,
		Command: w.t.Command, WorkDir: w.t.WorkDir, Stream: w.stream, Offset: offset, Data: append([]byte(nil), p...),
	}
	// Calling the sink while holding t.mu guarantees every data chunk is
	// delivered before a final chunk assembled later under the same mutex
	// (happens-before), so the observation stream cannot reorder the tail. The
	// sink contract is quick, non-blocking, and must not call back into the
	// task; the observation bridge only buffers and enqueues.
	if sink != nil {
		sink(chunk)
	}
	w.t.mu.Unlock()
	return n, nil
}

func (t *Task) emitOutputFinal() {
	// Final chunks are emitted under t.mu so they cannot overtake a data chunk
	// that is still being delivered by lockedWriter.Write.
	t.mu.Lock()
	defer t.mu.Unlock()
	sink := t.outputSink
	if sink == nil {
		return
	}
	chunks := []OutputChunk{
		{TaskID: t.ID, RequestID: t.RequestID, CallID: t.CallID, Tool: t.Tool, RemoteSessionID: t.RemoteSessionID, WorkspaceName: t.WorkspaceName, Command: t.Command, WorkDir: t.WorkDir, Stream: "stdout", Offset: t.stdoutOffset, Final: true},
		{TaskID: t.ID, RequestID: t.RequestID, CallID: t.CallID, Tool: t.Tool, RemoteSessionID: t.RemoteSessionID, WorkspaceName: t.WorkspaceName, Command: t.Command, WorkDir: t.WorkDir, Stream: "stderr", Offset: t.stderrOffset, Final: true},
	}
	for _, chunk := range chunks {
		sink(chunk)
	}
}

// Get returns task if the Remote Session owns it.
func (m *TaskManager) Get(remoteSessionID, taskID string) (*Task, error) {
	m.mu.Lock()
	t, ok := m.tasks[taskID]
	m.mu.Unlock()
	if !ok && m.db != nil {
		var t Task
		var exitCode, finishedAt sql.NullInt64
		var startedAt int64
		var truncated int
		err := m.db.QueryRow(`SELECT id, remote_session_id, workspace_name, workspace_path, command,
            status, pid, exit_code, log_path, log_size, log_truncated, started_at, finished_at, limit_reason
            FROM terminal_tasks WHERE id = ? AND remote_session_id = ?`, taskID, remoteSessionID).Scan(
			&t.ID, &t.RemoteSessionID, &t.WorkspaceName, &t.WorkDir, &t.Command, &t.Status, &t.PID,
			&exitCode, &t.log.path, &t.log.size, &truncated, &startedAt, &finishedAt, &t.LimitReason)
		if err != nil {
			return nil, fmt.Errorf("task not found")
		}
		t.StartedAt = time.UnixMilli(startedAt).UTC()
		t.logTruncated = truncated != 0
		if t.log.path != "" {
			t.stdoutLog.path = strings.TrimSuffix(t.log.path, ".log") + ".stdout.log"
			t.stderrLog.path = strings.TrimSuffix(t.log.path, ".log") + ".stderr.log"
			// Stream sizes are not stored in terminal_tasks. Recover the actual
			// durable byte windows instead of treating existing files as empty.
			for _, stream := range []*fileLog{&t.stdoutLog, &t.stderrLog} {
				if info, err := os.Stat(stream.path); err == nil {
					stream.size = info.Size()
				}
			}
		}
		t.db = m.db
		if exitCode.Valid {
			code := int(exitCode.Int64)
			t.ExitCode = &code
		}
		if finishedAt.Valid {
			finished := time.UnixMilli(finishedAt.Int64).UTC()
			t.FinishedAt = &finished
		}
		return &t, nil
	}
	if !ok {
		return nil, fmt.Errorf("task not found")
	}
	if t.RemoteSessionID != remoteSessionID {
		return nil, fmt.Errorf("task belongs to another Remote Session")
	}
	return t, nil
}

// StatusView is JSON-serializable status.
func (t *Task) StatusView() map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := map[string]any{
		"execution_task_id": t.ID,
		"status":            t.Status,
		"pid":               t.PID,
		"runtime_ms":        time.Since(t.StartedAt).Milliseconds(),
		"workspace":         t.WorkspaceName,
		"command":           t.Command,
		"log_truncated":     t.logTruncated,
	}
	if dropped := t.log.dropped(); dropped > 0 {
		out["log_dropped_bytes"] = dropped
	}
	if t.FinishedAt != nil {
		out["finished_at"] = *t.FinishedAt
		out["runtime_ms"] = t.FinishedAt.Sub(t.StartedAt).Milliseconds()
	}
	if t.ExitCode != nil {
		out["exit_code"] = *t.ExitCode
	}
	if t.LimitReason != "" {
		out["limit_reason"] = t.LimitReason
	}
	return out
}

// Logs returns the combined incremental log retained for backward-compatible
// Resource reads. New task_manage callers should use LogsFor for independent
// stdout and stderr offsets.
func (t *Task) Logs(offset int) (chunk string, next int) {
	return t.logsFor("combined", offset)
}

func (t *Task) LogsFor(stream string, offset int) (chunk string, next int) {
	switch stream {
	case "stdout", "stderr":
		return t.logsFor(stream, offset)
	default:
		return t.logsFor("combined", offset)
	}
}

const logsChunkLimit = 256 << 10

func (t *Task) logsFor(stream string, offset int) (chunk string, next int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	f := &t.log
	buffer := &t.logBuf
	base := t.logBase
	switch stream {
	case "stdout":
		f, buffer, base = &t.stdoutLog, &t.stdoutBuf, t.stdoutBase
	case "stderr":
		f, buffer, base = &t.stderrLog, &t.stderrBuf, t.stderrBase
	}
	if f.path != "" {
		data, next64, ok := f.read(int64(offset), logsChunkLimit)
		if !ok {
			return "", offset
		}
		// A byte limit may land inside a UTF-8 rune. Leave that incomplete
		// suffix for the next read so JSON encoding cannot replace it with U+FFFD.
		if next64 < f.size && len(data) > 0 {
			start := len(data) - 1
			for start > 0 && !utf8.RuneStart(data[start]) {
				start--
			}
			if !utf8.FullRune(data[start:]) {
				next64 -= int64(len(data) - start)
				data = data[:start]
			}
		}
		return string(data), int(next64)
	}
	data := buffer.Bytes()
	if offset < base {
		offset = base
	}
	if offset > base+len(data) {
		offset = base + len(data)
	}
	return string(data[offset-base:]), base + len(data)
}

func (t *Task) LogSize() int64 {
	return t.LogStreamSize("combined")
}

// LogStreamSize returns the absolute byte size for a requested output stream.
// For ring-truncated streams this is the logical stream length, which may
// exceed the physical file size because head bytes were evicted.
func (t *Task) LogStreamSize(stream string) int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	f := &t.log
	buffer := &t.logBuf
	base := t.logBase
	switch stream {
	case "stdout":
		f, buffer, base = &t.stdoutLog, &t.stdoutBuf, t.stdoutBase
	case "stderr":
		f, buffer, base = &t.stderrLog, &t.stderrBuf, t.stderrBase
	}
	if f.path != "" {
		return f.size
	}
	return int64(base + buffer.Len())
}

func (t *Task) ReadAllLogs(maxBytes int64) ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if maxBytes <= 0 {
		maxBytes = 8 << 20
	}
	if t.log.path != "" {
		base, end := t.log.window()
		if end-base > maxBytes {
			return nil, fmt.Errorf("task log exceeds resource limit; use terminal_logs pagination")
		}
		data, _, ok := t.log.read(base, end-base)
		if !ok {
			return nil, fmt.Errorf("task log is unavailable")
		}
		return data, nil
	}
	if int64(t.logBuf.Len()) > maxBytes {
		return nil, fmt.Errorf("task log exceeds resource limit; use terminal_logs pagination")
	}
	return append([]byte(nil), t.logBuf.Bytes()...), nil
}

func (m *TaskManager) pruneFinishedLocked(target int) {
	for id, task := range m.tasks {
		if len(m.tasks) <= target {
			return
		}
		task.mu.Lock()
		finished := task.Status != TaskRunning
		task.mu.Unlock()
		if finished {
			delete(m.tasks, id)
		}
	}
}

// Kill stops the task. It is idempotent: killing a task that already left the
// running state is a no-op. Terminal state is recorded while holding the task
// mutex, but the process kill and the SQLite persistence happen outside it so
// a slow database write never blocks concurrent output writes or log reads.
func (t *Task) Kill() error {
	t.mu.Lock()
	if t.Status != TaskRunning {
		t.mu.Unlock()
		return nil
	}
	code := -1
	t.ExitCode = &code
	t.finishLocked(TaskKilled, code)
	group, cmd, cancel := t.group, t.cmd, t.cancel
	t.mu.Unlock()
	var killErr error
	if group != nil {
		killErr = group.kill()
	} else {
		killProcessTree(cmd)
	}
	if cancel != nil {
		cancel()
	}
	t.persistFinish()
	return killErr
}

func (t *Task) enforceWallLimit(limit time.Duration) {
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case <-t.done:
		return
	case <-timer.C:
		t.terminateForLimit("wall_time_limit")
	}
}

func (t *Task) enforceCPUTimeLimit(limit time.Duration) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-t.done:
			return
		case <-ticker.C:
			t.mu.Lock()
			pid, running := t.PID, t.Status == TaskRunning
			t.mu.Unlock()
			if !running {
				return
			}
			if used, ok := processCPUTime(pid); ok && used >= limit {
				t.terminateForLimit("cpu_time_limit")
				return
			}
		}
	}
}

func (t *Task) terminateForLimit(reason string) {
	t.mu.Lock()
	if t.Status != TaskRunning {
		t.mu.Unlock()
		return
	}
	t.Status = TaskKilled
	t.LimitReason = reason
	group, cancel := t.group, t.cancel
	t.mu.Unlock()
	if group != nil {
		_ = group.kill()
	}
	if cancel != nil {
		cancel()
	}
}

// Running returns all live tasks owned by the process, independently of the
// bounded historical page. Completion state is copied while holding task locks.
func (m *TaskManager) Running(remoteSessionID string) []map[string]any {
	m.mu.Lock()
	tasks := make([]*Task, 0, len(m.tasks))
	for _, task := range m.tasks {
		if task.RemoteSessionID == remoteSessionID {
			tasks = append(tasks, task)
		}
	}
	m.mu.Unlock()
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	result := make([]map[string]any, 0, len(tasks))
	for _, task := range tasks {
		view := task.StatusView()
		if view["status"] == TaskRunning {
			result = append(result, view)
		}
	}
	return result
}

// List returns newest durable tasks for a Remote Session.
func (m *TaskManager) List(remoteSessionID string, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if m.db == nil {
		m.mu.Lock()
		items := make([]*Task, 0, len(m.tasks))
		for _, task := range m.tasks {
			if task.RemoteSessionID == remoteSessionID {
				items = append(items, task)
			}
		}
		m.mu.Unlock()
		sort.Slice(items, func(i, j int) bool { return items[i].StartedAt.After(items[j].StartedAt) })
		if len(items) > limit {
			items = items[:limit]
		}
		result := make([]map[string]any, 0, len(items))
		for _, task := range items {
			result = append(result, task.StatusView())
		}
		return result, nil
	}

	rows, err := m.db.Query(`SELECT id, workspace_name, command, status, pid,
        exit_code, log_truncated, started_at, finished_at
        FROM terminal_tasks WHERE remote_session_id = ? ORDER BY started_at DESC, id DESC LIMIT ?`, remoteSessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]map[string]any, 0, limit)
	for rows.Next() {
		var id, workspaceName, command, status string
		var pid, logTruncated int
		var exitCode, startedAt, finishedAt sql.NullInt64
		if err := rows.Scan(&id, &workspaceName, &command, &status, &pid,
			&exitCode, &logTruncated, &startedAt, &finishedAt); err != nil {
			return nil, err
		}
		m.mu.Lock()
		active := m.tasks[id]
		m.mu.Unlock()
		if active != nil {
			items = append(items, active.StatusView())
			continue
		}
		started := time.UnixMilli(startedAt.Int64).UTC()
		view := map[string]any{
			"execution_task_id": id, "status": TaskStatus(status), "pid": pid,
			"workspace": workspaceName, "command": command, "log_truncated": logTruncated != 0,
			"runtime_ms": time.Since(started).Milliseconds(),
		}
		if finishedAt.Valid {
			finished := time.UnixMilli(finishedAt.Int64).UTC()
			view["finished_at"] = finished
			view["runtime_ms"] = finished.Sub(started).Milliseconds()
		}
		if exitCode.Valid {
			view["exit_code"] = int(exitCode.Int64)
		}
		items = append(items, view)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

// finishLocked records terminal state in memory. Callers hold t.mu and must
// call persistFinish after unlocking. The FinishedAt guard makes repeat calls
// idempotent — when Kill races the Wait goroutine, the first finish wins and
// FinishedAt is never overwritten.
func (t *Task) finishLocked(status TaskStatus, code int) {
	if t.FinishedAt != nil {
		return
	}
	now := time.Now().UTC()
	t.Status = status
	t.ExitCode = &code
	t.FinishedAt = &now
	if t.stdin != nil {
		_ = t.stdin.Close()
		t.stdin = nil
	}
	t.closeFiles()
}

// persistFinish stores terminal task state in SQLite without holding t.mu, so
// the synchronous database round trip never blocks output or log reads.
func (t *Task) persistFinish() {
	if t.db == nil {
		return
	}
	t.mu.Lock()
	if t.FinishedAt == nil {
		t.mu.Unlock()
		return
	}
	finishedAt := *t.FinishedAt
	status, id, logSize, truncated, limitReason := t.Status, t.ID, t.log.size, t.logTruncated, t.LimitReason
	code := -1
	if t.ExitCode != nil {
		code = *t.ExitCode
	}
	t.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = t.db.ExecContext(ctx, `UPDATE terminal_tasks SET status = ?, exit_code = ?, log_size = ?, log_truncated = ?, limit_reason = ?, finished_at = ?, updated_at = ? WHERE id = ?`,
		status, code, logSize, boolInt(truncated), limitReason, finishedAt.UnixMilli(), finishedAt.UnixMilli(), id)
}

func (t *Task) closeFiles() {
	for _, file := range []*fileLog{&t.log, &t.stdoutLog, &t.stderrLog} {
		if file.file != nil {
			_ = file.file.Sync()
			_ = file.file.Close()
			file.file = nil
		}
	}
}

// Wait waits for a live task to exit. Restored tasks have no process and are
// already terminal or marked interrupted, so they never report a false live wait.
func (t *Task) Wait(ctx context.Context) bool {
	if t.done == nil {
		return false
	}
	select {
	case <-t.done:
		return true
	case <-ctx.Done():
		return false
	}
}

// WriteStdin safely forwards interactive input to a live Task. stdin is not
// persisted and is unavailable after a server restart.
func (t *Task) WriteStdin(content string) error {
	t.mu.Lock()
	if t.Status != TaskRunning || t.stdin == nil {
		t.mu.Unlock()
		return fmt.Errorf("task stdin is unavailable")
	}
	stdin := t.stdin
	t.mu.Unlock()
	// A pipe write may block until the child reads. Never hold the lifecycle
	// lock: Kill/Status/output collection must remain available, and closing
	// the pipe during shutdown unblocks the writer.
	_, err := io.WriteString(stdin, content)
	return err
}

func taskID(sequence uint64) string {
	var random [8]byte
	if _, err := rand.Read(random[:]); err == nil {
		return "task_" + base64.RawURLEncoding.EncodeToString(random[:])
	}
	return fmt.Sprintf("task_%d_%d", sequence, time.Now().UnixNano())
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// Close stops live tasks before the database is closed.
func (m *TaskManager) Close() {
	m.mu.Lock()
	tasks := make([]*Task, 0, len(m.tasks))
	for _, task := range m.tasks {
		tasks = append(tasks, task)
	}
	m.mu.Unlock()
	for _, task := range tasks {
		_ = task.Kill()
	}
}

var _ io.Writer = (*lockedWriter)(nil)
