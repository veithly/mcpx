package server

import (
	"context"
	"errors"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpx/internal/logging"
	"mcpx/internal/mcpresult"
)

const toolResponseTimeout = 45 * time.Second
const toolResponseConcurrency = 32
const toolRecoveryConcurrency = 8

// toolWaitMax caps wait-style arguments (execute yield_time_ms, operation_manage
// wait timeout_ms) below the synchronous response budget, so a single waiting
// call can never outrun its own response deadline.
const toolWaitMax = toolResponseTimeout - 5*time.Second

// toolWorkerHardLimit bounds a detached worker after the response deadline.
// Execution must outlive the client-facing budget so its final outcome is
// recorded and replayable, but a goroutine and its pool slot must not leak
// forever when a handler ignores cancellation entirely.
const toolWorkerHardLimit = 2 * toolResponseTimeout

// Bound the response independently of cancellation cooperation in a tool or
// dependency. The response deadline no longer doubles as the execution
// deadline: workers run detached and keep recording, and slots remain
// occupied until workers actually finish, not until clients give up, so
// timeouts cannot grow an unbounded set of orphan workers.
func (r *Runtime) boundedTool(name string, handler mcp.ToolHandler, timeout time.Duration) mcp.ToolHandler {
	r.toolResponseOnce.Do(func() {
		r.toolResponseSlots = make(chan struct{}, toolResponseConcurrency)
		r.toolRecoverySlots = make(chan struct{}, toolRecoveryConcurrency)
	})
	slots := r.toolResponseSlots
	switch name {
	case "observe", "operation_manage", "session", "workspace", "runtime_read", "progress":
		slots = r.toolRecoverySlots
	}
	return func(ctx context.Context, req *mcp.CallToolRequest) (result *mcp.CallToolResult, err error) {
		defer func() {
			if recover() != nil {
				result = emergencyToolFailure()
				err = nil
			}
		}()
		// A durable Operation owns its child deadline, cancellation and execution
		// identity. The external operation_manage call still has this response bound.
		if isOperationChild(ctx) {
			return handler(ctx, req)
		}
		ctx, rt := ensureRuntimeContext(ctx, mcpresult.Header(req), time.Now())
		failure := func(code, message string) *mcp.CallToolResult {
			return ensureToolResponse(ctx, name, req, toolFailure(ctx, name, req, code, message, nil), nil)
		}
		if ctx.Err() != nil {
			return failure("TOOL_CANCELLED", "Caller cancelled before this tool body was started."), nil
		}
		select {
		case slots <- struct{}{}:
		default:
			return failure("TOOL_BUSY", "The tool concurrency limit is reached; this call was not started."), nil
		}
		callCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		done := make(chan *mcp.CallToolResult, 1)
		go func() {
			// The response deadline bounds only the synchronous reply, never the
			// work itself. The worker detaches from client cancellation and from
			// that deadline, keeps the runtime identity, and runs under its own
			// hard limit: a long execution outlives the caller's TOOL_TIMEOUT and
			// its final outcome is still recorded (and replayable). Exactly one
			// of the response and the late worker outcome wins; the pool slot
			// stays occupied until the worker truly ends.
			workerCtx := withClientContext(context.WithoutCancel(ctx), ctx)
			workerCtx, workerCancel := context.WithTimeout(workerCtx, toolWorkerHardLimit)
			defer workerCancel()
			var out *mcp.CallToolResult
			var callErr error
			defer func() {
				if recover() != nil {
					out = emergencyToolFailure()
					callErr = nil
				}
				out = ensureToolResponse(workerCtx, name, req, out, callErr)
				done <- out
				<-slots
			}()
			if ctx.Err() != nil {
				// The caller was already gone before the body could start.
				callErr = ctx.Err()
				return
			}
			out, callErr = handler(workerCtx, req)
		}()
		select {
		case out := <-done:
			return out, nil
		case <-callCtx.Done():
			// Prefer a completed result if it arrived at the same boundary.
			select {
			case out := <-done:
				return out, nil
			default:
			}
			code := "TOOL_CANCELLED"
			message := "The caller cancelled this tool call. Cancellation was requested; verify any partial effects before deciding what to do next."
			if errors.Is(callCtx.Err(), context.DeadlineExceeded) {
				code = "TOOL_TIMEOUT"
				message = "The synchronous response deadline elapsed; execution continues detached and its final outcome will be recorded. An identical retry replays that recorded result instead of running twice, or inspect the recorded call/Task first; use operation_batch for long work."
			}
			logging.With("component", "mcp_tool").Warn("response deadline or cancellation", "tool", name, "request_id", rt.RequestID, "code", code)
			return failure(code, message), nil
		}
	}
}
