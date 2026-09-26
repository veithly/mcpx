package server

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/mcpresult"

	"mcpx/internal/envelope"
	"mcpx/internal/environment"
	"mcpx/internal/operation"
	"mcpx/internal/server/prompts"
)

// registerTools is the sole public tool registration point.
func (r *Runtime) registerTools(s *mcp.Server) {
	r.registerCleanCoreTools(s)
	r.captureToolIndex(s)
}

func (r *Runtime) captureToolIndex(s *mcp.Server) {
	// Official go-sdk has no ListTools snapshot API; addTool fills toolIndex.
	_ = s
}

func (r *Runtime) registeredTools() []mcp.Tool {
	r.toolIndexMu.RLock()
	defer r.toolIndexMu.RUnlock()
	tools := make([]mcp.Tool, 0, len(r.toolIndex))
	for _, tool := range r.toolIndex {
		tools = append(tools, tool)
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	return tools
}

// listedToolMap is a test/helper snapshot of the registered tool catalog.
func (r *Runtime) listedToolMap() map[string]mcp.Tool {
	r.toolIndexMu.RLock()
	defer r.toolIndexMu.RUnlock()
	out := make(map[string]mcp.Tool, len(r.toolIndex))
	for name, tool := range r.toolIndex {
		out[name] = tool
	}
	return out
}

// currentToolSchemaRevision is derived from the actual MCP registration,
// including name, description, input schema, and annotations. It deliberately
// excludes Session state so opening or handing off a session cannot refresh a
// client's tools/list cache.
func (r *Runtime) currentToolSchemaRevision() string {
	tools := r.registeredTools()
	items := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		encoded, _ := json.Marshal(tool)
		var item map[string]any
		_ = json.Unmarshal(encoded, &item)
		items = append(items, item)
	}
	return hashRevision(items)
}

// compactToolResult is the unified success-path tool result builder.
//
//	content[0].text   — human summary only
//	structuredContent — machine wire {status, data} (or an already-formed wire map)
//
// Models must consume structuredContent after ARC wrap; hosts render the text.
func compactToolResult(data any, summary string) *mcp.CallToolResult {
	if summary == "" {
		summary = "succeeded"
	}
	var wire map[string]any
	if existing, ok := asPublicWireEnvelope(data); ok {
		wire = existing
	} else {
		wire = map[string]any{
			"status": string(envelope.StatusOK),
			"data":   data,
		}
	}
	// JSON-normalize so nested slices are []any (stable for tests and hosts).
	return mcpresult.NewStructured(jsonNormalizeMap(wire), summary)
}

func jsonNormalizeMap(value map[string]any) map[string]any {
	encoded, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var normalized map[string]any
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		return value
	}
	return normalized
}

// asPublicWireEnvelope detects handler payloads already in the public wire shape
// {status, data?, error?} so success helpers do not double-wrap them.
func asPublicWireEnvelope(value any) (map[string]any, bool) {
	m, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	status, _ := m["status"].(string)
	switch status {
	case string(envelope.StatusOK), string(envelope.StatusAccepted),
		string(envelope.StatusNeedConfirmation), string(envelope.StatusInterrupted),
		string(envelope.StatusError):
		if _, hasData := m["data"]; hasData {
			return m, true
		}
		if _, hasError := m["error"]; hasError {
			return m, true
		}
	}
	return nil, false
}

type toolAnnotation struct {
	ReadOnly    bool
	Destructive bool
	Idempotent  bool
	OpenWorld   bool
	Title       string
	Meta        mcp.Meta
}

var (
	readOnlyToolAnnotation = toolAnnotation{ReadOnly: true, Destructive: false, Idempotent: true, OpenWorld: false}
	mutatingToolAnnotation = toolAnnotation{ReadOnly: false, Destructive: true, Idempotent: false, OpenWorld: true}
	sessionToolAnnotation  = toolAnnotation{ReadOnly: false, Destructive: false, Idempotent: false, OpenWorld: false}
	secretToolAnnotation   = toolAnnotation{ReadOnly: false, Destructive: false, Idempotent: false, OpenWorld: false}
)

