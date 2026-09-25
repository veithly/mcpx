package envelope

import "testing"

func TestOperationFailureDoesNotReportSuccessfulStepExitCode(t *testing.T) {
	for _, failed := range []map[string]any{
		{"id": "failed", "state": "failed", "result": map[string]any{"data": map[string]any{"exit_code": 127}}},
		{"id": "failed", "state": "failed", "error": map[string]any{"message": "MATCH_AMBIGUOUS"}},
	} {
		response := Fail(StatusError, "req", "demo", map[string]any{"steps": []map[string]any{
			{"id": "ok", "state": "succeeded", "result": map[string]any{"data": map[string]any{"exit_code": 0}}}, failed,
		}}, "OPERATION_FAILED", "operation failed")
		got, present := response.Error.Details["exit_code"]
		if failed["result"] != nil {
			if got != 127 {
				t.Fatalf("failed step exit code = %v; want 127", got)
			}
		} else if present {
			t.Fatalf("non-command failure inherited exit_code=%v", got)
		}
	}
}
