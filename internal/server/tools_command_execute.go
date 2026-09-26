package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/config"
	"mcpx/internal/envelope"
	"mcpx/internal/remotesession"
	"mcpx/internal/terminal"
)

// toolObserveTask reads retained task history. It cannot start, attach to,
// stop, or write input to the retired model execution backend.
func (r *Runtime) toolObserveTask(ctx context.Context, req *mcp.CallToolRequest, view string) (*mcp.CallToolResult, error) {
	ctx = withCleanCoreRequest(ctx)
	envReq, _, remote, fail := r.changeRequest(ctx, req, false)
	if fail != nil {
		return fail, nil
	}
	taskID := strings.TrimSpace(stringPayload(envReq.Payload, "execution_task_id"))
	if taskID == "" {
		return r.terminalErrorForContext(ctx, envReq, remote.ID, remote.WorkspaceName, "EXECUTION_TASK_ID_REQUIRED", "execution_task_id is required for observe(view="+view+")")
	}
	if strings.HasPrefix(taskID, "pt_") || strings.HasPrefix(taskID, "pl_") {
		return r.terminalErrorForContext(ctx, envReq, remote.ID, remote.WorkspaceName, "EXECUTION_TASK_ID_INVALID", "execution_task_id must identify a terminal execution Task; use observe(view=plan) for plan_task_id")
	}
	task, err := r.tasks.Get(remote.ID, taskID)
	if err != nil {
		return r.terminalErrorForContext(ctx, envReq, remote.ID, remote.WorkspaceName, "EXECUTION_TASK_NOT_FOUND", "execution_task_id does not belong to this Remote Session")
	}
	switch view {
	case "status":
		data := task.StatusView()
		code, message := annotateExecutionOutcome(data)
		if code == "EXECUTION_INTERRUPTED" {
			return r.executionOutcomeFailure(envReq, remote, data, code, message)
		}
		return r.remoteResult(envReq, remote.ID, remote.WorkspaceName, data)
	case "logs":
		data := r.taskResultData(task, intPayload(envReq.Payload, "stdout_offset"), intPayload(envReq.Payload, "stderr_offset"))
		capTaskExecutionOutput(data, config.MaxResultBytes(r.cfg.Limits))
		setTaskLogContinuation(data, remote.ID)
		return compactToolResult(data, commandOutputText(ctx, data, fmt.Sprintf("Task %s log chunk returned.", task.ID))), nil
	default:
		return r.invalidAction(ctx, req, "observe", view)
	}
}

// observationArguments passes the current tool contract to observation. The
// retired execute(runtime/script) redaction and argument rewriting are gone.
func observationArguments(_ string, args map[string]any) map[string]any { return args }

func (r *Runtime) executionOutcomeFailure(envReq envelope.Request, remote remotesession.Session, data map[string]any, code, message string) (*mcp.CallToolResult, error) {
	if code == "EXECUTION_INTERRUPTED" {
		data["next_action"] = nextActionWithReason("exec_command", "该历史 Task 因 MCPX 重启而中断；确认现场后再执行原命令", map[string]any{
			"remote_session_id": remote.ID, "cmd": data["command"],
		})
	}
	response := envelope.Fail(envelope.StatusError, envReq.RequestID, remote.WorkspaceName, data, code, message)
	response.RemoteSessionID = remote.ID
	return r.resultJSON(response)
}

func commandOutputText(_ context.Context, data map[string]any, summary string) string {
	var builder strings.Builder
	builder.WriteString(summary)
	if taskID, _ := data["execution_task_id"].(string); taskID != "" {
		fmt.Fprintf(&builder, "\nTask %s: status=%v, outcome=%v", taskID, data["status"], data["outcome"])
		if exitCode, ok := data["exit_code"]; ok && exitCode != nil {
			fmt.Fprintf(&builder, ", exit_code=%v", exitCode)
		}
		stdout, _ := data["stdout"].(string)
		stderr, _ := data["stderr"].(string)
		if stdout == "" && stderr == "" {
			builder.WriteString("\nNo stdout/stderr in this chunk. Use the task outcome and exit code; a command may write its build log to a separate file.")
		}
	}
	for _, stream := range []string{"stdout", "stderr"} {
		text, _ := data[stream].(string)
		if text == "" {
			continue
		}
		builder.WriteString("\n\n")
		builder.WriteString(stream)
		builder.WriteString(":\n")
		builder.WriteString(text)
	}
	if truncated, _ := data["output_truncated"].(bool); truncated {
		builder.WriteString("\n\n")
		builder.WriteString("Output truncated; call observe(view=logs) to read more.")
	}
	return builder.String()
}

