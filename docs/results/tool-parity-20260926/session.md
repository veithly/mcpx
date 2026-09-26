# session 逐工具对标审查（2026-09-26）

结论：确认 1 个 P1 缺陷（同一分页根因的两个表现）；正常创建、恢复、发现、缺失 ID 恢复提示及 HTTP 重连通过。
交接：主线已确认第三批 session 红测并接手修复；以下结果为本 agent 修复前证据，本报告不宣称主线修复已通过。

## 范围与证据基线
- 工作树：`/Users/rick/.codex/worktrees/mcpx-tool-reliability/mcpx`，分支 `codex/tool-reliability-20260925`，HEAD `6f799d2ddfa7ab9cbfbe0493823af6db11e918df`。
- 工作树已有其他 agent 的未提交改动；本次测试针对当前工作树，不能称为纯 base 验收。session 三个实现文件与 HEAD 无 diff。
- 已读适用全局、工作树 AGENTS.md 和 RTK.md；`internal/`、`docs/` 未找到更近的 AGENTS.md。Go 为 `go1.26.6 darwin/arm64`，此工作树 go.mod 为 1.26.1。
- 仅新增本报告和 `internal/server/parity_session_test.go`；无实现修改、常驻服务调用、部署、重启、commit/push、联网或子 agent。
- 新增测试复用 `newWorkspaceRuntime`（`workspace_resolve_test.go:18`），MCPX_HOME、SQLite、workspace 都在 t.TempDir；真实 sleep 进程由 Runtime 清理。

## Codex 最接近的工作流（只读）
对照根：`/tmp/mcpx-codex-cli-20260925`，已核实 commit `68e0c9f5d8fd9449e97a81e92e8fcb86795713b2`。
这里对应 app-server 线程生命周期 RPC，没有完全对应 MCPX session 的单一模型侧工具。
| MCPX | Codex 函数与行号（相对对照根） | 对照结论 |
| --- | --- | --- |
| open 新建 | `codex-rs/app-server/src/request_processors/thread_processor.rs:524` `thread_start` | 独立新建 RPC；MCPX 以是否有 remote_session_id 分流。 |
| open 恢复 | 同文件 `:555` `thread_resume`、`:3613` `thread_resume_inner` | 正在关闭时明确报错并提示关闭后重试（`:3622`）；MCPX 返回动态状态和 revisions。 |
| list | 同文件 `:2511` `thread_list_response_inner`；`codex-rs/app-server-protocol/src/protocol/v2/thread.rs:1378` `ThreadListParams` | 均有 cursor/limit；Codex 另有排序、归档等筛选，不要求 MCPX 照搬。 |
| close/archive | `thread_processor.rs:1707` `thread_archive_response`、`:1046` `prepare_thread_for_archive` | Codex 做线程关闭准备；MCPX 约定有 running task 时拒绝 close，语义不同。 |

## MCPX 公开契约及正常路径
- schema：`internal/server/tools_clean_core.go:140-159`，action=open/list/close，可省略；ID 恢复、workspace/path 新建、分页、client_request_id、mode、Git 身份参数。
- 分派：`tools_public_adapters.go:34-60`；无 action 默认 open，有 mode 推导 close；`tools_session_open.go:27-70` 按 ID 新建或恢复。
- 错误恢复：`tools_remote_session.go:136-201`；缺失 ID 返回 NOT_FOUND、精确复制提示和 session(action=list) 恢复动作，现有测试实跑通过。
- 5 项 focused 正常测试均通过：`TestCleanCoreSessionDefaultsToOpenAndResumesSuppliedID`、`TestRemoteSessionResumeUsesCompactPayload`、`TestCleanCoreSessionListDiscoversExistingSession`、`TestRemoteSessionNotFoundExplainsExactCopy`、`TestStreamableHTTPReconnectRestoresRemoteSession`。
- 最后一项用 httptest + MCP SDK 真实 HTTP、注入 503 后重连（`reconnect_acceptance_test.go:15`）；复用已有 fixture，不另建统一 harness。
- compact 测试实际输出：`session_resume_serialized_bytes open=35545 resume=1181 delta=34364`（fixture 序列化值，非独立网络包大小测量）。

