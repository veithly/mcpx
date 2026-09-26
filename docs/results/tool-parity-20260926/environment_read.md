# environment_read 逐工具对标审查

- 日期：2026-09-26；结论：正常读取与错误恢复通过，确认 2 项 P2 问题；本 agent 未修改实现。
- 工作树：/Users/rick/.codex/worktrees/mcpx-tool-reliability/mcpx。
- 分支：codex/tool-reliability-20260925；审查时 HEAD/base：6f799d2ddfa7ab9cbfbe0493823af6db11e918df。
- Codex：/tmp/mcpx-codex-cli-20260925，68e0c9f5d8fd9449e97a81e92e8fcb86795713b2，只读源码。
- 已读适用 AGENTS.md、RTK.md；复用隔离 Runtime + MCP SDK Streamable HTTP fixture。
- 审查时共享工作树已有其他 agent 改动；当时本工具 catalog/adapter/handler/inspect 文件与 base 的 diff 为空。
- 本次仅新增专属测试及本报告；不操作常驻服务、不重启/部署/提交，不重复主线宿主 schema/harness 工作。

## Codex 最接近的实现

以下路径相对上述 Codex checkout；仅源码对照，未运行 Rust 测试。
- `codex-rs/core/src/context/world_state/environment.rs:41`，`EnvironmentsState::from_turn_context_with_environments`：从 TurnContext 生成环境上下文，包含 shell、日期、时区、文件系统/网络约束。
- 同文件 `:108` 的 `snapshot`、`:135` 的 `render_diff`：保存并比较模型可见环境状态，按变化更新上下文。
- `codex-rs/core/src/tools/handlers/unified_exec/exec_command.rs:155`，`ExecCommandHandler::handle_call`；`:186-204` 解析环境并确定 workdir，可用显式命令获取工具版本。
- `codex-rs/core/src/tools/handlers/shell_spec.rs:24`，`create_exec_command_tool_with_environment_id`：命令接口提供 workdir、yield_time_ms、max_output_tokens。
- 未发现直接等价的 `environment_read` 公共工具：Codex 的环境上下文差分与显式命令探测是近似工作流，不提供 MCPX 的持久快照 ID 比较契约。

## MCPX 契约与实际调用

以下路径相对 MCPX 工作树。
- 公开 schema：`internal/server/tools_catalog.go:358-361`；remote_session_id/workspace/view/sections/snapshot_id 均可省略，view=current|compare，sections 为 8 个枚举分区。
- `internal/server/tools_public_adapters.go:282-301`：默认 current；提供非空 snapshot_id 推导 compare；始终设置 save_snapshot=false。
- `internal/server/tools_environment.go:17-93`：解析 workspace/session、分区、采集、快照读取与授权、比较、结果封装；compare 之前先全量采集。
- 新测试复用 `newWorkspaceRuntime`（`workspace_resolve_test.go:18`），配置/SQLite/Workspace 全在 t.TempDir；HTTP 仅监听 httptest 临时端口。
- PATH 指向临时目录；工具版本测试只运行本测试创建的假 go 脚本，不调用真实工具链或外部应用。
- 当前读取真实输出：`current: status=succeeded runtime_present=true toolchains_present=false snapshot_present=false`。
- 隔离数据库预置合成快照后，以 snapshot_id 隐式 compare：`inferred compare: status=succeeded base_matches=true`。
- 不存在快照：`missing snapshot: status=failed isError=true error=ENVIRONMENT_SNAPSHOT_NOT_FOUND`。
- 无效分区：`invalid section: status=failed isError=true error=INVALID_ARGUMENTS`。
- 同一 HTTP session 随后正常读取：`recovery: status=succeeded`，错误没有破坏后续调用。

## P2：compare 缺少 snapshot_id 仍返回成功

- 最小请求：`environment_read({"view":"compare","sections":["runtime"]})`。
- 复现：在上述工作树执行 `go test ./internal/server -run '^TestParityenvironment_readCurrentCompareAndRecovery$' -count=1 -v`。
- 真实输出：`compare without snapshot: status=succeeded isError=false comparison_present=false`。
- 回归断言失败：`compare without snapshot_id must fail rather than return current environment as success`。
- 根因：adapter `tools_public_adapters.go:285-288` 只在字段存在时转发，handler `tools_environment.go:56-57` 仅在 compare_to 非空时比较；没有必填校验。
- 影响：客户端以为比较成功，却收不到 comparison，也收不到可纠正参数的错误，可能误判或重复请求。
- 最小修复：compare 分支在任何探测前要求非空 snapshot_id，返回 INVALID_ARGUMENTS；同步 schema 条件约束/说明，保持隐式 compare。

## P2：runtime 分区读取仍执行无关工具链探测

- 最小请求：`environment_read({"sections":["runtime"]})`；PATH 中放本测试的假 go，执行时在 t.TempDir 写标记。
- 复现：在上述工作树执行 `go test ./internal/server -run '^TestParityenvironment_readRuntimeOnlySkipsToolchains$' -count=1 -v`。
- 真实输出：`runtime-only: status=succeeded unrelated_go_probe_executed=true`；该次日志 processing_ms=384，仅为本次观测值。
- 回归断言失败：`runtime-only read executed a toolchain version probe although snapshots are disabled`。
- 根因：`tools_environment.go:49-54` 无条件 Inspect(..., nil) 后裁剪；只读 adapter 已禁用持久化，仍沿用全量快照采集。
- `internal/environment/inspect.go:293-334` 会探测 PATH 中找到的工具；`:337-353` 每次外部命令设 1500ms 上限。
- 影响：轻量读取无故依赖工具版本进程，增加等待与失败面；本次只确认多余进程及耗时，没有宣称复现死锁或无限等待。
- 最小修复：current 且 save_snapshot=false 时直接 Inspect(ctx, workspacePath, sections)；完整快照/比较仍保留必要的全量采集。

## 执行结果、通过项与未覆盖项

- 实际运行：`go test ./internal/server -run '^(TestParityenvironment_read|TestEnvironmentReadCanonicalizesView|TestEnvironmentSchemasExposeOnlySemanticInputs)' -count=1 -v`。
- 本 agent 修复前实测结果：退出 1，包耗时 1.326s；既有 canonicalization/schema 两项 PASS；新测试正常读取/比较/错误恢复检查通过，上述两个缺陷断言 FAIL。
- 已 gofmt；专属文件 `git diff --check` 无输出。保留红灯回归供主线修复，未修改其他实现或测试。
- 已发现 `mcp__codex_apps__macmcpx_environment_read`，定义包含本工具所需字段；未实际调用常驻连接器，不声称宿主协议已验收。
- 未覆盖：真实工具链可用性、探测超时/子进程泄漏、Windows/Linux、远程会话跨主体授权、常驻服务/宿主 schema 一致性。
- 未读取真实凭证文件、私人屏幕；文件/命令行为测试只使用临时目录；无外网依赖。
- 交付文件：`internal/server/parity_environment_read_test.go`、`docs/results/tool-parity-20260926/environment_read.md`。

## 最终交接

- 据用户本轮反馈，主线第三批红测已复现上述两项 environment_read 缺陷，并接手统一修复。
- 本报告保留修复前实测证据；不代表主线修复后的当前结果，本 agent 未执行修复后复测。
- 审查到此收敛，仅交付本报告与专属回归测试；不再扩展调查、增加测试或修改实现。
- 后续由主线完成最小修复并运行本报告中的 focused 测试验收。
