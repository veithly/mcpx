package observation

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
)

func TestActionColorUsesToolAndErrorOverride(t *testing.T) {
	for _, test := range []struct {
		tool  string
		color string
	}{
		{tool: "execute", color: ansiAmber},
		{tool: "read", color: ansiCyan},
		{tool: "edit", color: ansiGreen},
		{tool: "plan", color: ansiYellow},
		{tool: "session", color: ansiMagenta},
	} {
		if got := actionColor(test.tool, false, ColorModeANSI16); got != test.color {
			t.Fatalf("%s color=%q, want %q", test.tool, got, test.color)
		}
	}
	if got := actionColor("file_read", true, ColorModeANSI16); got != ansiRed {
		t.Fatalf("error color=%q", got)
	}
	if got := actionColor("read", false, ColorModeTrueColor); got != ansiTrueCyan {
		t.Fatalf("truecolor read=%q, want %q", got, ansiTrueCyan)
	}
	if got := mutedColor(ColorModeTrueColor); got != ansiTrueMuted {
		t.Fatalf("truecolor muted=%q, want %q", got, ansiTrueMuted)
	}
}

func TestEventStatusUsesSemanticMarkerAndColor(t *testing.T) {
	confirmation := Event{Tool: "execute", Status: "waiting_confirmation"}
	if eventMarker(confirmation) != "?" || eventActionColor(confirmation, ansiGreen, ColorModeANSI16) != ansiYellow {
		t.Fatalf("confirmation style marker=%q color=%q", eventMarker(confirmation), eventActionColor(confirmation, ansiGreen, ColorModeANSI16))
	}
	failed := Event{Tool: "file_read", Status: "failed"}
	if eventMarker(failed) != "!" || eventActionColor(failed, ansiBlue, ColorModeANSI16) != ansiRed {
		t.Fatalf("failure style marker=%q color=%q", eventMarker(failed), eventActionColor(failed, ansiBlue, ColorModeANSI16))
	}
}

func TestDiffLineStyleUsesTrueColorAndFallback(t *testing.T) {
	added := diffLineStyle("+new", ColorModeTrueColor)
	if !strings.Contains(added, ansiDiffAddedBackground) || !strings.Contains(added, ansiDiffAddedForeground) || !strings.HasSuffix(added, ansiReset) {
		t.Fatalf("added style=%q", added)
	}
	removed := diffLineStyle("-old", ColorModeTrueColor)
	if !strings.Contains(removed, ansiDiffRemovedBackground) || !strings.Contains(removed, ansiDiffRemovedForeground) {
		t.Fatalf("removed style=%q", removed)
	}
	for _, line := range []string{"+++ b/demo.go", "--- a/demo.go"} {
		styled := diffLineStyle(line, ColorModeTrueColor)
		if strings.Contains(styled, "48;2;") {
			t.Fatalf("header has content background=%q", styled)
		}
	}
	if styled := diffLineStyle("+new", ColorModeANSI16); strings.Contains(styled, "48;2;") {
		t.Fatalf("16-color style has truecolor background=%q", styled)
	}
	if styled := diffLineStyle("+new", ColorModeNone); styled != "+new" {
		t.Fatalf("no-color style=%q", styled)
	}
	styled := formatDiffLine("+new", ColorModeTrueColor, 12)
	if displayWidth(styled) != 4 || !strings.Contains(styled, ansiDiffAddedBackground) {
		t.Fatalf("content diff line=%q width=%d", styled, displayWidth(styled))
	}
	blank := formatDiffLine("+", ColorModeTrueColor, 12)
	if strings.Contains(blank, ansiDiffAddedBackground) || displayWidth(blank) != 1 {
		t.Fatalf("blank diff line should not become a background block: %q", blank)
	}
}

func TestHumanTextSummarizesErrorWithoutProtocolDiagnostics(t *testing.T) {
	got := humanText(`{"ok":false,"status":"error","completed_at_ms":0,"network_latency_ms":0,"processing_ms":0,"received_at_ms":0,"server_elapsed_ms":0,"started_at_ms":0,"remote_session_id":"rs_test","request_id":"req_test","error":{"code":"REVISION_REQUIRED","message":"expected_sha256 required for delete"}}`)
	want := "REVISION_REQUIRED: expected_sha256 required for delete"
	if got != want {
		t.Fatalf("error summary=%q, want %q", got, want)
	}
	for _, diagnostic := range []string{"completed_at_ms", "remote_session_id", "request_id", "server_elapsed_ms"} {
		if strings.Contains(got, diagnostic) {
			t.Fatalf("protocol diagnostic %q leaked into %q", diagnostic, got)
		}
	}
}

func TestUnifiedExtensionToolRenderingUsesConcreteTarget(t *testing.T) {
	verb, label := toolAction("skill_tool", []byte(`{"action":"call","remote_session_id":"rs_hidden","name":"brainstorming"}`))
	if verb != "Called" || label != "brainstorming" {
		t.Fatalf("skill_tool rendering verb=%q label=%q", verb, label)
	}
	verb, label = toolAction("mcp_tool", []byte(`{"action":"call","remote_session_id":"rs_hidden","server":"dbx","tool":"query"}`))
	if verb != "Called" || label != "dbx/query" {
		t.Fatalf("mcp_tool rendering verb=%q label=%q", verb, label)
	}
}

