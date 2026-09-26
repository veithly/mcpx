# mcp_tool 对标审查（2026-09-26）

结论：本轮关键路径通过，未确认需修复的新缺陷；不改失败重放契约。
范围：仅 mcp_tool；指定 worktree、分支 codex/tool-reliability-20260925，HEAD/base 6f799d2。
测试针对当前共享工作树（含主线已有未提交修改），不代表常驻服务验收。
隔离：newWorkspaceRuntime 设置 MCPX_HOME=t.TempDir；fake stdio peer 仅写临时日志。
依据：internal/server/workspace_resolve_test.go:18、reliability_regression_test.go:18、clean_idempotency_replay_test.go:73。

## Codex 对照

只读 /tmp/mcpx-codex-cli-20260925，已核实 commit 68e0c9f5d8fd9449e97a81e92e8fcb86795713b2。
- 发现：codex-rs/codex-mcp/src/connection_manager/tool_catalog.rs:181 的 list_all_tools / :186 的 list_tools_with_errors 汇集工具及启动错误。
- 协议发现：codex-rs/rmcp-client/src/rmcp_client.rs:693 的 list_tools 执行 tools/list；:816 的 call_tool 校验 arguments/_meta 为对象并调用 tools/call。
- 调度：codex-rs/core/src/tools/handlers/mcp.rs:196 的 McpHandler::handle_call 固定本次绑定与输出策略；codex-rs/core/src/mcp_tool_call.rs:128 的 handle_mcp_tool_call 解析 JSON、处理工具不可用。
- 连接：codex-rs/codex-mcp/src/connection_manager.rs:970 的 call_tool 校验 server、环境、工具启用状态及连接，合并超时。
- 差异：上述 Codex 路径以 ToolInfo/工具定义提供 schema，未见 MCPX 式单个 list/describe/call 三动作入口；未把 Codex 调用误称为具有 MCPX 的持久化幂等重放。

## MCPX schema、handler 与边界

- internal/server/tools_catalog.go:454–469：list 必需 remote_session_id；describe 再需 server/tool；call 再需 purpose。arguments 是开放对象，运行时再校验上游 schema；query 明确筛选 Server。
- tools_discover.go:180/197/247/288：动作路由、清单、详情、当前 schema 检查；schema 变化返回 describe 恢复动作，参数错误返回 MCP_ARGUMENT_INVALID。
- tools_ext.go:221：预检与 call 共用同一上游实例；幂等重放延迟解析配置且无需启动上游。
- tools_ext.go:365–422、internal/mcpproxy/call.go:142：单次上游调用，协议错误不自行重发；成功与业务错误保留上游结果，附加受信任 MCPX metadata。
- 已通过工具目录发现 MacMCPX mcp_tool connector 定义；本轮未调用它，不重复主线的宿主 schema 对比或 HTTP harness。

## 实际调用结果与复现

工作目录：/Users/rick/.codex/worktrees/mcpx-tool-reliability/mcpx。
新增测试经 rt.toolHandlers["mcp_tool"] 调用注册 handler，再走真实 stdio JSON-RPC fake peer；不是仅静态推测。

1. 正常流程与参数恢复：TestParitymcp_toolListDescribeCallAndInvalidArgumentRecovery。
   最小命令：go test ./internal/server -run '^TestParitymcp_toolListDescribeCallAndInvalidArgumentRecovery$' -count=1 -v
   实际输出：list data=map[server:fake tools:[map[description:echo a value name:echo]]]
   describe 返回 input_schema（value:string、required:[value]）；错误参数 value=7 返回 code=mcp_argument_invalid。
   更正为 value=recovered 并使用新 key 后：corrected call="echo:recovered"; replay=true; starts unchanged=4。
   判定：通过；同 key 成功重放不启动新 peer。次数包含 list、describe、错误预检和成功调用。

2. 协议错误后的重复效果：TestParitymcp_toolProtocolFailureDoesNotRepeatEffect。
   最小命令：go test ./internal/server -run '^TestParitymcp_toolProtocolFailureDoesNotRepeatEffect$' -count=1 -v
   fixture 每次 tools/call 先追加 effect，再返回 JSON-RPC -32603；同 key 连续调用两次。
   两次实际错误：MCP_CALL_FAILED: upstream probe/probe: calling "tools/call": response failed after effect
   实际计数：two calls, upstream effects=1。判定：通过；未重放副作用，错误保持可见。

两项实际合并运行命令：go test ./internal/server -run '^TestParitymcp_tool' -count=1 -v
结果：2 PASS；ok mcpx/internal/server 0.854s。
原始日志：/tmp/mcpx-parity-mcp-tool-20260926.log。

补充运行现有 3 项 focused fixture：
go test ./internal/server -run '^(TestMCPToolContractCoversInventoryErrorsSchemaAndUpstreamFailure|TestMCPToolCallPassesContextAndReplaysFullUpstreamResult|TestMCPToolUpstreamIsErrorPassesThroughAndReplays)$' -count=1 -v
结果：3 PASS；ok mcpx/internal/server 2.243s。
证据：clean_core_p1_p3_test.go:471 覆盖 Server 查询、未知 Server/Tool、启动失败、schema 变更拒绝、业务错误；mcp_passthrough_test.go:17/:147 覆盖文本/图片/structuredContent、metadata 隔离、关闭 discovery 后成功及失败重放。
原始日志：/tmp/mcpx-parity-mcp-tool-existing-20260926.log。

## 问题、最小建议与未覆盖

- 本轮无确认缺陷，无严重度条目或实现修复建议；保留两个独占回归测试即可，不新增重构。
- 未验收：真实 MCP Server、宿主 connector/HTTP 端到端、OAuth、网络断连/长超时、异步执行、跨进程重启持久性、分页、大结果预算；上述通过不外推至这些场景。
- Codex 仅源码对照，未构建或执行其测试。所有行为验证在隔离 Runtime，未访问真实凭证、屏幕、外部应用或常驻服务。
- 本 agent 只新增 internal/server/parity_mcp_tool_test.go 与本报告；未改实现、重启、部署、commit/push。