func annotatedTool(tool mcp.Tool, annotation toolAnnotation) mcp.Tool {
	dest, open := annotation.Destructive, annotation.OpenWorld
	tool.Annotations = &mcp.ToolAnnotations{
		ReadOnlyHint:    annotation.ReadOnly,
		DestructiveHint: &dest,
		IdempotentHint:  annotation.Idempotent,
		OpenWorldHint:   &open,
		Title:           annotation.Title,
	}
	if annotation.Title != "" {
		tool.Title = annotation.Title
	}
	if annotation.Meta != nil {
		tool.Meta = annotation.Meta
	}
	return tool
}

type actionSchemaBranch struct {
	Description string
	Properties  map[string]any
	Required    []string
}

// cleanActionTool is the strict flat action schema used by the final core catalog.
// Action descriptions summarize each action's purpose and conditional required fields.
func cleanActionTool(name, description string, common map[string]any, branches map[string]actionSchemaBranch, annotation toolAnnotation) mcp.Tool {
	actions := make([]string, 0, len(branches))
	for action := range branches {
		actions = append(actions, action)
	}
	sort.Strings(actions)

	actionDescriptions := make([]string, 0, len(actions))
	rootProperties := make(map[string]any, len(common)+len(branches)+1)
	for key, value := range common {
		rootProperties[key] = value
	}

	for _, action := range actions {
		branch := branches[action]
		if branch.Description == "" {
			branch.Description = "仅执行「" + action + "」操作；失败时按返回的 next_action 继续。"
		}
		for key, value := range branch.Properties {
			if _, exists := rootProperties[key]; !exists {
				rootProperties[key] = value
			}
		}

		var reqFields []string
		for _, field := range withoutBoundSessionRequirement(branch.Required) {
			if field != "action" {
				reqFields = append(reqFields, field)
			}
		}

		desc := action + "：" + branch.Description
		if len(reqFields) > 0 {
			if !strings.HasSuffix(desc, "。") && !strings.HasSuffix(desc, "；") {
				desc += "；"
			}
			desc += "必填 " + strings.Join(reqFields, "、") + "。"
		}
		actionDescriptions = append(actionDescriptions, desc)
	}

	rootProperties["action"] = map[string]any{
		"type":        "string",
		"enum":        actions,
		"description": strings.Join(actionDescriptions, "\n"),
	}

	raw, _ := json.Marshal(map[string]any{
		"type":                 "object",
		"properties":           rootProperties,
		"required":             []string{"action"},
		"additionalProperties": false,
	})
	return annotatedTool(mcp.Tool{Name: name, Description: description, InputSchema: json.RawMessage(raw)}, annotation)
}

func withoutBoundSessionRequirement(required []string) []string {
	out := make([]string, 0, len(required))
	for _, field := range required {
		if field != "remote_session_id" {
			out = append(out, field)
		}
	}
	return out
}

func activityInputSchema() map[string]any {
	properties := map[string]any{
		"intent":     activityStringSchema("新实质工作 turn 的目标；非空即开启新 turn"),
		"hypothesis": activityStringSchema("尚未证实、可被后续证据推翻的暂定判断"),
		"evidence":   activityStringSchema("本次新观察到的可核验事实；不写推断"),
		"conclusion": activityStringSchema("由 evidence 支持的当前判断"),
		"next":       activityStringSchema("立即下一步；应与本次调用对齐"),
		"status":     activityStringSchema("仅在阶段、等待或阻塞状态发生实质变化时填写"),
	}
	return map[string]any{
		"type":                 "object",
		"description":          "可选公开 Activity；只填写本次发生变化的字段，不重复未变化内容",
		"properties":           properties,
		"additionalProperties": false,
	}
}

func activityStringSchema(description string) map[string]any {
	schema := stringSchema(description)
	schema["maxLength"] = envelope.MaxActivityBytes
	return schema
}