func TestRenderTextShowsNestedFailureWithoutProtocolDiagnostics(t *testing.T) {
	var output bytes.Buffer
	if err := RenderText(&output, Event{
		Tool:   "edit",
		Type:   TypeToolCompleted,
		Input:  []byte(`{"purpose":"update file"}`),
		Output: []byte(`{"status":"ok","result":{"content":[{"type":"text","text":"{\"ok\":false,\"status\":\"error\",\"completed_at_ms\":0,\"remote_session_id\":\"rs_test\",\"request_id\":\"req_test\",\"error\":{\"code\":\"REVISION_REQUIRED\",\"message\":\"base_sha256 required for update\"}}"}]}}`),
	}, false); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if !strings.Contains(text, "failed: REVISION_REQUIRED: base_sha256 required for update") {
		t.Fatalf("nested failure summary missing: %s", text)
	}
	for _, diagnostic := range []string{"completed_at_ms", "remote_session_id", "request_id"} {
		if strings.Contains(text, diagnostic) {
			t.Fatalf("protocol diagnostic %q leaked: %s", diagnostic, text)
		}
	}
}

func TestRenderTextDoesNotTreatNullErrorAsFailure(t *testing.T) {
	var output bytes.Buffer
	if err := RenderText(&output, Event{
		Tool:   "environment_inspect",
		Type:   TypeToolCompleted,
		Output: []byte(`{"status":"ok","result":{"content":[{"type":"text","text":"{\"ok\":true,\"status\":\"ok\",\"error\":null,\"data\":{\"os\":{}}}"}]}}`),
	}, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "failed:") {
		t.Fatalf("successful response with null error was rendered as failure: %q", output.String())
	}
}

func TestRenderTextPreservesPlainMCPErrorMessage(t *testing.T) {
	var output bytes.Buffer
	if err := RenderText(&output, Event{
		Tool:   "environment_inspect",
		Type:   TypeToolCompleted,
		Output: []byte(`{"status":"error","result":{"content":[{"type":"text","text":"environment inspection failed: permission denied"}]}}`),
	}, false); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); !strings.Contains(got, "failed: environment inspection failed: permission denied") {
		t.Fatalf("plain MCP error was hidden: %q", got)
	}
}

func TestRenderTextSkipsEmptyFailureHeadersAndKeepsUsefulContext(t *testing.T) {
	var output bytes.Buffer
	if err := RenderText(&output, Event{
		Tool:   "edit",
		Type:   TypeToolCompleted,
		Input:  []byte(`{"purpose":"update files"}`),
		Output: []byte(`{"status":"failed","summary":"Context:\nMATCH_AMBIGUOUS: replacement matched 3 locations\npath: flux-boot-ui/src/store/modules/consultComm.ts\nhint: use a more specific replacement anchor\nignored fourth line"}`),
	}, false); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	for _, want := range []string{
		"! Edit failed edit",
		"failed: MATCH_AMBIGUOUS: replacement matched 3 locations",
		"path: flux-boot-ui/src/store/modules/consultComm.ts",
		"hint: use a more specific replacement anchor",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("failure detail missing %q: %q", want, got)
		}
	}
	for _, forbidden := range []string{"failed: Context:", "ignored fourth line"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("failure rendering leaked %q: %q", forbidden, got)
		}
	}
}

func TestRenderTextExtractsUsefulNestedMultilineFailure(t *testing.T) {
	var output bytes.Buffer
	if err := RenderText(&output, Event{
		Tool:   "edit",
		Type:   TypeToolCompleted,
		Output: []byte(`{"status":"error","result":{"content":[{"type":"text","text":"Error:\nREVISION_MISMATCH: file changed since read\npath: internal/observation/render.go"}]}}`),
	}, false); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	for _, want := range []string{
		"failed: REVISION_MISMATCH: file changed since read",
		"path: internal/observation/render.go",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("nested multiline failure missing %q: %q", want, got)
		}
	}
	if strings.Contains(got, "failed: Error:") {
		t.Fatalf("nested failure retained empty header: %q", got)
	}
}