// capTaskExecutionOutput bounds retained history and advances continuation
// offsets only by bytes actually delivered to the caller.
func capTaskExecutionOutput(data map[string]any, maxBytes int) {
	const hardCap = 256 << 10
	if maxBytes <= 0 || maxBytes > hardCap {
		maxBytes = hardCap
	}
	for _, stream := range []string{"stdout", "stderr"} {
		value, _ := data[stream].(string)
		trimmed, truncated := TruncateUTF8(value, maxBytes)
		if !truncated {
			continue
		}
		data[stream] = trimmed
		data[stream+"_truncated"] = true
		offset, _ := data[stream+"_offset"].(int)
		data[stream+"_next_offset"] = offset + len(trimmed)
		data["output_truncated"] = true
	}
}

func commandFailureCode(exitCode int, _ string) string {
	// POSIX shells reserve 127 for command lookup failure. Do not inspect the
	// shell's diagnostic text here: modern Bash localizes that message, so an
	// English substring check makes protocol error taxonomy locale-dependent.
	if exitCode == 127 {
		return "COMMAND_NOT_FOUND"
	}
	return "PROCESS_EXIT"
}

func commandFailureMessage(code string, exitCode int) string {
	if code == "COMMAND_NOT_FOUND" {
		return fmt.Sprintf("command executable was not found (exit code %d)", exitCode)
	}
	return fmt.Sprintf("command exited with code %d", exitCode)
}

func (r *Runtime) taskResultData(task *terminal.Task, stdoutOffset, stderrOffset int) map[string]any {
	stdout, stdoutNext := task.LogsFor("stdout", stdoutOffset)
	stderr, stderrNext := task.LogsFor("stderr", stderrOffset)
	data := task.StatusView()
	data["execution_task_id"] = task.ID
	data["stdout"] = stdout
	data["stderr"] = stderr
	data["stdout_offset"] = stdoutOffset
	data["stderr_offset"] = stderrOffset
	data["stdout_next_offset"] = stdoutNext
	data["stderr_next_offset"] = stderrNext
	if int64(stdoutNext) < task.LogStreamSize("stdout") || int64(stderrNext) < task.LogStreamSize("stderr") {
		data["output_truncated"] = true
	}
	annotateExecutionOutcome(data)
	return data
}

func setTaskLogContinuation(data map[string]any, remoteID string) {
	if truncated, _ := data["output_truncated"].(bool); !truncated {
		return
	}
	data["next_action"] = nextAction("observe", map[string]any{
		"remote_session_id": remoteID, "view": "logs", "execution_task_id": data["execution_task_id"],
		"stdout_offset": data["stdout_next_offset"], "stderr_offset": data["stderr_next_offset"],
	})
}

func annotateExecutionOutcome(data map[string]any) (code, message string) {
	status := strings.TrimSpace(fmt.Sprint(data["status"]))
	if status == string(terminal.TaskRunning) {
		data["outcome"] = "running"
		delete(data, "error_code")
		return "", ""
	}
	if reason, _ := data["limit_reason"].(string); strings.TrimSpace(reason) != "" {
		data["outcome"] = "error"
		data["error_code"] = "RUNTIME_LIMIT_EXCEEDED"
		return "RUNTIME_LIMIT_EXCEEDED", fmt.Sprintf("execution exceeded %s", reason)
	}
	if status == string(terminal.TaskInterrupted) {
		// The Task was left running by a previous MCPX process: its final exit
		// status is unknowable, so callers must never read it as a success.
		data["outcome"] = "interrupted"
		data["error_code"] = "EXECUTION_INTERRUPTED"
		return "EXECUTION_INTERRUPTED", "MCPX restarted while this command was still running; its final exit status is unknown. Verify the current workspace state, then re-run the command if its effects must be guaranteed."
	}
	if status == string(terminal.TaskKilled) {
		data["outcome"] = "stopped"
		delete(data, "error_code")
		return "", ""
	}
	if exitCode, ok := data["exit_code"].(int); ok {
		if exitCode != 0 {
			stderr, _ := data["stderr"].(string)
			code := commandFailureCode(exitCode, stderr)
			data["outcome"] = "error"
			data["error_code"] = code
			return code, commandFailureMessage(code, exitCode)
		}
		data["outcome"] = "succeeded"
		delete(data, "error_code")
		return "", ""
	}
	if status == "" {
		status = "unknown"
	}
	data["outcome"] = status
	delete(data, "error_code")
	return "", ""
}