func withEmbeddedActivitySchema(tool mcp.Tool) mcp.Tool {
	switch tool.Name {
	case "workspace", "exec_command", "write_stdin", "apply_patch":
		return tool
	}
	if tool.InputSchema == nil {
		return tool
	}
	encoded, err := json.Marshal(tool.InputSchema)
	if err != nil {
		return tool
	}
	var schema map[string]any
	if json.Unmarshal(encoded, &schema) != nil || schema == nil {
		return tool
	}
	rootProperties, _ := schema["properties"].(map[string]any)
	if rootProperties != nil {
		if toolSupportsEmbeddedActivity(tool.Name) {
			rootProperties["activity"] = activityInputSchema()
		}
		rootProperties["acknowledge_requests"] = map[string]any{
			"type": "array", "maxItems": 16, "items": map[string]any{"type": "string"},
			"description": "已读 operator_control.requests 的原始 ID；同一 remote_session_id 的显式回执。",
		}
	}
	raw, err := json.Marshal(schema)
	if err == nil {
		tool.InputSchema = json.RawMessage(raw)
	}
	return tool
}

func stringSchema(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func promptToolDescription(descriptions map[string]string, name, fallback string) string {
	if description := descriptions[name]; description != "" {
		return description
	}
	return fallback
}

func numberSchema(description string) map[string]any {
	return map[string]any{"type": "number", "description": description}
}

func booleanSchema(description string) map[string]any {
	return map[string]any{"type": "boolean", "description": description}
}

func arraySchema(items map[string]any, description string) map[string]any {
	return map[string]any{"type": "array", "items": items, "description": description}
}

func enumSchema(description string, values ...string) map[string]any {
	return map[string]any{"type": "string", "enum": values, "description": description}
}

// registerConsolidatedToolsCatalog registers the clean-core support tools.
func (r *Runtime) registerConsolidatedToolsCatalog(s *mcp.Server) {
	toolDesc := prompts.MustDescriptions()
	remoteSession := stringSchema("Remote Session 标识；当前 MCP transport 已绑定 Session 时省略，显式传入可覆盖绑定")
	workspace := stringSchema("已注册的 Workspace 名称")
	path := stringSchema("工作区相对路径")
	supportTool := cleanCoreTool

	operationSteps := arraySchema(map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"id":         stringSchema("批次内唯一的步骤 ID"),
			"tool":       stringSchema("网关管理面工具名称；编程工具 exec_command/write_stdin/apply_patch 直接调用"),
			"arguments":  map[string]any{"type": "object", "additionalProperties": true, "description": "目标工具的业务参数"},
			"depends_on": arraySchema(map[string]any{"type": "string"}, "前置步骤 ID"),
		},
		"required": []string{"id", "tool", "arguments"},
	}, "带依赖关系的网关管理面操作")
	operationSteps["maxItems"] = operation.MaxSteps
	r.addTool(s, supportTool("operation_batch", toolDesc["operation_batch"], map[string]any{
		"run_id": stringSchema("调用方稳定运行 ID"), "operation_id": stringSchema("调用方稳定操作 ID；同 ID 同计划回读原操作，不重复执行"),
		"remote_session_id": remoteSession, "operations": operationSteps, "purpose": stringSchema("本次调用的目的；必须由用户明确提供"),
	}, []string{"remote_session_id", "purpose", "operations"}, mutatingToolAnnotation), r.toolOperationBatch)
	operationIDsSchema := arraySchema(map[string]any{"type": "string"}, "批量查询的异步操作 ID；最多 32 个，不要把 operation_manage 嵌套进 operation_batch")
	operationIDsSchema["minItems"] = 1
	operationIDsSchema["maxItems"] = operation.MaxBatchQueries
	operationManage := supportTool("operation_manage", toolDesc["operation_manage"], map[string]any{
		"remote_session_id": remoteSession,
		"operation_id":      stringSchema("单个异步操作 ID；与 operation_ids 二选一"), "operation_ids": operationIDsSchema,
		"action":  enumSchema("操作动作；单操作必填 operation_id，支持 status、wait、result、cancel、resume；批量查询必填 operation_ids，仅支持 status、result；二者互斥。", "status", "wait", "result", "cancel", "resume"),
		"step_id": stringSchema("批量操作子步骤 ID"), "timeout_ms": numberSchema("wait 最长等待毫秒数"),
		"confirmation_token": stringSchema("仅表示用户已确认同一子操作，不是认证凭据"), "cursor": stringSchema("结果分页游标"), "limit": numberSchema("结果字节或列表数量限制"),
	}, []string{"remote_session_id", "action"}, sessionToolAnnotation)

	r.addTool(s, operationManage, r.toolOperationManage)

	r.addTool(s, supportTool("runtime_read", toolDesc["runtime_read"], map[string]any{
		"remote_session_id": remoteSession, "workspace": workspace, "view": enumSchema("读取视图；省略时默认 capabilities，anchor_path/paths 出现时推导 instructions", "capabilities", "project", "instructions"),
		"anchor_path": stringSchema("指令锚点路径；出现时可省略 view"), "paths": arraySchema(map[string]any{"type": "string"}, "指令路径；出现时可省略 view"),
	}, nil, readOnlyToolAnnotation), r.toolRuntimeRead)
	r.addTool(s, supportTool("environment_read", toolDesc["environment_read"], map[string]any{
		"remote_session_id": remoteSession, "workspace": workspace, "view": enumSchema("读取视图；省略时 snapshot_id 推导 compare，否则默认 current", "current", "compare"),
		"sections": arraySchema(map[string]any{"type": "string", "enum": environment.ValidSections}, "环境分区"), "snapshot_id": stringSchema("比较用快照 ID；出现时可省略 view"),
	}, nil, readOnlyToolAnnotation), r.toolEnvironmentRead)
	r.addTool(s, supportTool("environment", toolDesc["environment"], map[string]any{
		"remote_session_id": remoteSession,
		"sections":          arraySchema(map[string]any{"type": "string", "enum": environment.ValidSections}, "环境分区"),
	}, []string{"remote_session_id"}, sessionToolAnnotation), r.toolEnvironment)

	planCommon := map[string]any{
		"remote_session_id": remoteSession, "purpose": stringSchema("本次计划操作的用户目标"),
		"idempotency_key": stringSchema("同一计划写操作重试时复用的幂等键"), "execution_mode": enumSchema("执行模式", "sync", "async"),
	}
	planBranches := map[string]actionSchemaBranch{
		"create":   {Properties: map[string]any{"summary": stringSchema("计划摘要"), "tasks": arraySchema(planTaskInputSchema(), "有序计划任务")}, Required: []string{"remote_session_id", "purpose", "tasks"}},
		"read":     {Properties: map[string]any{"plan_id": stringSchema("服务端返回的 Plan ID")}, Required: []string{"remote_session_id", "plan_id"}},
		"advance":  {Properties: map[string]any{"plan_id": stringSchema("Plan ID"), "plan_task_id": stringSchema("服务端返回的正式 Plan Task ID")}, Required: []string{"remote_session_id", "purpose", "plan_id", "plan_task_id"}},
		"complete": {Properties: map[string]any{"plan_id": stringSchema("Plan ID"), "plan_task_id": stringSchema("服务端返回的正式 Plan Task ID"), "evidence": arraySchema(planEvidenceSchema(), "完成任务所需证据")}, Required: []string{"remote_session_id", "purpose", "plan_id", "plan_task_id", "evidence"}},
		"block":    {Properties: map[string]any{"plan_id": stringSchema("Plan ID"), "plan_task_id": stringSchema("服务端返回的正式 Plan Task ID"), "reason": stringSchema("阻塞原因"), "evidence": arraySchema(planEvidenceSchema(), "已获得证据")}, Required: []string{"remote_session_id", "purpose", "plan_id", "plan_task_id", "reason"}},
		"replan":   {Properties: map[string]any{"plan_id": stringSchema("Plan ID"), "summary": stringSchema("新的计划摘要"), "reason": stringSchema("重新规划原因"), "operations": arraySchema(planOperationSchema(), "新增、更新或移除任务")}, Required: []string{"remote_session_id", "purpose", "plan_id", "reason", "operations"}},
		"deliver":  {Properties: map[string]any{"plan_id": stringSchema("Plan ID")}, Required: []string{"remote_session_id", "purpose", "plan_id"}},
	}
	r.addTool(s, cleanActionTool("plan", toolDesc["plan"], planCommon, planBranches, planToolAnnotation), r.toolPlanClean)

	artifactCommon := map[string]any{
		"remote_session_id": remoteSession,
		"purpose":           stringSchema("本次产物操作的用户目标"),
		"idempotency_key":   stringSchema("同一登记操作重试时复用的幂等键"),
		"execution_mode":    enumSchema("执行模式", "sync", "async"),
		"kind":              enumSchema("产物类型；register 时指定产物类型，list 时按类型过滤", "test_report", "coverage", "build", "screenshot", "log", "other"),
		"limit":             numberSchema("数量或字节限制；list 时为返回数量，read 时为字节数量"),
	}
	artifactBranches := map[string]actionSchemaBranch{
		"register": {Properties: map[string]any{"path": path, "name": stringSchema("显示名称"), "mime_type": stringSchema("MIME 类型")}, Required: []string{"remote_session_id", "purpose", "path"}},
		"list":     {Properties: map[string]any{}, Required: []string{"remote_session_id"}},
		"read":     {Properties: map[string]any{"artifact_id": stringSchema("服务端返回的 Artifact ID"), "offset": numberSchema("字节偏移")}, Required: []string{"remote_session_id", "artifact_id"}},
	}
	r.addTool(s, cleanActionTool("artifact", toolDesc["artifact"], artifactCommon, artifactBranches, artifactToolAnnotation), r.toolArtifactClean)

	skillCommon := map[string]any{
		"remote_session_id": remoteSession,
		"query":             stringSchema("按关键词筛选 Skill"),
		"name":              stringSchema("Skill 名称"),
		"purpose":           stringSchema("调用 Skill 的用户目标"),
		"arguments":         map[string]any{"type": "object", "additionalProperties": true},
		"idempotency_key":   stringSchema("同一调用重试时复用的幂等键"),
		"execution_mode":    enumSchema("执行模式", "sync", "async"),
	}
	skillBranches := map[string]actionSchemaBranch{
		"list":     {Description: "列出或搜索当前 Session 可用 Skill。", Required: []string{"remote_session_id"}},
		"describe": {Description: "读取某个 Skill 的详细能力、instructions 与参数 schema。", Required: []string{"remote_session_id", "name"}},
		"call":     {Description: "调用 Skill；Runtime 负责 revision 与参数校验。", Required: []string{"remote_session_id", "purpose", "name"}},
	}
	r.addTool(s, cleanActionTool("skill_tool", toolDesc["skill_tool"], skillCommon, skillBranches, skillToolAnnotation), r.toolSkillTool)

	mcpCommon := map[string]any{
		"remote_session_id": remoteSession,
		"query":             stringSchema("按关键词筛选 MCP Server"),
		"server":            stringSchema("MCP Server 名称"),
		"tool":              stringSchema("上游 MCP Tool 名称"),
		"purpose":           stringSchema("调用上游 MCP 的用户目标"),
		"arguments":         map[string]any{"type": "object", "additionalProperties": true},
		"idempotency_key":   stringSchema("同一调用重试时复用的幂等键"),
		"execution_mode":    enumSchema("执行模式", "sync", "async"),
	}
	mcpBranches := map[string]actionSchemaBranch{
		"list":     {Description: "列出 MCP Servers；提供 server 时列出该 Server 的 Tools。", Required: []string{"remote_session_id"}},
		"describe": {Description: "读取某个 MCP Tool 的完整 input schema。", Required: []string{"remote_session_id", "server", "tool"}},
		"call":     {Description: "调用上游 MCP Tool；Runtime 负责当前 schema 与参数校验。", Required: []string{"remote_session_id", "purpose", "server", "tool"}},
	}
	r.addTool(s, cleanActionTool("mcp_tool", toolDesc["mcp_tool"], mcpCommon, mcpBranches, mcpToolAnnotation), r.toolMCPTool)

	browserTabID := stringSchema("Browser Service 返回的标签页 ID；用户现有标签页先用 tabs 获取，操作前通常先 claim")
	browserTimeout := map[string]any{"type": "integer", "minimum": 0, "maximum": 60000, "description": "浏览器动作超时（毫秒）；click/double_click 仅 node_id 模式支持"}
	browserKeys := arraySchema(stringSchema("按键名，如 Control、Shift、Enter、A"), "同时按下或发送的按键序列")
	browserKeys["minItems"] = 1
	browserPath := arraySchema(map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{"x": map[string]any{"type": "number"}, "y": map[string]any{"type": "number"}},
		"required":   []string{"x", "y"},
	}, "拖拽路径坐标")
	browserPath["minItems"] = 1
	browserOfficialCommand := map[string]any{
		"type": "object", "additionalProperties": true,
		"description": "官方 Browser Service handleRpc(execute) 命令对象；用于当前强类型动作未覆盖的官方 Playwright/AX/WebMCP/history/export/dialog 等能力。仍经过 Browser Service 的 origin/site-status/confirmation 等安全检查；不得设置 browser_id，list_browsers 请使用 status。",
		"properties":  map[string]any{"type": stringSchema("官方 Browser Service execute command type")},
		"required":    []string{"type"},
	}
	browserCommon := map[string]any{
		"remote_session_id":   remoteSession,
		"browser_instance_id": stringSchema("OpenAI 官方扩展实例 ID；多个浏览器实例并存时，除 status/tabs 外必须明确指定"),
		"purpose":             stringSchema("浏览器操作的真实用户目标和预期副作用；status/tabs 可省略"),
		"user_confirmed":      booleanSchema("仅在上一响应明确要求 Browser Service 权限确认且用户已确认后设为 true；服务端仍校验同一动作与官方权限提示"),
	}
	browserBranches := map[string]actionSchemaBranch{
		"status":     {Description: "发现并验证本机 OpenAI 官方 ChatGPT/Codex 浏览器扩展连接。", Required: []string{"remote_session_id"}},
		"tabs":       {Description: "读取官方扩展可见的当前用户浏览器标签页元数据；复用现有浏览器与登录态。", Required: []string{"remote_session_id"}},
		"claim":      {Description: "把用户现有标签页纳入当前 Browser Service 会话；不改变页面内容。", Properties: map[string]any{"tab_id": browserTabID}, Required: []string{"remote_session_id", "purpose", "tab_id"}},
		"agent_tabs": {Description: "列出当前 Browser Service 会话已创建或已 claim 的标签页。", Required: []string{"remote_session_id", "purpose"}},
		"get_tab":    {Description: "读取会话内单个标签页当前 title/url。", Properties: map[string]any{"tab_id": browserTabID}, Required: []string{"remote_session_id", "purpose", "tab_id"}},
		"create_tab": {Description: "通过官方扩展创建一个新的受控标签页。", Required: []string{"remote_session_id", "purpose"}},
		"close_tab":  {Description: "关闭指定受控标签页。", Properties: map[string]any{"tab_id": browserTabID}, Required: []string{"remote_session_id", "purpose", "tab_id"}},
		"navigate": {Description: "通过官方 Browser Service 导航到完整 http/https URL；保留其 URL、站点状态和 origin 权限检查。", Properties: map[string]any{
			"tab_id": browserTabID, "url": stringSchema("完整 http:// 或 https:// URL"), "timeout_ms": browserTimeout,
		}, Required: []string{"remote_session_id", "purpose", "tab_id", "url"}},
		"back":    {Description: "后退当前标签页历史记录。", Properties: map[string]any{"tab_id": browserTabID, "timeout_ms": browserTimeout}, Required: []string{"remote_session_id", "purpose", "tab_id"}},
		"forward": {Description: "前进当前标签页历史记录。", Properties: map[string]any{"tab_id": browserTabID, "timeout_ms": browserTimeout}, Required: []string{"remote_session_id", "purpose", "tab_id"}},
		"reload":  {Description: "重新加载当前标签页。", Properties: map[string]any{"tab_id": browserTabID, "timeout_ms": browserTimeout}, Required: []string{"remote_session_id", "purpose", "tab_id"}},
		"snapshot": {Description: "读取官方 DOM-CUA 可见页面结构并返回稳定 node_id；后续 click/scroll 可直接使用 node_id。", Properties: map[string]any{
			"tab_id": browserTabID,
		}, Required: []string{"remote_session_id", "purpose", "tab_id"}},
		"click": {Description: "点击 DOM-CUA node_id；没有 node_id 时可使用页面坐标 x/y。timeout_ms 仅 node_id 模式生效。", Properties: map[string]any{
			"tab_id": browserTabID, "node_id": stringSchema("最近一次 snapshot 返回的 DOM-CUA node_id"), "x": map[string]any{"type": "number"}, "y": map[string]any{"type": "number"},
			"button": map[string]any{"type": "integer", "enum": []int{1, 2, 3}, "description": "鼠标按钮：1 左键、2 中键、3 右键"}, "keys": browserKeys, "timeout_ms": browserTimeout,
		}, Required: []string{"remote_session_id", "purpose", "tab_id"}},
		"double_click": {Description: "双击 DOM-CUA node_id；没有 node_id 时可使用页面坐标 x/y。timeout_ms 仅 node_id 模式生效。", Properties: map[string]any{
			"tab_id": browserTabID, "node_id": stringSchema("最近一次 snapshot 返回的 DOM-CUA node_id"), "x": map[string]any{"type": "number"}, "y": map[string]any{"type": "number"}, "keys": browserKeys, "timeout_ms": browserTimeout,
		}, Required: []string{"remote_session_id", "purpose", "tab_id"}},
		"type": {Description: "向当前聚焦输入目标键入文本；由官方 Browser Service 处理剪贴板/富文本与输入防护。", Properties: map[string]any{
			"tab_id": browserTabID, "text": stringSchema("要输入的文本"),
		}, Required: []string{"remote_session_id", "purpose", "tab_id", "text"}},
		"keypress": {Description: "向当前聚焦目标发送按键或组合键。", Properties: map[string]any{
			"tab_id": browserTabID, "keys": browserKeys,
		}, Required: []string{"remote_session_id", "purpose", "tab_id", "keys"}},
		"scroll": {Description: "滚动指定 DOM 节点或页面中心；提供 x/y 时改为坐标滚动。", Properties: map[string]any{
			"tab_id": browserTabID, "node_id": stringSchema("可选 DOM-CUA node_id"), "x": map[string]any{"type": "number"}, "y": map[string]any{"type": "number"},
			"scroll_x": map[string]any{"type": "number", "description": "水平滚动量"}, "scroll_y": map[string]any{"type": "number", "description": "垂直滚动量"}, "keys": browserKeys,
		}, Required: []string{"remote_session_id", "purpose", "tab_id", "scroll_x", "scroll_y"}},
		"move": {Description: "移动鼠标到页面坐标。", Properties: map[string]any{
			"tab_id": browserTabID, "x": map[string]any{"type": "number"}, "y": map[string]any{"type": "number"}, "keys": browserKeys,
		}, Required: []string{"remote_session_id", "purpose", "tab_id", "x", "y"}},
		"drag": {Description: "沿给定页面坐标路径拖拽。", Properties: map[string]any{
			"tab_id": browserTabID, "path": browserPath, "keys": browserKeys,
		}, Required: []string{"remote_session_id", "purpose", "tab_id", "path"}},
		"screenshot": {Description: "通过官方 Browser Service 截取当前页面视口、整页或裁剪区域，返回 base64 图像数据。", Properties: map[string]any{
			"tab_id": browserTabID, "full_page": booleanSchema("是否截取整页"),
			"crop_x": map[string]any{"type": "number"}, "crop_y": map[string]any{"type": "number"}, "crop_width": map[string]any{"type": "number", "exclusiveMinimum": 0}, "crop_height": map[string]any{"type": "number", "exclusiveMinimum": 0},
		}, Required: []string{"remote_session_id", "purpose", "tab_id"}},
		"official": {Description: "高级入口：把一个官方 Browser Service execute command 交给同一 Browser Service 安全策略层执行。仅在强类型动作无法表达官方能力时使用；不会直接调用 extension-host/raw CDP。", Properties: map[string]any{
			"command": browserOfficialCommand,
		}, Required: []string{"remote_session_id", "purpose", "command"}},
	}
	r.addTool(s, cleanActionTool("browser", toolDesc["browser"], browserCommon, browserBranches, browserToolAnnotation), r.toolBrowser)

	r.addTool(s, supportTool("screenshot_capture", toolDesc["screenshot_capture"], map[string]any{
		"remote_session_id": remoteSession, "purpose": stringSchema("截取屏幕的用户目标和范围"),
		"mode": stringSchema("全屏或区域"), "display": map[string]any{"type": "integer", "minimum": 0, "description": "显示器索引"},
		"x": map[string]any{"type": "integer", "description": "区域 X"}, "y": map[string]any{"type": "integer", "description": "区域 Y"}, "width": map[string]any{"type": "integer", "minimum": 0, "description": "宽度"}, "height": map[string]any{"type": "integer", "minimum": 0, "description": "高度"},
		"compression": stringSchema("压缩模式"), "format": stringSchema("png 或 jpeg"), "quality": map[string]any{"type": "integer", "minimum": 0, "maximum": 100, "description": "JPEG 质量"},
		"max_width": map[string]any{"type": "integer", "minimum": 0, "maximum": 16384, "description": "输出宽度上限"}, "max_height": map[string]any{"type": "integer", "minimum": 0, "maximum": 16384, "description": "输出高度上限"},
	}, []string{"remote_session_id", "purpose"}, readOnlyToolAnnotation), r.toolScreenshotCapture)
	r.addTool(s, supportTool("secret_provide", toolDesc["secret_provide"], map[string]any{
		"remote_session_id": remoteSession, "purpose": stringSchema("向当前会话提供 Secret 的用户目标"), "secret_id": stringSchema("Secret ID"),
		"values": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}, "description": "Secret 名称和值"},
	}, []string{"remote_session_id", "purpose"}, secretToolAnnotation), r.toolSecretsProvide)

	r.registerResources(s)
}

func (r *Runtime) registerResources(s *mcp.Server) {
	s.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "mcpx://remote-sessions/{remote_session_id}/artifacts/{artifact_id}",
		Name:        "Remote Session 产物",
		Description: "读取已注册的 MCPX 开发产物",
	}, r.resourceArtifact)
	s.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "mcpx://remote-sessions/{remote_session_id}/tasks/{execution_task_id}/logs",
		Name:        "终端 Task 日志",
		Description: "读取 MCPX 终端 Task 的完整日志",
		MIMEType:    "text/plain",
	}, r.resourceTaskLogs)
}

func mustSchemaJSON(schema map[string]any) json.RawMessage {
	raw, err := json.Marshal(schema)
	if err != nil {
		return json.RawMessage(`{"type":"object"}`)
	}
	return raw
}
