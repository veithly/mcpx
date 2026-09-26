# browser 逐工具对标审查（2026-09-26）

结论：确认 1 项 P2 参数丢失；已知 macOS 不支持边界符合预期。未修改实现。
范围：工作树 /Users/rick/.codex/worktrees/mcpx-tool-reliability/mcpx；分支 codex/tool-reliability-20260925；HEAD/base 6f799d2。
共享工作树已有主线及其他 agent 改动；受审 tools_browser.go、tools_catalog.go 相对 HEAD 无差异。
不含宿主 connector schema 对比和统一端到端 harness；不访问真实浏览器、网站、凭证或外部应用。

## Codex 对照（只读源码）

对照仓库 /tmp/mcpx-codex-cli-20260925，实际 HEAD 68e0c9f5d8fd9449e97a81e92e8fcb86795713b2。
- core 工具 handler 注册模块未提供直接 browser handler：codex-rs/core/src/tools/handlers/mod.rs，MCP 模块在第 10 行；不能据此推断桌面私有浏览器实现。
- 最近工作流为 MCP 工具调用：codex-rs/core/src/tools/handlers/mcp.rs:196，McpHandler::handle_call 绑定调用元数据、检查 payload；不提供 DOM 动作语义。
- codex-rs/core/src/mcp_tool_call.rs:128 handle_mcp_tool_call；:439 handle_approved_mcp_tool_call；:749 maybe_request_codex_apps_auth_elicitation：分别覆盖调用、批准后执行、认证交互。
- 同文件 :927 sanitize_mcp_tool_result_for_model，:960-962 保留 structured_content/is_error/meta；图像不受模型支持时替换为说明。
- MCPX 对应 tools_browser.go:22 toolBrowser、:107 确认绑定、:127 单次 stale attachment 重试、:147 导航恢复、:174 截图处理；不存在可声称逐动作等价的 Codex 公共实现。
（上述 core 相对路径均以对照仓库为根；MCPX 相对路径均以指定工作树为根。）

## 实际调用与通过项

复现工作目录必须为 /Users/rick/.codex/worktrees/mcpx-tool-reliability/mcpx：
```sh
go test ./internal/server -run '^(TestParitybrowser|TestBrowser)' -count=1 -v
```
实际结果：10 个顶层测试通过、1 个顶层测试失败（含 click/double_click 两个失败子项），包耗时 3.102s，退出码 1。
完整原始输出：/tmp/mcpx-browser-parity-20260926.log。
- TestParitybrowserStatusUnsupportedHTTP：newWorkspaceRuntime（workspace_resolve_test.go:18）将 MCPX_HOME、工作区和数据库放入 t.TempDir；开启临时 localhost MCP HTTP，调用 browser {action:status, remote_session_id:<临时会话>}。
- 实际响应摘要：isError=true code=BROWSER_UNAVAILABLE message=OpenAI browser extension integration is only supported on Windows。
- 这是已知平台边界，非新缺陷：browseruse/installation_other.go:5 DiscoverInstalledOfficial 立即返回 ErrUnsupported；不加载浏览器后端。
- 正常映射、混合参数拒绝通过：TestBrowserServiceCommandMapsCommonActions / RejectsInvalidMixedArguments（tools_browser_test.go:17 / :127）。包含导航 timeout、坐标点击、滚动、拖拽、截图及 official 禁止 browser_id 覆盖。
- 公开 schema 与风险标记、确认选择及 digest 绑定通过：TestBrowserPublicSchemaAndRiskMetadata / ConfirmationUsesLatestMatchingPrompt / CommandDigestBindsBrowserAndCommand（:170 / :151 / :213）。
- 导航超时/节点陈旧恢复条件通过：TestBrowserNavigationTimeoutRecoveryRequiresObservedURLChange / ClickNavigationRecoveryRequiresNodeClickAndURLChange（:225 / :250）。仅模拟判断函数，不代表真实导航成功。
- 截图 base64 转 image content、已知错误分类通过：TestBrowserCompactScreenshotMovesBase64ToImageContent / ActionErrorCodeClassifiesExpectedStateErrors（:272 / :294）；使用合成图像字节，无截图采集。

## 确认问题 P2：DOM 点击静默丢弃手势参数

证据：internal/server/tools_catalog.go:510-517 公开 click/double_click 的 node_id 与 keys，click 同时公开 button；描述仅对 timeout_ms 限定模式。
internal/server/tools_browser.go:451-463 的 DOM 分支直接返回 type/tab_id/node_id/timeout_ms，未保留或拒绝 keys/button；坐标分支 :477-483 则处理这些参数。
最小复现（同工作目录）：
```sh
go test ./internal/server -run '^TestParitybrowserNodeClickPreservesGesture$' -count=1 -v
```
本轮组合命令已实际执行上述用例；真实输出：
```text
input=map[button:3 keys:[Shift] node_id:42 tab_id:7] mapped=map[node_id:42 tab_id:7 type:dom_cua_click] err=<nil>
input=map[keys:[Shift] node_id:42 tab_id:7] mapped=map[node_id:42 tab_id:7 type:dom_cua_double_click] err=<nil>
accepted gesture lost keys/button; reject unsupported combination or preserve semantics
```
影响：请求右键或带修饰键的 DOM 点击时，下游收到普通手势，可能执行错误动作或诱发无效重试；参数丢失已证明，真实页面副作用未执行。
最小修复：DOM 模式不支持这些参数时，在映射前明确拒绝并同步 schema/描述；若官方 DOM 契约支持，按其字段传递。不要未核实就假定上游支持。
新增测试 internal/server/parity_browser_test.go:48 刻意保持失败，允许“保留语义或明确拒绝”，供主线统一修复。

## 未覆盖与交付边界

Windows 扩展发现、真实 tabs/claim、导航、截图、权限提示交互、sidecar 关闭重建及重放均未验收；无真实浏览器可用性结论。
确认与恢复仅复用纯映射/存储/判断测试，未把模拟通过写成完整 handler 成功；macOS 实际 handler 仅执行 status。
未运行全套、race、构建、部署；未调用常驻服务；未安装扩展、spawn agent、commit 或 push。
仅新增本报告及 internal/server/parity_browser_test.go；其余源码保持不动。