func TestRenderTextShowsSemanticProgressStates(t *testing.T) {
	tests := []struct {
		name   string
		status string
		want   string
	}{
		{name: "working", status: "in_progress", want: "◆ Progress 正在补消息历史测试"},
		{name: "done", status: "completed", want: "✓ Done 消息 Tab 和问诊聊天页已完成"},
		{name: "waiting", status: "waiting_for_user", want: "? Waiting 需要确认推送 origin/main"},
		{name: "blocked", status: "blocked", want: "! Blocked 缺少测试环境配置"},
		{name: "failed", status: "failed", want: "✗ Failed 全仓测试仍有失败"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			current := map[string]string{
				"in_progress":      "正在补消息历史测试",
				"completed":        "消息 Tab 和问诊聊天页已完成",
				"waiting_for_user": "需要确认推送 origin/main",
				"blocked":          "缺少测试环境配置",
				"failed":           "全仓测试仍有失败",
			}[test.status]
			input := `{"status":` + strconv.Quote(test.status) + `,"current":` + strconv.Quote(current) + `,"result":["2 files changed","tests checked"],"next":"根据状态继续"}`
			if err := RenderText(&output, Event{
				Tool: "progress", Type: TypeToolCompleted, Input: []byte(input), Output: []byte(`{"status":"succeeded"}`),
			}, false); err != nil {
				t.Fatal(err)
			}
			got := output.String()
			for _, want := range []string{test.want, "result:\n", "    - 2 files changed\n", "    - tests checked\n", "next: 根据状态继续"} {
				if !strings.Contains(got, want) {
					t.Fatalf("progress rendering missing %q: %q", want, got)
				}
			}
			if strings.Contains(got, "Reported") {
				t.Fatalf("progress used generic tool rendering: %q", got)
			}
		})
	}
}

func TestRenderTextShowsFailedToolAndSnapshotReason(t *testing.T) {
	var output bytes.Buffer
	if err := RenderText(&output, Event{
		Tool:   "session",
		Type:   TypeToolCompleted,
		Status: "failed",
		Input:  []byte(`{"action":"attach","remote_session_id":"c897d261-7b53-4a24-9a8b-d7c2ed78cde2"}`),
		Output: []byte(`{"available":true,"status":"failed","summary":"SESSION_NOT_FOUND: remote session does not exist"}`),
	}, false); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	if !strings.Contains(got, "! Session failed c897d261-7b53-4a24-9a8b-d7c2ed78cde2") {
		t.Fatalf("failed tool identity missing: %q", got)
	}
	if !strings.Contains(got, "failed: SESSION_NOT_FOUND: remote session does not exist") {
		t.Fatalf("failed tool reason missing: %q", got)
	}
}