## P1：历史前 100 条截断导致运行任务丢失且错误允许关闭
- 根因：`TaskManager.List` 在筛选 running 前按 started_at 截取历史（`internal/terminal/task.go:840-866`）。
- 恢复：`tools_session_open.go:173-186` 只取前 100 条再过滤；结果却标记 `tasks_scope=running`（`:265`），没有补取更早的活跃任务。
- 关闭：`tools_remote_session.go:321-329` 同样只检查前 100 条，然后直接 Close。
- 复现：启动一个真实、仍在运行的临时 sleep；在隔离 DB 填入 99/100 个更新的 exited 任务；经注册的 `session` handler 调用恢复与关闭。
- 最小命令（在上述工作树执行）：`go test ./internal/server -run '^TestParitysessionRunningTaskBeyondHistoryPage$' -count=1 -v`。

```text
completed=99 resume.tasks_scope=running resume.tasks=1 close.status=failed close.error="running_task" live_task.status=running
completed=100 resume.tasks_scope=running resume.tasks=0 close.status=succeeded close.error="" live_task.status=running
resume hid the running task: got 0, want 1
close must reject a live task; got status=ok code=""
--- FAIL: TestParitysessionRunningTaskBeyondHistoryPage
    --- PASS: TestParitysessionRunningTaskBeyondHistoryPage/completed_99
    --- FAIL: TestParitysessionRunningTaskBeyondHistoryPage/completed_100
```

- 日志 close.status 取 fixture 的 public_status；断言里的 ok 是旧测试助手对 succeeded 的归一化值（`acceptance_protocol_test.go:74-100`）。
- 影响：长期开发服务在后续 100 次短任务后从恢复上下文消失；客户端可能误判任务结束或重复启动，并能将实际仍运行的会话关闭。
- 最小修复建议：任务层提供按会话查询全部运行任务及是否存在运行任务的接口；恢复用活跃集合，关闭用不受历史分页影响的存在性检查。不要仅调大 100。查询失败时不应继续关闭（该错误分支未故障注入）。
- 回归保持正确期望，因此当前 FAIL 是已证实缺陷，未改实现或把错误行为写成 PASS。

## 新增 replay 线索与未覆盖
- 当前 `tool_replay.go:294-297` 将 session 的 list/open/省略 action 排除缓存；`observability.go:218`、`tools_remote_session.go:81` 明确排除 session 的 transport 隐式绑定。
- 因而未把“省略 ID 的命令跨 session 混淆”确认为本工具问题；未运行其他工具的跨会话命令试验，交主线统一 replay 验证。
- 已发现 MacMCPX session connector；其本次工具声明不含工作树 schema 的 run_id/git_identity_path/remote_name，无法经该 typed 定义提交身份快照参数。仅记录定义障碍，未绕过契约，完整宿主 schema 比较仍属主线。
- 未验收：常驻 connector 的实际请求、真实鉴权/外部 MCP/用户应用、并发 close/start 竞态、DB 故障注入、异常 cursor、跨进程恢复及其他平台。Codex 仅源码对照，未运行其 Rust 测试。
- 新增测试关闭 MCP/Skills discovery；现有 fixture 使用默认发现配置，测试通过不代表任何外部服务通过。

## 本次最终 focused 运行
`go test ./internal/server -run '^(TestParitysessionRunningTaskBeyondHistoryPage|TestCleanCoreSessionDefaultsToOpenAndResumesSuppliedID|TestRemoteSessionResumeUsesCompactPayload|TestCleanCoreSessionListDiscoversExistingSession|TestRemoteSessionNotFoundExplainsExactCopy|TestStreamableHTTPReconnectRestoresRemoteSession)$' -count=1 -v`

结果：5 个现有测试 PASS；新增测试 99 条子项 PASS、100 条子项 FAIL；`FAIL mcpx/internal/server 1.840s`，退出码 1。新增 Go 文件 gofmt 检查无输出。
