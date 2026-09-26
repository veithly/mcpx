# observe 逐工具对标审查

结论：正常 Task 查询、双流 offset 读取、错误 ID／跨 Session 拒绝通过；确认两个问题：**P1 历史 Task 恢复后日志被静默当作空流**、**P2 日志分页切断 UTF-8 字符**。仅添加本工具测试与报告，未修改实现。主线已接手恢复日志修复；以下结果是主线修复前的实际运行证据，不代表修复后的验收。

## 范围与环境

- 工作树：`/Users/rick/.codex/worktrees/mcpx-tool-reliability/mcpx`，分支 `codex/tool-reliability-20260925`，HEAD `6f799d2ddfa7ab9cbfbe0493823af6db11e918df`，Go `1.26.6 darwin/arm64`。保留主线既有未提交改动；本次测试运行于该共享工作树。
- Codex 只读对照：`/tmp/mcpx-codex-cli-20260925`，commit `68e0c9f5d8fd9449e97a81e92e8fcb86795713b2`。没有联网或重新 clone。
- 复用 `newWorkspaceRuntime`（`internal/server/workspace_resolve_test.go:18`）：配置、数据库、工作区、日志均在 `t.TempDir`；由 `httptest.NewServer` 加 MCP SDK 发出真正 `observe` 调用。任务用直接进程 fixture 产生，未调用常驻服务、用户外部应用或登录 shell。
- 不涉及宿主 schema 总体对比、统一端到端 harness、其他工具修复、部署、重启、commit/push。

## Codex 对照与 MCPX 入口

| 行为 | Codex 证据（路径相对上述 clone） | MCPX 证据 |
|---|---|---|
| 查询既有执行并读增量输出 | 最接近 `write_stdin(chars="")`；`codex-rs/core/src/tools/handlers/shell_spec.rs:117` 的 `create_write_stdin_tool`；`tools/handlers/unified_exec/write_stdin.rs:23` 的 `WriteStdinArgs`、`:58` 的 `handle_call`（均相对 `codex-rs/core/src/`） | `internal/server/tools_observe.go:14` 分发，`:56` 推导 view；`tools_execute.go:124` 的 `toolObserveTask`；`tools_command_execute.go:897` 的 `toolTaskManage` |
| 分页与并发 | `codex-rs/core/src/unified_exec/process_manager.rs:883` 的 `write_stdin_inner`，`:890` 同进程交互加锁，`:1018` 限制轮询等待，`:1579` 的 `collect_output_until_deadline`、`:1614` 消费 pending 缓冲 | `internal/server/tools_command_execute.go:943` 读 logs，`:999` 的 `taskResultData` 返回双流字节 offset；`internal/terminal/task.go:647` 每次最多 256 KiB |
| 生命周期与历史读取 | `process_manager.rs:1133` 的 `refresh_process_state` 在进程结束时移除执行句柄；该执行工具没有 MCPX 的持久化 Task 日志 offset 契约 | `internal/terminal/task.go:559` 的 `TaskManager.Get` 从 SQLite 恢复 Task；MCPX 承诺可再次读历史日志，不能直接照搬 Codex 生命周期 |

没有发现与 MCPX 全部 `session/plan/history/diff` 视图直接一一对应的单一 Codex 工具；本审查仅对标 Task／logs 工作流。Codex 仅静态阅读，没有运行 Rust 测试，因此不宣称其所有 Unicode 输出边界已通过。

公开 schema 位于 `internal/server/tools_clean_core.go:298–321`：view 包含 `session/task/plan/history/logs/diff`，日志接受 `stdout_offset/stderr_offset`，目标唯一时可省略 view。缺少 Task ID、错种 ID、非所属 Task 的处理分别见 `tools_execute.go:126`、`tools_command_execute.go:924–929`。ARC 在 `internal/arc/arc.go:591` 的 `actionsFrom`、`:657` 的 `canonicalAction` 将 handler 的 `next_action` 转为顶层 `actions`，测试按实际 wire 契约续读。

已通过工具目录发现 `mcp__codex_apps__macmcpx_observe`。当前连接器声明的 view 只有 `session/task/plan/history/logs`，没有 `diff/edit_id/offset`，且要求 `remote_session_id`；这里只记录目录证据，不重复主线宿主 schema 对比、不调用真实账号、不用其他工具绕过契约。

## 实际协议调用与问题

完整本地输出：`/tmp/mcpx-parity-observe-20260926.log`。以下单项命令均显式固定工作树；聚合运行结果是 **1 PASS、2 FAIL，退出码 1**。两个 FAIL 是针对期望正确行为保留的回归断言，未反向断言缺陷。

### 通过：Task、offset 和目标错误

```sh
go -C /Users/rick/.codex/worktrees/mcpx-tool-reliability/mcpx test ./internal/server -run '^TestParityobserveNormalTaskLogsAndErrors$' -count=1 -v
```

