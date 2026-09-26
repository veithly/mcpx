package server

import (
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/server/prompts"
)

var planToolAnnotation = toolAnnotation{
	ReadOnly: false, Destructive: false, Idempotent: true, OpenWorld: false,
	Meta: mcp.Meta{"mcpx/action_risk": map[string]any{
		"create":   riskDescriptor(false, false, true, false, "server_state_write"),
		"read":     riskDescriptor(true, false, true, false, "server_state_read"),
		"advance":  riskDescriptor(false, false, true, false, "server_state_write"),
		"complete": riskDescriptor(false, false, true, false, "server_state_write"),
		"block":    riskDescriptor(false, false, true, false, "server_state_write"),
		"replan":   riskDescriptor(false, false, true, false, "server_state_write"),
		"deliver":  riskDescriptor(false, false, true, false, "server_state_write"),
	}},
}

var artifactToolAnnotation = toolAnnotation{
	ReadOnly: false, Destructive: false, Idempotent: true, OpenWorld: false,
	Meta: mcp.Meta{"mcpx/action_risk": map[string]any{
		"register": riskDescriptor(false, false, true, false, "workspace_artifact_registration"),
		"list":     riskDescriptor(true, false, true, false, "workspace_artifact_read"),
		"read":     riskDescriptor(true, false, true, false, "workspace_artifact_read"),
	}},
}

var skillToolAnnotation = toolAnnotation{
	ReadOnly: false, Destructive: true, Idempotent: false, OpenWorld: true,
	Meta: mcp.Meta{"mcpx/action_risk": map[string]any{
		"list":     riskDescriptor(true, false, true, false, "skill_inventory_read"),
		"describe": riskDescriptor(true, false, true, false, "skill_definition_read"),
		"call":     riskDescriptor(false, true, false, true, "skill_execution_dynamic_risk"),
	}},
}

var mcpToolAnnotation = toolAnnotation{
	ReadOnly: false, Destructive: true, Idempotent: false, OpenWorld: true,
	Meta: mcp.Meta{"mcpx/action_risk": map[string]any{
		"list":     riskDescriptor(true, false, true, true, "upstream_mcp_capability_read"),
		"describe": riskDescriptor(true, false, true, true, "upstream_mcp_schema_read"),
		"call":     riskDescriptor(false, true, false, true, "upstream_mcp_dynamic_risk"),
	}},
}

var browserToolAnnotation = toolAnnotation{
	ReadOnly: false, Destructive: true, Idempotent: false, OpenWorld: true,
	Meta: mcp.Meta{"mcpx/action_risk": map[string]any{
		"status":       riskDescriptor(true, false, true, true, "local_browser_extension_status"),
		"tabs":         riskDescriptor(true, false, true, true, "local_browser_tab_metadata_read"),
		"claim":        riskDescriptor(false, false, true, true, "browser_session_tab_claim"),
		"agent_tabs":   riskDescriptor(true, false, true, true, "browser_session_tab_read"),
		"get_tab":      riskDescriptor(true, false, true, true, "browser_session_tab_read"),
		"create_tab":   riskDescriptor(false, false, false, true, "browser_tab_create"),
		"close_tab":    riskDescriptor(false, true, false, true, "browser_tab_close"),
		"navigate":     riskDescriptor(false, true, false, true, "browser_navigation_open_world"),
		"back":         riskDescriptor(false, false, false, true, "browser_navigation_history"),
		"forward":      riskDescriptor(false, false, false, true, "browser_navigation_history"),
		"reload":       riskDescriptor(false, false, false, true, "browser_navigation_reload"),
		"snapshot":     riskDescriptor(true, false, true, true, "browser_page_dom_read"),
		"click":        riskDescriptor(false, true, false, true, "browser_page_interaction"),
		"double_click": riskDescriptor(false, true, false, true, "browser_page_interaction"),
		"type":         riskDescriptor(false, true, false, true, "browser_page_input"),
		"keypress":     riskDescriptor(false, true, false, true, "browser_page_input"),
		"scroll":       riskDescriptor(false, false, false, true, "browser_page_view_change"),
		"move":         riskDescriptor(false, false, false, true, "browser_pointer_move"),
		"drag":         riskDescriptor(false, true, false, true, "browser_page_interaction"),
		"screenshot":   riskDescriptor(true, false, true, true, "browser_page_screenshot_read"),
		"official":     riskDescriptor(false, true, false, true, "browser_official_dynamic_command"),
	}},
}

func riskDescriptor(readOnly, destructive, idempotent, openWorld bool, classification string) map[string]any {
	return map[string]any{
		"read_only": readOnly, "destructive": destructive, "idempotent": idempotent,
		"open_world": openWorld, "classification": classification,
	}
}

// cleanCoreTool builds the stable clean-core contract with remote_session_id.
func cleanCoreTool(name, description string, properties map[string]any, required []string, annotation toolAnnotation) mcp.Tool {
	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	required = withoutBoundSessionRequirement(required)
	if len(required) > 0 {
		schema["required"] = required
	}
	raw, _ := json.Marshal(schema)
	return annotatedTool(mcp.Tool{Name: name, Description: description, InputSchema: json.RawMessage(raw)}, annotation)
}

