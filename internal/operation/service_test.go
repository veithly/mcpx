package operation

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mcpx/internal/state"
)

func newTestService(t *testing.T, workers int) *Service {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UnixMilli()
	if _, err := store.DB().Exec(`INSERT INTO principals (id, kind, subject_hash, created_at, last_seen_at) VALUES ('principal', 'test', 'subject', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`INSERT INTO remote_sessions (id, workspace_name, workspace_path, label, description, status, owner_principal_id, version, created_at, last_active_at) VALUES ('session', 'workspace', ?, 'test', '', 'active', 'principal', 1, ?, ?)`, t.TempDir(), now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`INSERT INTO remote_session_members (remote_session_id, principal_id, role, joined_at, last_active_at) VALUES ('session', 'principal', 'owner', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	service, err := New(store.DB(), workers, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	return service
}

func TestServiceRunsIndependentStepsConcurrentlyAndHonorsDependencies(t *testing.T) {
	service := newTestService(t, 3)
	started := make(chan string, 3)
	release := make(chan struct{})
	var mu sync.Mutex
	seen := map[string]bool{}
	executor := func(ctx context.Context, input ExecuteInput) ExecuteResult {
		started <- input.StepID
		if input.StepID == "a" || input.StepID == "b" {
			<-release
		}
		return ExecuteResult{Result: []byte(`{"step":"done"}`)}
	}
	record, err := service.Submit(context.Background(), SubmitSpec{
		RemoteSessionID: "session",
		WorkspaceName:   "workspace",
		RequestID:       "request",
		Purpose:         "test operation",
		Steps: []StepSpec{
			{ID: "a", Tool: "source_read"},
			{ID: "b", Tool: "source_read"},
			{ID: "c", Tool: "source_read", DependsOn: []string{"a", "b"}},
		},
	}, executor)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		select {
		case step := <-started:
			mu.Lock()
			seen[step] = true
			mu.Unlock()
		case <-time.After(time.Second):
			t.Fatal("independent steps did not start concurrently")
		}
	}
	if !seen["a"] || !seen["b"] {
		t.Fatalf("started steps=%v", seen)
	}
	close(release)
	select {
	case step := <-started:
		if step != "c" {
			t.Fatalf("dependent step started before dependencies: %q", step)
		}
	case <-time.After(time.Second):
		t.Fatal("dependent step did not start")
	}
	final, timedOut, err := service.Wait(context.Background(), record.ID, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if timedOut || final.State != StateSucceeded {
		t.Fatalf("final=%+v timedOut=%v", final, timedOut)
	}
}

func TestServiceSkipsDescendantsAfterFailure(t *testing.T) {
	service := newTestService(t, 2)
	called := make(chan string, 2)
	record, err := service.Submit(context.Background(), SubmitSpec{
		RemoteSessionID: "session", WorkspaceName: "workspace", RequestID: "request", Purpose: "test failure",
		Steps: []StepSpec{
			{ID: "fail", Tool: "source_read"},
			{ID: "child", Tool: "source_read", DependsOn: []string{"fail"}},
		},
	}, func(ctx context.Context, input ExecuteInput) ExecuteResult {
		called <- input.StepID
		return ExecuteResult{Err: errors.New("expected failure")}
	})
	if err != nil {
		t.Fatal(err)
	}
	final, timedOut, err := service.Wait(context.Background(), record.ID, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if timedOut || final.State != StateFailed {
		t.Fatalf("final=%+v timedOut=%v", final, timedOut)
	}
	if got := <-called; got != "fail" {
		t.Fatalf("called step=%q", got)
	}
	for _, step := range final.Steps {
		if step.ID == "child" && step.State != StateSkipped {
			t.Fatalf("child state=%s", step.State)
		}
	}
}

func TestServiceCancelsRunningStep(t *testing.T) {
	service := newTestService(t, 1)
	started := make(chan struct{})
	record, err := service.Submit(context.Background(), SubmitSpec{
		RemoteSessionID: "session", WorkspaceName: "workspace", RequestID: "request", Purpose: "test cancel",
		Steps: []StepSpec{{ID: "run", Tool: "command_run"}},
	}, func(ctx context.Context, input ExecuteInput) ExecuteResult {
		close(started)
		<-ctx.Done()
		return ExecuteResult{Err: ctx.Err()}
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("step did not start")
	}
	if _, err := service.Cancel(context.Background(), record.ID); err != nil {
		t.Fatal(err)
	}
	final, err := service.Get(context.Background(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.State != StateCancelled {
		t.Fatalf("state=%s", final.State)
	}
}

func TestServiceRejectsDependencyCycle(t *testing.T) {
	service := newTestService(t, 1)
	_, err := service.Submit(context.Background(), SubmitSpec{
		RemoteSessionID: "session", WorkspaceName: "workspace", Purpose: "cycle",
		Steps: []StepSpec{{ID: "a", Tool: "source_read", DependsOn: []string{"b"}}, {ID: "b", Tool: "source_read", DependsOn: []string{"a"}}},
	}, func(context.Context, ExecuteInput) ExecuteResult { return ExecuteResult{} })
	if !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("err=%v", err)
	}
}

func TestServicePersistsSingleStepResultOnOperation(t *testing.T) {
	service := newTestService(t, 1)
	record, err := service.Submit(context.Background(), SubmitSpec{
		RemoteSessionID: "session", WorkspaceName: "workspace", RequestID: "request", Purpose: "result",
		Steps: []StepSpec{{ID: "main", Tool: "source_read"}},
	}, func(context.Context, ExecuteInput) ExecuteResult {
		return ExecuteResult{Result: []byte(`{"value":42}`)}
	})
	if err != nil {
		t.Fatal(err)
	}
	final, timedOut, err := service.Wait(context.Background(), record.ID, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if timedOut || final.State != StateSucceeded {
		t.Fatalf("final=%+v timedOut=%v", final, timedOut)
	}
	page, err := service.Result(context.Background(), record.ID, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(page.Result) != `{"value":42}` {
		t.Fatalf("operation result=%s", page.Result)
	}
}

func TestServiceResumesWaitingConfirmationStep(t *testing.T) {
	service := newTestService(t, 1)
	var mu sync.Mutex
	confirmed := false
	record, err := service.Submit(context.Background(), SubmitSpec{
		RemoteSessionID: "session", WorkspaceName: "workspace", RequestID: "request", Purpose: "test confirmation",
		Steps: []StepSpec{{ID: "confirm", Tool: "change_apply"}},
	}, func(ctx context.Context, input ExecuteInput) ExecuteResult {
		mu.Lock()
		defer mu.Unlock()
		if !confirmed {
			return ExecuteResult{WaitingConfirmation: true, ConfirmationToken: "confirm-token"}
		}
		if input.Arguments["confirmation_token"] != "confirm-token" {
			return ExecuteResult{Err: errors.New("confirmation token was not forwarded")}
		}
		return ExecuteResult{Result: []byte(`{"confirmed":true}`)}
	})
	if err != nil {
		t.Fatal(err)
	}
	waiting, timedOut, err := service.Wait(context.Background(), record.ID, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if timedOut || waiting.State != StateWaitingConfirmation {
		t.Fatalf("waiting state=%q steps=%+v timedOut=%v", waiting.State, waiting.Steps, timedOut)
	}
	mu.Lock()
	confirmed = true
	mu.Unlock()
	if _, err := service.Resume(context.Background(), record.ID, "confirm", "confirm-token", nil); err != nil {
		t.Fatal(err)
	}
	final, timedOut, err := service.Wait(context.Background(), record.ID, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if timedOut || final.State != StateSucceeded {
		t.Fatalf("final=%+v timedOut=%v", final, timedOut)
	}
}

func TestServiceRecoversFromExecutorPanicAndKeepsWorkerUsable(t *testing.T) {
	service := newTestService(t, 2)
	record, err := service.Submit(context.Background(), SubmitSpec{
		RemoteSessionID: "session", WorkspaceName: "workspace", RequestID: "panic_request", Purpose: "panic isolation",
		Steps: []StepSpec{{ID: "panic", Tool: "execute"}, {ID: "healthy", Tool: "read"}},
	}, func(ctx context.Context, input ExecuteInput) ExecuteResult {
		if input.StepID == "panic" {
			panic("synthetic executor panic")
		}
		return ExecuteResult{Result: []byte(`{"ok":true}`)}
	})
	if err != nil {
		t.Fatal(err)
	}
	final, timedOut, err := service.Wait(context.Background(), record.ID, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if timedOut || final.State != StateFailed {
		t.Fatalf("panic operation=%+v timedOut=%v", final, timedOut)
	}
	for _, step := range final.Steps {
		if step.ID == "panic" && step.State != StateFailed {
			t.Fatalf("panic step=%+v", step)
		}
		if step.ID == "healthy" && step.State != StateSucceeded {
			t.Fatalf("independent healthy step=%+v", step)
		}
	}

	recovered, err := service.Submit(context.Background(), SubmitSpec{
		RemoteSessionID: "session", WorkspaceName: "workspace", RequestID: "after_panic", Purpose: "worker recovery",
		Steps: []StepSpec{{ID: "after", Tool: "read"}},
	}, func(context.Context, ExecuteInput) ExecuteResult {
		return ExecuteResult{Result: []byte(`{"recovered":true}`)}
	})
	if err != nil {
		t.Fatal(err)
	}
	final, timedOut, err = service.Wait(context.Background(), recovered.ID, 2*time.Second)
	if err != nil || timedOut || final.State != StateSucceeded {
		t.Fatalf("worker did not recover: final=%+v timedOut=%v err=%v", final, timedOut, err)
	}
}

func TestStepTimeoutFailsStepAndKeepsWorkerUsable(t *testing.T) {
	service := newTestService(t, 1)
	service.stepTimeout = 50 * time.Millisecond
	started := make(chan struct{})
	record, err := service.Submit(context.Background(), SubmitSpec{
		RemoteSessionID: "session", WorkspaceName: "workspace", RequestID: "timeout_request", Purpose: "step deadline",
		Steps: []StepSpec{{ID: "stuck", Tool: "execute"}},
	}, func(ctx context.Context, input ExecuteInput) ExecuteResult {
		close(started)
		<-ctx.Done()
		return ExecuteResult{Err: ctx.Err()}
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("step did not start")
	}
	final, waitTimedOut, err := service.Wait(context.Background(), record.ID, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if waitTimedOut || final.State != StateFailed {
		t.Fatalf("final=%+v waitTimedOut=%v", final, waitTimedOut)
	}
	var stepError string
	for _, step := range final.Steps {
		if step.ID == "stuck" {
			if step.State != StateFailed {
				t.Fatalf("stuck step=%+v", step)
			}
			stepError = string(step.Error)
		}
	}
	if !strings.Contains(stepError, "RUNTIME_STEP_TIMEOUT") {
		t.Fatalf("step error %q does not mention RUNTIME_STEP_TIMEOUT", stepError)
	}
	// The deadline must free the worker for the next operation.
	recovered, err := service.Submit(context.Background(), SubmitSpec{
		RemoteSessionID: "session", WorkspaceName: "workspace", RequestID: "after_timeout", Purpose: "worker recovery",
		Steps: []StepSpec{{ID: "after", Tool: "read"}},
	}, func(context.Context, ExecuteInput) ExecuteResult {
		return ExecuteResult{Result: []byte(`{"recovered":true}`)}
	})
	if err != nil {
		t.Fatal(err)
	}
	final, waitTimedOut, err = service.Wait(context.Background(), recovered.ID, 2*time.Second)
	if err != nil || waitTimedOut || final.State != StateSucceeded {
		t.Fatalf("worker did not recover after timeout: final=%+v waitTimedOut=%v err=%v", final, waitTimedOut, err)
	}
}

func TestSubmitDoesNotBlockWhenWorkersBusyAndQueueFull(t *testing.T) {
	service := newTestService(t, 1)
	release := make(chan struct{})
	started := make(chan string, 8)
	executor := func(ctx context.Context, input ExecuteInput) ExecuteResult {
		started <- input.StepID
		if input.StepID == "holder" {
			<-release
		}
		return ExecuteResult{Result: []byte(`{}`)}
	}
	holder, err := service.Submit(context.Background(), SubmitSpec{
		ID: "op_holder", RemoteSessionID: "session", WorkspaceName: "workspace", RequestID: "request", Purpose: "hold the worker",
		Steps: []StepSpec{{ID: "holder", Tool: "execute"}},
	}, executor)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("holder step did not start")
	}

	// The only worker is pinned by op_holder and the queue capacity is
	// workers*2 = 2. Extra submissions overflow into the pending set, so every
	// Submit must still return promptly instead of blocking on the queue.
	type submitOutcome struct {
		id  string
		err error
	}
	submitted := make(chan submitOutcome, 4)
	for _, id := range []string{"op_full_a", "op_full_b", "op_overflow_c", "op_overflow_d"} {
		operationID := id
		go func() {
			_, err := service.Submit(context.Background(), SubmitSpec{
				ID: operationID, RemoteSessionID: "session", WorkspaceName: "workspace", RequestID: operationID, Purpose: "queue pressure",
				Steps: []StepSpec{{ID: "step_" + operationID, Tool: "read"}},
			}, executor)
			submitted <- submitOutcome{id: operationID, err: err}
		}()
	}
	for i := 0; i < 4; i++ {
		select {
		case outcome := <-submitted:
			if outcome.err != nil {
				t.Fatalf("Submit %s failed: %v", outcome.id, outcome.err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("Submit blocked while workers were busy and the queue was full (got %d/4 back)", i)
		}
	}
	close(release)
	for _, id := range []string{holder.ID, "op_full_a", "op_full_b", "op_overflow_c", "op_overflow_d"} {
		final, waitTimedOut, err := service.Wait(context.Background(), id, 5*time.Second)
		if err != nil || waitTimedOut || final.State != StateSucceeded {
			t.Fatalf("operation %s: final=%+v waitTimedOut=%v err=%v", id, final.State, waitTimedOut, err)
		}
	}
}