func TestRenderTextHidesToolStartAndShowsHumanReadAction(t *testing.T) {
	var output bytes.Buffer
	err := RenderText(&output, Event{
		Tool:   "file_read",
		Type:   TypeToolStarted,
		Intent: "inspect the login flow",
		Input:  []byte(`{"path":"auth.go","offset":10,"limit":20}`),
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatalf("tool start should be hidden: %q", output.String())
	}
	if err := RenderText(&output, Event{
		Tool:   "file_read",
		Type:   TypeToolCompleted,
		Intent: "inspect the login flow",
		Input:  []byte(`{"path":"auth.go","offset":10,"limit":20}`),
		Output: []byte(`{"status":"ok","result":{"content":[{"type":"text","text":"Read 1 source item(s); 42 bytes returned."}]}}`),
	}, false); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{"• Read auth.go (lines 11-30)", "Read 1 source item(s); 42 bytes returned."} {
		if !strings.Contains(text, want) {
			t.Fatalf("human read rendering missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "TOOL") || strings.Contains(text, "intent:") || strings.Contains(text, "```json") || strings.Contains(text, "\"path\":\"auth.go\"") {
		t.Fatalf("human read rendering leaked protocol details: %q", text)
	}
}

func TestRenderTextShowsBatchReadPathsAndRanges(t *testing.T) {
	var output bytes.Buffer
	if err := RenderText(&output, Event{
		Tool:   "read",
		Type:   TypeToolCompleted,
		Input:  []byte(`{"view":"file","items":[{"path":"internal/arc/arc.go","offset":0,"limit":40},{"path":"internal/arc/human.go","offset":80,"limit":20},{"path":"internal/arc/presentation.go","mode":"full"}]}`),
		Output: []byte(`{"status":"succeeded","summary":"Read 3 source items."}`),
	}, false); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{
		"• Read 3 files",
		"internal/arc/arc.go (lines 1-40)",
		"internal/arc/human.go (lines 81-100)",
		"internal/arc/presentation.go (full)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("batch read rendering missing %q: %s", want, text)
		}
	}
}

func TestRenderTextShowsReadViewTargets(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "list directory", input: `{"view":"list","path":"flux-boot-ui/src/store","include_glob":"**/*.ts"}`, want: "• Read flux-boot-ui/src/store (list)"},
		{name: "search scope", input: `{"view":"search","query":"loading","paths":["flux-boot-ui/src"]}`, want: `• Read search "loading" in flux-boot-ui/src`},
		{name: "context scope", input: `{"view":"context","query":"consultComm refresh history","paths":["flux-boot-ui/src/store"]}`, want: `• Read context "consultComm refresh history" in flux-boot-ui/src/store`},
		{name: "environment sections", input: `{"view":"environment","sections":["toolchains","runtime"]}`, want: "• Read environment (toolchains, runtime)"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			if err := RenderText(&output, Event{
				Tool:   "read",
				Type:   TypeToolCompleted,
				Input:  []byte(test.input),
				Output: []byte(`{"status":"succeeded"}`),
			}, false); err != nil {
				t.Fatal(err)
			}
			text := output.String()
			if !strings.Contains(text, test.want) {
				t.Fatalf("read target missing %q: %q", test.want, text)
			}
			if strings.Contains(text, "• Read read\n") {
				t.Fatalf("read action fell back to generic tool name: %q", text)
			}
		})
	}
}

func TestRenderTextShowsSearchCommandAndFullMatchPaths(t *testing.T) {
	var output bytes.Buffer
	if err := RenderText(&output, Event{
		Tool:   "context_query",
		Type:   TypeToolCompleted,
		Intent: "查找会员查询实现",
		Input:  []byte(`{"action":"search","query":"会员卡 手机号 customer userId","paths":["fanyi-cloud-ui"],"include_glob":"**/*.vue"}`),
		Output: []byte(`{"status":"ok","result":{"content":[{"type":"text","text":"Source search returned 2 match(es)."}],"structured_content":{"matches":[{"path":"fanyi-cloud-ui/src/views/erp/cashier-desk/index.vue","line":81},{"path":"fanyi-cloud-ui/src/views/erp/cashier-desk/components/order.vue","line":24}],"truncated":false}}}`),
	}, false); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{
		`• Searched rg --glob "**/*.vue" "会员卡 手机号 customer userId" fanyi-cloud-ui`,
		`  Source search returned 2 match(es): fanyi-cloud-ui/src/views/erp/cashier-desk/index.vue:81, fanyi-cloud-ui/src/views/erp/cashier-desk/components/order.vue:24`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("search rendering missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "intent:") || strings.Contains(text, `"matches"`) {
		t.Fatalf("search rendering leaked intent or JSON: %s", text)
	}
}

func TestRenderTextShowsWorkspaceListResultInsteadOfOK(t *testing.T) {
	var output bytes.Buffer
	if err := RenderText(&output, Event{
		Tool:   "workspace_list",
		Type:   TypeToolCompleted,
		Input:  []byte(`{"intent":"列出可用工作区"}`),
		Output: []byte(`{"status":"ok","result":{"available":true,"content":[{"type":"text","text":"{\"request_id\":\"req_1\",\"status\":\"ok\",\"data\":{\"workspaces\":[{\"name\":\"fyy\",\"path\":\"/workspaces/fyy\"}]}}"}]}}`),
	}, false); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{"• Listed workspaces", "Available workspaces: fyy (/workspaces/fyy)"} {
		if !strings.Contains(text, want) {
			t.Fatalf("workspace list rendering missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "intent:") || strings.Contains(text, "data=object") || strings.Contains(text, "TOOL") {
		t.Fatalf("workspace list rendering leaked protocol details: %s", text)
	}
}

func TestRenderTextSummarizesPlanManageEnvelope(t *testing.T) {
	var output bytes.Buffer
	if err := RenderText(&output, Event{
		Tool:   "plan_manage",
		Type:   TypeToolCompleted,
		Input:  []byte(`{"action":"create","summary":"修复可见性"}`),
		Output: []byte(`{"status":"ok","result":{"content":[{"type":"text","text":"{\"ok\":true,\"status\":\"ok\",\"data\":{\"plan_id\":\"pl_demo\",\"status\":\"ready\",\"tasks\":[{\"plan_task_id\":\"pt_1\"},{\"plan_task_id\":\"pt_2\"}]}}"}]}}`),
	}, false); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{"Plan pl_demo", "状态 ready", "任务 2 个", "pt_1", "pt_2"} {
		if !strings.Contains(text, want) {
			t.Fatalf("plan summary missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "data=object") {
		t.Fatalf("plan summary leaked generic data marker: %s", text)
	}
}

func TestRenderTextSummarizesEnvironmentInspectEnvelope(t *testing.T) {
	var output bytes.Buffer
	if err := RenderText(&output, Event{
		Tool:   "environment_inspect",
		Type:   TypeToolCompleted,
		Output: []byte(`{"status":"ok","result":{"content":[{"type":"text","text":"{\"ok\":true,\"status\":\"ok\",\"data\":{\"snapshot_id\":\"env_demo\",\"toolchains\":{\"python\":{\"available\":true,\"version\":\"Python 3.11.9\"},\"go\":{\"available\":true,\"version\":\"go1.26.1\"},\"node\":{\"available\":false}}}}"}]}}`),
	}, false); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{"环境快照 env_demo 已保存", "python Python 3.11.9", "go go1.26.1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("environment summary missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "data=object") {
		t.Fatalf("environment summary leaked generic data marker: %s", text)
	}
}

func TestRenderTextSummarizesScreenshotRuntimeAndWorkspaceMemoryEnvelopes(t *testing.T) {
	tests := []struct {
		name   string
		tool   string
		output string
		wants  []string
	}{
		{
			name:   "screenshot",
			tool:   "screenshot_capture",
			output: `{"status":"ok","result":{"content":[{"type":"text","text":"{\"ok\":true,\"data\":{\"display\":0,\"output_width\":1440,\"output_height\":900,\"format\":\"png\"}}"}]}}`,
			wants:  []string{"• Captured screenshot", "截图已捕获", "1440×900", "png"},
		},
		{
			name:   "runtime project",
			tool:   "runtime_inspect",
			output: `{"status":"ok","result":{"content":[{"type":"text","text":"{\"ok\":true,\"data\":{\"stacks\":[\"python\"],\"manifests\":[\"pyproject.toml\"],\"git_status\":\"## main\"}}"}]}}`,
			wants:  []string{"• Read project summary", "项目摘要", "技术栈：python", "清单：pyproject.toml"},
		},
		{
			name:   "workspace memory",
			tool:   "workspace_state",
			output: `{"status":"ok","result":{"content":[{"type":"text","text":"{\"ok\":true,\"data\":{\"items\":[{\"id\":7}],\"total\":42,\"has_more\":true}}"}]}}`,
			wants:  []string{"• Read project memory", "项目记忆：返回 1 条，共 42 条", "还有更多"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			input := []byte(`{}`)
			switch test.tool {
			case "runtime_inspect":
				input = []byte(`{"action":"project"}`)
			case "workspace_state":
				input = []byte(`{"action":"memory"}`)
			}
			if err := RenderText(&output, Event{Tool: test.tool, Type: TypeToolCompleted, Input: input, Output: []byte(test.output)}, false); err != nil {
				t.Fatal(err)
			}
			text := output.String()
			for _, want := range test.wants {
				if !strings.Contains(text, want) {
					t.Fatalf("summary missing %q: %s", want, text)
				}
			}
			if strings.Contains(text, "data=object") {
				t.Fatalf("summary leaked generic data marker: %s", text)
			}
		})
	}
}

func TestRenderTextShowsAgentActivitySemanticTimeline(t *testing.T) {
	var output bytes.Buffer
	if err := RenderText(&output, Event{
		Type:             TypeAgentActivity,
		Status:           "reviewing_result",
		TurnID:           "turn-42",
		ActivitySequence: 18,
		ActivityKind:     "evidence",
		ProgressSummary:  "已确认 profit_sharing_percentage 直接参与结算金额计算。",
		RelatedCallID:    "call-read-1",
	}, false); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if !strings.Contains(text, "◇ Evidence") || !strings.Contains(text, "已确认 profit_sharing_percentage 直接参与结算金额计算。") {
		t.Fatalf("activity semantic timeline missing: %s", text)
	}
	for _, forbidden := range []string{"Observed", "REVIEWING RESULT", "call-read-1"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("compact activity leaked %q: %s", forbidden, text)
		}
	}
}

func TestRenderTextHidesObserverMetadataObject(t *testing.T) {
	var output bytes.Buffer
	if err := RenderText(&output, Event{
		Type:    TypeObserverNotice,
		Summary: "command.started: go test ./...",
		Output:  []byte(`{"source_type":"command.started","source_sequence":311,"metadata":{"purpose":"测试","scope":"workspace"}}`),
	}, false); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if !strings.Contains(text, "• Observed command.started: go test ./...") {
		t.Fatalf("observer title missing: %s", text)
	}
	for _, forbidden := range []string{"metadata=object", "source_sequence", "purpose="} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("observer metadata leaked %q: %s", forbidden, text)
		}
	}
}

func TestRenderTextSummarizesExtensionInventoryStructuredContent(t *testing.T) {
	var output bytes.Buffer
	if err := RenderText(&output, Event{
		Tool:   "extension_manage",
		Type:   TypeToolCompleted,
		Input:  []byte(`{"action":"list","kind":"skill","query":"ui ux"}`),
		Output: []byte(`{"status":"ok","result":{"content":[{"type":"text","text":"Extension inventory returned."}],"structured_content":{"skills":[{"name":"ui-ux-pro-max"}]}}}`),
	}, false); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{"Skill 1 项：ui-ux-pro-max"} {
		if !strings.Contains(text, want) {
			t.Fatalf("extension inventory summary missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "Extension inventory returned.") {
		t.Fatalf("generic extension text should be replaced by inventory summary: %s", text)
	}
}

func TestRenderTextShowsFindCommandForContextList(t *testing.T) {
	var output bytes.Buffer
	if err := RenderText(&output, Event{
		Tool:   "context_query",
		Type:   TypeToolCompleted,
		Input:  []byte(`{"action":"list","paths":["fanyi-cloud-ui"],"include_glob":"**/*.vue"}`),
		Output: []byte(`{"status":"ok","result":{"content":[{"type":"text","text":"Source list returned 2 of 2 file(s)."}],"structured_content":{"files":[{"path":"fanyi-cloud-ui/src/views/a.vue"},{"path":"fanyi-cloud-ui/src/views/b.vue"}]}}}`),
	}, false); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{
		`• Searched find fanyi-cloud-ui -type f -path "**/*.vue"`,
		`  Source list returned 2 of 2 file(s): fanyi-cloud-ui/src/views/a.vue, fanyi-cloud-ui/src/views/b.vue`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("context list rendering missing %q: %s", want, text)
		}
	}
}

func TestRenderTextShowsToolOutputSummaryWithoutSourceBlock(t *testing.T) {
	var output bytes.Buffer
	err := RenderText(&output, Event{
		Tool:   "file_read",
		Type:   TypeToolCompleted,
		Intent: "inspect the supplier form",
		Output: []byte(`{"status":"ok","timing":{"server_elapsed_ms":1},"result":{"content":[{"type":"text","text":"Read 1 source item(s); 42 bytes returned."}],"structured_content":{"path":"src/Supplier.vue","content":"<template>\n  <div />\n</template>\n","offset":0,"limit":3,"total_lines":3}}}`),
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{"• Read src/Supplier.vue", "Read 1 source item(s); 42 bytes returned."} {
		if !strings.Contains(text, want) {
			t.Fatalf("tool completion missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "### `src/Supplier.vue`") || strings.Contains(text, "```vue") || strings.Contains(text, "structured_content") || strings.Contains(text, "```json") || strings.Contains(text, "status=") {
		t.Fatalf("tool completion must render only the summary: %s", text)
	}
}

func TestRenderTextShowsExecutedCommandAndProgressSummary(t *testing.T) {
	var output bytes.Buffer
	if err := RenderText(&output, Event{
		Tool:            "command_execute",
		Type:            TypeToolCompleted,
		ProgressSummary: "已读取配置，下一步运行单元测试",
		Input:           []byte(`{"command":"go test ./internal/...","purpose":"验证修改"}`),
		Output:          []byte(`{"status":"ok","timing":{"server_elapsed_ms":12},"result":{"content":[{"type":"text","text":"Command completed with exit code 0."}]}}`),
	}, false); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{"• Ran go test ./internal/...", "progress: 已读取配置，下一步运行单元测试", "Command completed with exit code 0."} {
		if !strings.Contains(text, want) {
			t.Fatalf("command rendering missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "TOOL COMPLETED") || strings.Contains(text, "status=") || strings.Contains(text, "elapsed=") || strings.Contains(text, "duration=") {
		t.Fatalf("legacy completion label or default telemetry leaked: %s", text)
	}
}

func TestRenderTextColorsCommandAndStreams(t *testing.T) {
	var stdout bytes.Buffer
	if err := RenderText(&stdout, Event{
		Tool:    "command_execute",
		Type:    TypeCommandOutput,
		Command: "go test ./...",
		Stream:  "stdout",
		Output:  []byte(`{"text":"ok\n"}`),
	}, true); err != nil {
		t.Fatal(err)
	}
	stdoutText := stdout.String()
	for _, want := range []string{
		ansiAmber + "Ran" + ansiReset + " go test ./...",
		ansiGray + "stdout:" + ansiReset,
		ansiGray + "  1 | " + ansiReset + ansiDim + "ok" + ansiReset,
	} {
		if !strings.Contains(stdoutText, want) {
			t.Fatalf("stdout command color missing %q: %q", want, stdoutText)
		}
	}

	var stderr bytes.Buffer
	if err := RenderText(&stderr, Event{
		Tool:    "command_execute",
		Type:    TypeCommandOutput,
		Command: "go test ./...",
		Stream:  "stderr",
		Output:  []byte(`{"text":"failed\n"}`),
	}, true); err != nil {
		t.Fatal(err)
	}
	stderrText := stderr.String()
	for _, want := range []string{
		ansiYellow + "stderr:" + ansiReset,
		ansiGray + "  1 | " + ansiReset + ansiYellow + "failed" + ansiReset,
	} {
		if !strings.Contains(stderrText, want) {
			t.Fatalf("stderr command color missing %q: %q", want, stderrText)
		}
	}

	var failed bytes.Buffer
	if err := RenderText(&failed, Event{
		Tool:    "command_execute",
		Type:    TypeToolCompleted,
		Status:  "failed",
		Command: "go test ./...",
		Input:   []byte(`{"command":"go test ./..."}`),
		Output:  []byte(`{"status":"failed","error":"tests failed"}`),
	}, true); err != nil {
		t.Fatal(err)
	}
	failedText := failed.String()
	for _, want := range []string{
		ansiRed + "Command failed" + ansiReset + " go test ./...",
	} {
		if !strings.Contains(failedText, want) {
			t.Fatalf("failed command color missing %q: %q", want, failedText)
		}
	}
}

func TestRenderTextShowsMarkdownFileDiff(t *testing.T) {
	var output bytes.Buffer
	err := RenderText(&output, Event{
		Type:    TypeFileChanged,
		Summary: "update login flow",
		Output:  []byte(`{"results":[{"path":"auth.go","operation":"update","diff":"--- a/auth.go\n+++ b/auth.go\n@@\n-old\n+new\n"}]}`),
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{"• Edited auth.go [-1,+1]", "  1 | -old", "  1 | +new"} {
		if !strings.Contains(text, want) {
			t.Fatalf("file diff rendering missing %q: %q", want, text)
		}
	}
	if strings.Count(text, "auth.go") != 1 || strings.Contains(text, "--- a/auth.go") || strings.Contains(text, "@@") {
		t.Fatalf("file diff should show one path without unified headers: %q", text)
	}
	if strings.Contains(text, "```diff") || strings.Contains(text, "status=") {
		t.Fatalf("file diff rendering=%q", text)
	}
}

func TestRenderTextShowsMoveOutAsWorkspaceMutation(t *testing.T) {
	var output bytes.Buffer
	if err := RenderText(&output, Event{
		Tool:   "move_out",
		Type:   TypeFileChanged,
		Status: "committed",
		Output: []byte(`{"status":"committed","moved_count":1,"failed_count":0,"target_count":1,"reversible":true,"target_preview":[{"path":"src/old.go","kind":"file","status":"moved","quarantine_path":"trash://old.go"}]}`),
	}, false); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{"• Removed src/old.go", "Moved to quarantine", "Moved 1 · failed 0 · reversible"} {
		if !strings.Contains(text, want) {
			t.Fatalf("move_out rendering missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "file details unavailable") || strings.Contains(text, "target_preview") {
		t.Fatalf("move_out leaked generic/protocol rendering: %s", text)
	}
}

func TestRenderTextShowsCleanEditResultsDiff(t *testing.T) {
	var output bytes.Buffer
	err := RenderText(&output, Event{
		Tool:    "edit",
		Type:    TypeFileChanged,
		Summary: "edit 2 files, 2 changed lines",
		Output: []byte(`{"edit_id":"edit_clean_1","results":[
{"path":"created.txt","operation":"create","diff":"--- /dev/null\n+++ b/created.txt\n@@ -0,0 +1 @@\n+created\n"},
{"path":"updated.txt","operation":"update","diff":"--- a/updated.txt\n+++ b/updated.txt\n@@ -1 +1 @@\n-old\n+new\n"}
]}`),
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{"created.txt (create)", "updated.txt (update)", "  1 | +created", "  1 | -old"} {
		if !strings.Contains(text, want) {
			t.Fatalf("clean edit diff missing %q: %q", want, text)
		}
	}
	if strings.Contains(text, "file details unavailable") {
		t.Fatalf("clean edit fell back to unavailable details: %q", text)
	}
	if strings.Contains(text, "--- ") || strings.Contains(text, "+++ ") || strings.Contains(text, "@@") {
		t.Fatalf("clean edit leaked unified headers: %q", text)
	}
}

func TestRenderTextTruncatesLargeFileDiff(t *testing.T) {
	var output bytes.Buffer
	if err := RenderText(&output, Event{
		Type:   TypeFileChanged,
		Output: []byte(`{"results":[{"path":"large.go","operation":"update","diff":"--- a/large.go\n+++ b/large.go\n@@\n-line-1\n+line-1\n-line-2\n+line-2\n-line-3\n+line-3\n-line-4\n+line-4\n-line-5\n+line-5\n"}]}`),
	}, false); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if !strings.Contains(text, "• Edited large.go [-5,+5]") || !strings.Contains(text, "  5 | +line-5") {
		t.Fatalf("large diff rendering: %s", text)
	}
}

func TestRenderFileChangedFullKeepsAllContentWithoutTransportHeaders(t *testing.T) {
	diff := "--- a/context.go\n+++ b/context.go\n@@ -1,13 +1,13 @@\n" +
		" before-1\n before-2\n before-3\n before-4\n before-5\n before-6\n" +
		"-old\n+new\n" +
		" after-1\n after-2\n after-3\n after-4\n after-5\n after-6\n"
	var output bytes.Buffer
	if err := RenderText(&output, Event{Type: TypeFileChanged, Output: []byte(`{"results":[{"path":"context.go","operation":"update","diff":` + strconv.Quote(diff) + `}]}`)}, false); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{"before-1", "before-6", "-old", "+new", "after-1", "after-6"} {
		if !strings.Contains(text, want) {
			t.Fatalf("full diff missing %q: %q", want, text)
		}
	}
	for _, header := range []string{"--- a/context.go", "+++ b/context.go", "@@ -1,13 +1,13 @@"} {
		if strings.Contains(text, header) {
			t.Fatalf("full diff leaked transport header %q: %q", header, text)
		}
	}
}

func TestRenderFileChangedSupportsSummaryAndPreviewModes(t *testing.T) {
	payload := []byte(`{"results":[{"path":"demo.go","operation":"update","diff":"--- a/demo.go\n+++ b/demo.go\n@@ -10 +20 @@\n-old\n+new\n"}]}`)
	var summary bytes.Buffer
	if err := renderFileChanged(&summary, Event{Type: TypeFileChanged, Output: payload}, renderOptions{diffMode: DiffModeSummary}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(summary.String(), "-old") || strings.Contains(summary.String(), "+new") || !strings.Contains(summary.String(), "Edited demo.go [-1,+1]") {
		t.Fatalf("summary diff=%q", summary.String())
	}

	var preview bytes.Buffer
	if err := renderFileChanged(&preview, Event{Type: TypeFileChanged, Output: payload}, renderOptions{diffMode: DiffModePreview, terminalWidth: 120}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"10 | -old", "20 | +new"} {
		if !strings.Contains(preview.String(), want) {
			t.Fatalf("preview missing %q: %q", want, preview.String())
		}
	}
}

func TestRenderFileChangedColorsCompactDiffStats(t *testing.T) {
	payload := []byte(`{"results":[{"path":"demo.go","operation":"create","diff":"--- /dev/null\n+++ b/demo.go\n@@ -0,0 +1 @@\n+new\n"}]}`)
	var colored bytes.Buffer
	if err := renderFileChanged(&colored, Event{Type: TypeFileChanged, Output: payload}, renderOptions{diffMode: DiffModeSummary, colorMode: ColorModeANSI16}); err != nil {
		t.Fatal(err)
	}
	wantStats := ansiRed + "-0" + ansiReset + "," + ansiGreen + "+1" + ansiReset
	if !strings.Contains(colored.String(), wantStats) {
		t.Fatalf("colored compact stats missing %q: %q", wantStats, colored.String())
	}

	var plain bytes.Buffer
	if err := renderFileChanged(&plain, Event{Type: TypeFileChanged, Output: payload}, renderOptions{diffMode: DiffModeSummary, colorMode: ColorModeNone}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain.String(), "\033[") || !strings.Contains(plain.String(), "Created demo.go [-0,+1]") {
		t.Fatalf("plain compact stats=%q", plain.String())
	}
}

func TestRenderTextShowsSourceFormatMetadata(t *testing.T) {
	var output bytes.Buffer
	if err := RenderText(&output, Event{
		Tool:   "file_read",
		Type:   TypeToolCompleted,
		Input:  []byte(`{"path":"demo.go"}`),
		Output: []byte(`{"status":"succeeded","result":{"content":[{"type":"text","text":"Read demo.go."}],"structured_content":{"path":"demo.go","sha256":"sha256:abc","format":{"charset":"UTF-8","line_ending":"CRLF","final_newline":true}}}}`),
	}, false); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{"format=UTF-8", "line-ending=CRLF", "final-newline=yes", "sha256=sha256:abc"} {
		if !strings.Contains(text, want) {
			t.Fatalf("format metadata %q missing: %q", want, text)
		}
	}
}

func TestRenderJSONEmitsOneEventLine(t *testing.T) {
	var output bytes.Buffer
	if err := RenderJSON(&output, Event{Sequence: 7, Workspace: "demo", Type: TypeObserverNotice}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(output.String(), "\n") || !strings.Contains(output.String(), `"sequence":7`) {
		t.Fatalf("json rendering=%q", output.String())
	}
}

func TestCommandOutputEllipsisStaysOnLine(t *testing.T) {
	renderer := NewTextRendererWithWidth(false, 80)
	var output bytes.Buffer
	line := "SMOKE-007 FAIL http=200 code=401 msg=请求访问：/api/getInfo，认证失败，无法访问系统资源 trace."
	if err := renderer.RenderEvent(&output, Event{
		Tool: "command_execute", Type: TypeCommandOutput, Stream: "stdout",
		Command: "python3 -i",
		Output:  []byte(`{"text":"` + line + `\n"}`),
	}); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if strings.Contains(text, "\n      ..\n") {
		t.Fatalf("stray wrapped ellipsis line: %q", text)
	}
	for _, outputLine := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		if width := displayWidth(outputLine); width > 80 {
			t.Fatalf("line over budget width=%d: %q", width, outputLine)
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(text), "...") {
		t.Fatalf("ellipsis not kept on the content line: %q", text)
	}
}

func TestFullDiffWrapsWithoutTruncatingContent(t *testing.T) {
	renderer := NewTextRendererWithWidth(false, 80)
	var output bytes.Buffer
	line := strings.Repeat("回归验证日志内容", 12) + "TAIL-MARKER"
	diff := "--- a/report.md\n+++ b/report.md\n@@ -1 +1 @@\n-old\n+" + line + "\n"
	payload := []byte(`{"results":[{"path":"report.md","operation":"update","diff":` + strconv.Quote(diff) + `}]}`)
	if err := renderer.RenderEvent(&output, Event{
		Tool:   "edit",
		Type:   TypeFileChanged,
		Output: payload,
	}); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, outputLine := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		if width := displayWidth(outputLine); width > 80 {
			t.Fatalf("line over budget width=%d: %q", width, outputLine)
		}
	}
	if strings.Contains(text, "output truncated") || strings.Contains(text, "...") || !strings.Contains(text, "TAIL-MARKER") {
		t.Fatalf("full diff lost content: %q", text)
	}
}
