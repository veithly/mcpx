package operation

import (
	"context"
	"testing"
	"time"
)

func TestCancelCompletedOperationReturnsDurableOutcome(t *testing.T) {
	service := newTestService(t, 1)
	ctx := context.Background()
	record, err := service.Submit(ctx, SubmitSpec{RemoteSessionID: "session", WorkspaceName: "workspace", RequestID: "cancel-completed", Steps: []StepSpec{{ID: "done", Tool: "read"}}}, func(context.Context, ExecuteInput) ExecuteResult { return ExecuteResult{Result: []byte(`{"ok":true}`)} })
	if err != nil {
		t.Fatal(err)
	}
	record, timedOut, err := service.Wait(ctx, record.ID, 3*time.Second)
	if err != nil || timedOut || record.State != StateSucceeded {
		t.Fatalf("wait: %+v %v", record, err)
	}
	for range 2 {
		got, err := service.Cancel(ctx, record.ID)
		if err != nil {
			t.Fatalf("cancel raced with completion: %v", err)
		}
		if got.State != record.State || got.StateSequence != record.StateSequence || got.StateEventID != record.StateEventID {
			t.Fatalf("cancel changed completed outcome: %+v", got)
		}
	}
}