func (r *Runtime) registerCleanCoreTools(s *mcp.Server) {
	desc := prompts.MustDescriptions()
	remoteSession := stringSchema("Remote Session 标识；当前 MCP transport 已绑定 Session 时省略，显式传入可覆盖绑定")
	workspace := stringSchema("已注册的 Workspace 名称")
	path := stringSchema("Workspace 内的相对文件路径")

	r.addTool(s, cleanCoreTool("workspace", desc["workspace"], map[string]any{}, nil, readOnlyToolAnnotation), r.toolWorkspace)

	r.addTool(s, cleanCoreTool("session", desc["session"], map[string]any{
		"remote_session_id":            remoteSession,
		"action":                       enumSchema("会话生命周期动作；省略时默认 open/resume，传 mode 时可省略并推导 close；remote_session_id 丢失时显式 list 发现已有会话", "open", "list", "close"),
		"workspace":                    workspace,
		"query":                        stringSchema("list 时按 label、description 或 Session ID 搜索"),
		"status":                       stringSchema("list 时按状态过滤；多个状态用逗号分隔"),
		"cursor":                       stringSchema("list 分页游标"),
		"limit":                        numberSchema("list 返回数量限制"),
		"label":                        stringSchema("会话标签"),
		"description":                  stringSchema("开发目标或会话描述"),
		"workspace_path":               stringSchema("用户明确指定的已有项目绝对路径；创建时直接注册并绑定，无需借用 mcpx 或其他会话。恢复时必须匹配原会话项目路径。"),
		"client_request_id":            stringSchema("客户端幂等键；不能在不同项目间复用"),
		"include_instructions_content": booleanSchema("是否内联返回指令内容"),
		"include_project_tasks":        booleanSchema("是否返回项目任务"),
		"mode":                         enumSchema("关闭模式；出现时省略 action 也会推导 close", "closed", "archived"),
	}, nil, sessionToolAnnotation), r.toolSession)

	r.addTool(s, cleanCoreTool("exec_command", desc["exec_command"], map[string]any{
		"cmd":               stringSchema("Shell command to execute."),
		"workdir":           stringSchema("Working directory for the command. Defaults to the Remote Session workspace root; the resolved physical directory must remain inside that workspace."),
		"shell":             stringSchema("Shell binary to launch. Defaults to the user's default shell."),
		"login":             booleanSchema("True runs the shell with -l/-i semantics; false disables them. Defaults to true."),
		"tty":               booleanSchema("True allocates a PTY for the command; false or omitted uses plain pipes."),
		"yield_time_ms":     numberSchema("Wait before yielding output. Defaults to 10000 ms; effective range is 250-30000 ms."),
		"max_output_tokens": numberSchema("Output token budget. Defaults to 10000 tokens; larger requests may be capped by policy."),
		"remote_session_id": remoteSession,
	}, []string{"cmd"}, mutatingToolAnnotation), r.toolExecCommand)

	r.addTool(s, cleanCoreTool("write_stdin", desc["write_stdin"], map[string]any{
		"session_id":        map[string]any{"type": "integer", "description": "Identifier of the running exec session returned by exec_command; distinct from remote_session_id."},
		"chars":             stringSchema("Bytes to write to stdin. Defaults to empty, which polls without writing."),
		"yield_time_ms":     numberSchema("Wait before yielding output. Non-empty writes default to 250 ms and cap at 30000 ms; empty polls wait 5000-300000 ms by default."),
		"max_output_tokens": numberSchema("Output token budget. Defaults to 10000 tokens; larger requests may be capped by policy."),
		"remote_session_id": remoteSession,
	}, []string{"session_id"}, mutatingToolAnnotation), r.toolWriteStdin)

	r.addTool(s, cleanCoreTool("apply_patch", desc["apply_patch"], map[string]any{
		"input":             stringSchema("Raw Codex patch text, beginning with *** Begin Patch and ending with *** End Patch. MCP wraps this freeform text in one input string. Paths are relative to the Remote Session workspace root."),
		"remote_session_id": remoteSession,
	}, []string{"input"}, toolAnnotation{ReadOnly: false, Destructive: true, Idempotent: false, OpenWorld: false}), r.toolApplyPatch)

	moveOutTarget := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"path":            path,
			"expected_sha256": stringSchema("可选的完整文件 SHA-256 前置条件；可通过 exec_command 读取当前哈希"),
		},
		"required": []string{"path"},
	}
	moveOutTargets := arraySchema(moveOutTarget, "明确的安全移出清单；Runtime 用不跟随 symlink 的 lstat 推导 file/directory/symlink，directory 原子移出当前目录树，symlink 只移出链接入口")
	moveOutTargets["minItems"] = 1
	moveOutTargets["maxItems"] = moveOutRequestMaxTargets
	moveOutCommon := map[string]any{
		"remote_session_id": remoteSession,
	}
	moveOutBranches := map[string]actionSchemaBranch{
		"prepare": {
			Description: "只读冻结明确的 Workspace 文件、目录或 symlink 安全移出清单；不执行文件系统移动。Workspace 和目标类型由 Runtime 从 Remote Session 与文件系统事实确定。",
			Properties: map[string]any{
				"purpose":         stringSchema("向用户展示的安全移出目的；删除/移除/清理请求必须准确描述最终移出意图"),
				"targets":         moveOutTargets,
				"idempotency_key": stringSchema("可选；需要跨请求重放同一 prepare 时复用。省略时 Runtime 为本次请求生成 key"),
			},
			Required: []string{"remote_session_id", "purpose", "targets"},
		},
		"submit": {
			Description: "提交网页端模型已向用户询问并确认的冻结 manifest；客户端只能带回服务端签发的 confirmation_uuid。",
			Properties: map[string]any{
				"confirmation_uuid": stringSchema("move_out(action=prepare) 返回的服务端生成 UUID；用户确认后原样带回"),
			},
			Required: []string{"remote_session_id", "confirmation_uuid"},
		},
	}
	r.addTool(s, cleanActionTool("move_out", desc["move_out"], moveOutCommon, moveOutBranches, workspaceMoveOutToolAnnotation), r.toolMoveOut)

	r.addTool(s, cleanCoreTool("observe", desc["observe"], map[string]any{
		"remote_session_id":  remoteSession,
		"workspace":          workspace,
		"view":               enumSchema("观察视图；省略时 Runtime 仅在目标唯一时推导，完全无目标参数时默认为 session", "session", "task", "plan", "history", "logs"),
		"limit":              numberSchema("返回数量限制"),
		"cursor":             stringSchema("分页游标"),
		"call_id":            stringSchema("按调用关联 ID 过滤 history"),
		"event_ids":          arraySchema(map[string]any{"type": "string"}, "按事件 sequence ID 过滤 history"),
		"request_ids":        arraySchema(map[string]any{"type": "string"}, "按多个请求 ID 过滤 history"),
		"operation_ids":      arraySchema(map[string]any{"type": "string"}, "按 Operation ID 过滤 history"),
		"plan_task_ids":      arraySchema(map[string]any{"type": "string"}, "按 Plan Task ID 过滤 history"),
		"execution_task_ids": arraySchema(map[string]any{"type": "string"}, "按执行 Task ID 过滤 history"),
		"keyword":            stringSchema("在摘要、用途、工具、命令、路径和输入输出中搜索 history"),
		"kinds":              arraySchema(map[string]any{"type": "string"}, "事件类型过滤，如 tool、command、skill、mcp、file_change、session、error"),
		"statuses":           arraySchema(map[string]any{"type": "string"}, "按事件状态过滤 history"),
		"created_after":      stringSchema("仅返回此时间之后的事件；支持 RFC3339、YYYY-MM-DD 或 Unix 毫秒"),
		"created_before":     stringSchema("仅返回此时间之前的事件；支持 RFC3339、YYYY-MM-DD 或 Unix 毫秒"),
		"plan_task_id":       stringSchema("Plan Task ID；view=plan 时使用，也可用于 history 过滤"),
		"execution_task_id":  stringSchema("执行 Task ID；view=task/logs 时使用，也可用于 history 过滤"),
		"stdout_offset":      numberSchema("view=logs 的 stdout 字节偏移；可原样使用服务端 next_action 返回值"),
		"stderr_offset":      numberSchema("view=logs 的 stderr 字节偏移；可原样使用服务端 next_action 返回值"),
	}, []string{"remote_session_id"}, readOnlyToolAnnotation), r.toolObserve)

	r.addTool(s, cleanCoreTool("progress", desc["progress"], map[string]any{
		"remote_session_id": remoteSession,
		"status":            enumSchema("用户可见进度或终态；普通工具调用不要求逐次 progress，任务正常完成并准备最终回复前必须发送一次 completed，等待、阻塞或失败使用对应状态", "in_progress", "completed", "waiting_for_user", "blocked", "failed"),
		"current":           stringSchema("当前阶段或终态的用户可见摘要；陈述已发生或正在发生的工作"),
		"result": map[string]any{
			"type": "array", "maxItems": maxProgressResultItems,
			"items":       stringSchema("一条可独立扫描的已验证事实"),
			"description": "已验证结果列表；每项只表达一个有证据支持的事实，不要把多个结论拼成一段",
		},
		"next":         stringSchema("下一步动作；completed 时通常留空"),
		"phase":        stringSchema("可选的阶段名称，例如 implementation、verification、release"),
		"related_tool": stringSchema("可选的相关 MCPX 工具名"),
	}, []string{"remote_session_id", "current"}, sessionToolAnnotation), r.toolProgress)

	r.registerConsolidatedToolsCatalog(s)
}