实际聚合运行输出：

```text
task: status=succeeded task_status=exited outcome=error exit_code=7
logs: status=succeeded stdout="中文" stderr="error" offsets=11/13
invalid target: status=failed code=execution_task_id_required
invalid target: status=failed code=execution_task_not_found
invalid target: status=failed code=execution_task_not_found
--- PASS: TestParityobserveNormalTaskLogsAndErrors (0.11s)
```

证据：`internal/server/parity_observe_test.go:73`。进程确实以 7 退出，查询成功与执行失败状态区分正确；stdout 从字节 5、stderr 从字节 8 读取准确；缺 ID、不存在 ID、另一个 Session 持有的 ID 均拒绝。日志中的 code 是测试 helper 转为小写后的值；wire 原值为大写。

### P1：恢复 Task 后，磁盘仍有日志但 observe 返回成功空流

```sh
go -C /Users/rick/.codex/worktrees/mcpx-tool-reliability/mcpx test ./internal/server -run '^TestParityobserveRestoredTaskLogs$' -count=1 -v
```

步骤：临时 `/usr/bin/printf` 输出 24 字节；HTTP `observe(view=logs)` 可读；直接检查临时 stdout 文件仍有完整字节；仅重建隔离 TaskManager，使用同一 SQLite 数据库走真实 `Get` 恢复路径；再次以相同 Task ID 发出 HTTP observe。没有重启任何服务。

```text
restore: before_stdout="persisted-observe-output" disk_bytes=24 status=succeeded task_status=exited stdout="" next_offset=0 actions=<nil>
restored task silently lost persisted stdout
--- FAIL: TestParityobserveRestoredTaskLogs (0.07s)
```

证据：测试 `parity_observe_test.go:106`；根因 `internal/terminal/task.go:568–580` 只恢复 combined `log.size`，stdout/stderr 只恢复 path，size 仍为 0；`:200–212` 的 `fileLog.read` 在打开文件前即将读取窗口判为空。`:697–698` 返回 0 流长，所以上层也不给续读 action。

影响：恢复后的已完成 Task 无法经公开日志接口获取本来存在的输出，返回 `succeeded` 且无后续动作，重试同一查询无法恢复证据。最小修复：恢复每条流的有效长度；未发生 ring 覆盖的日志可由文件信息恢复，ring 流须有正确的逻辑长度／游标元数据，不能把物理文件长度当作逻辑长度。主线已接手；本审查仅验证普通 24 字节日志，不宣称 ring 恢复已验收。

### P2：原样跟随日志续读 action 仍损坏中文

```sh
go -C /Users/rick/.codex/worktrees/mcpx-tool-reliability/mcpx test ./internal/server -run '^TestParityobserveUTF8LogContinuation$' -count=1 -v
```

步骤：`/bin/cat` 接收有限 stdin，内容为 `a × 262143 + 中文tail`；HTTP 读首段后，直接将 wire 顶层 `actions[0].arguments` 传给下一次 observe。

```text
UTF8: first_status=succeeded second_status=succeeded offsets=262144/262153 first_tail="aaaaaaaaa�" second="��文tail" input_bytes=262153 joined_bytes=262159
following the server's exact next_action corrupts UTF-8 output
--- FAIL: TestParityobserveUTF8LogContinuation (0.14s)
```

证据：测试 `parity_observe_test.go:136`；`internal/terminal/task.go:647–666` 按固定 256 KiB 读取并直接转 string，将 `中` 的三个字节分为首段一个、次段两个；JSON 输出把非法 UTF-8 片段替换成 `�`。`internal/server/tools_command_execute.go:944–950` 用原始字节位置生成续读，客户端无法通过正常拼接还原字符。

最小修复：文本日志分段应只返回完整 UTF-8 字符，续读 offset 按实际消费的原始字节推进，把段尾未完成字符留给下一次读取。应在日志分段源头处理；仅对已损坏的 string 再截断无法还原。此次未验证任意二进制流或人为指向字符中间的 offset，不额外扩大契约。

## 交付与未覆盖

- 新增 `internal/server/parity_observe_test.go`，三个测试均已运行；原正常用例中 helper 大小写及原 UTF-8 用例中的 action 读取位置已修正，上述输出均来自修正后的运行。
- 新增本报告；未修改任何实现源文件。修复后主线可运行 `go -C /Users/rick/.codex/worktrees/mcpx-tool-reliability/mcpx test ./internal/server -run '^TestParityobserve' -count=1 -v` 验收。
- 未覆盖：Plan、diff、history 搜索分页、32 MiB ring 覆盖、运行中任务恢复、Windows、真实常驻服务和外部连接器协议调用。没有执行全套测试或 Codex Rust 测试。
