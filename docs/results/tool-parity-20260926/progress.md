# progress 对标审查（2026-09-26）

结论：限定关键路径通过，未确认造成卡住、错误或额外重试的缺陷，无需修改实现。

## 范围与版本
- 工作树：`/Users/rick/.codex/worktrees/mcpx-tool-reliability/mcpx`。
- 分支 `codex/tool-reliability-20260925`，HEAD `6f799d2ddfa7ab9cbfbe0493823af6db11e918df`。
- 测试使用包含其他 agent 改动的当前共享工作树；结果不代表常驻服务验收。
- Codex：`/tmp/mcpx-codex-cli-20260925`，commit `68e0c9f5d8fd9449e97a81e92e8fcb86795713b2`，只读对照。
- 仅新增本工具测试和报告；未改实现，未调用常驻服务，未部署、重启、commit/push。

## Codex 对照
以下 Codex 路径均相对上述 clone：
- `codex-rs/protocol/src/models.rs:944-952`：`MessagePhase::Commentary/FinalAnswer` 区分过程叙述和最终回答，接近 progress 的过程／终态，但不是独立 MCP 工具。
- `codex-rs/core/src/stream_events_utils.rs:532-545`：`completed_item_defers_mailbox_delivery_to_next_turn` 对 Commentary 返回 false，消息阶段参与实际流程判断。
- `codex-rs/core/src/tools/handlers/plan_spec.rs:7-57`：`create_update_plan_tool` 定义必填 plan、可选 explanation，步骤状态 pending/in_progress/completed。
- `codex-rs/core/src/tools/handlers/plan.rs:66-111`：`PlanHandler::handle_call` 拒绝不支持的 payload 和 Plan 模式，解析后发 `EventMsg::PlanUpdate`；解析失败向模型返回错误。
- 同文件 `PlanToolOutput:22-45` 返回 `Plan updated`；`codex-rs/protocol/src/plan_tool.rs:15-29` 拒绝未知字段。
- 无直接同构工具：update_plan 更新步骤清单，MCPX progress 记录会话级里程碑、结果与终态；不据此扩展产品范围。

## MCPX schema、handler 与输出
- `internal/server/tools_clean_core.go:323-335`：必填 remote_session_id/current；status 五种值；result 为字符串数组，最多 12 项。
- `internal/server/tools_progress.go:18-61`：解析会话，检查 current 空白及字节上限、result 项数和累计字节上限；过滤空结果，默认 status=in_progress 并校验枚举。
- 同文件 `63-80`：文本展示 current、结果数量和 next；结构化数据保留实际 result、status、phase、会话和 workspace。
- `internal/server/tools_catalog.go:68-84` 明确文本负责展示、structuredContent 承载机器数据，不能将文本仅显示结果数量误判为结果丢失。

## 实际调用与复现
在指定工作树运行：
```sh
go test ./internal/server -run '^(TestParityprogress|TestProgress)' -count=1 -v
```
- `newWorkspaceRuntime`（`internal/server/workspace_resolve_test.go:18-43`）将 MCPX_HOME、配置、workspace、数据库置于 t.TempDir，使用 auth=open。
- 新增 `internal/server/parity_progress_test.go:11-57`，通过 `instrumentTool("progress", rt.toolProgress)` 调用真实 handler，没有 mock progress。
- 流程：省略 status → in_progress；invalid → 拒绝；同会话修正为 completed → 成功；持久化查询及重新附着 → completed。
- 实际输出（日志节选）：
```text
default=in_progress invalid_status=map[category:validation code:BAD_REQUEST details:map[origin:caller retry_hint:Correct the request arguments and retry. safe_to_retry_unchanged:false] message:unsupported progress status "invalid" retryable:false]
recovery=completed persisted=completed reattach_status=completed summary=isolated progress check
--- PASS: TestParityprogressLifecycleAndErrorRecovery (0.50s)
PASS
ok  mcpx/internal/server  0.812s
```
- 共 5 项通过：新增 lifecycle/error recovery；既有 `TestProgressRecordsPauseStateAndPreviousResult`、`TestProgressAcceptsTerminalFailedStateAndRestoresLatestModelState`、`TestProgressAllowsResultUpToConfiguredLimit`、`TestProgressRejectsLegacyFieldShape`。
- 既有测试证据见 `internal/server/tools_progress_test.go:18-187`：waiting_for_user 事件、failed 持久化与附着恢复、结果字节上限接受／超限拒绝、缺少 current 拒绝。

## 问题与最小修复
- 确证缺陷：无；严重度不适用。invalid 状态是预期校验错误；错误明确要求修改参数且禁止原样重试，修正后无需重开会话。
- 最小修复：无需实现修复，保留新增回归测试；不调整 plan 的显式幂等失败重放策略。

## 未覆盖与交接
- ALL_TOOLS 已发现 MacMCPX progress connector；按禁止常驻服务写入的边界未调用。
- 未验证宿主 connector 字段完整性、MCP HTTP schema 校验及传输、真实观察端 UI；由主线统一 schema／端到端 harness 验收。
- Codex 仅源码对照，未执行其二进制或测试；无联网、新 clone、私人屏幕或外部应用访问。
- 未覆盖 session 过期、并发发布顺序及所有字段边界组合；未跑全套、race 或构建。
- 本工具审查完成；其他工具验收不属于本报告。
