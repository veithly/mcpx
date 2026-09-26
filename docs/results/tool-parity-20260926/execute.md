# execute 工具对标审查

审查日期：2026-09-26。范围仅为 execute，按用户最后指示收敛为一个确认问题及通过用例。

## 范围与证据边界

- 工作树：/Users/rick/.codex/worktrees/mcpx-tool-reliability/mcpx。
- 分支：codex/tool-reliability-20260925；起点 HEAD：6f799d2ddfa7ab9cbfbe0493823af6db11e918df。
- Codex 只读参照：/tmp/mcpx-codex-cli-20260925，commit 68e0c9f5d8fd9449e97a81e92e8fcb86795713b2。
- 实测使用 Go 1.26.6、darwin/arm64；当前起点 go.mod 声明 go 1.26.1。
- 所有行为测试均经临时 Runtime、临时 Remote Session、httptest Streamable MCP HTTP、官方 SDK ClientSession.CallTool。MCPX_HOME、HOME 指向临时目录，清空 BASH_ENV/ENV；进程工作目录为临时注册 Workspace。没有访问真实凭证、屏幕或外部应用，没有调用常驻服务，没有重启、部署、commit/push。
- 本 agent 仅新增 internal/server/parity_execute_test.go 和本报告。测试期间主线并行修改 tool_replay/tool_response，因此实测不是 pristine 6f799d2；本项根因所在 tools_command_execute.go、terminal/task.go 在取得下述红测时仍为审查原实现。
- 主线随后告知已修复 stdin 持有生命周期锁。本 agent 按要求立即交付，**没有复跑修复后代码，不将其记为已验收**。下文均为修复前证据和行号。
- 无显式 idempotency_key 的旧执行结果回放、宿主 schema 比对和全工具 harness 由主线负责，本报告不重复归因。

## Codex 最近对应工具/工作流

MCPX execute 最接近 Codex exec_command + write_stdin 的组合，不对应 JavaScript 编排工具 functions.exec。Codex 原生接口没有与 MCPX 四个 action 完全一一对应的单工具入口。

以下 Codex 路径均相对于上述只读 clone：

| 场景 | Codex 函数和位置 | MCPX 对照及差异 |
|---|---|---|
| 发起命令 | codex-rs/core/src/tools/handlers/shell_spec.rs:24，create_exec_command_tool_with_environment_id；:96 发布 exec_command；unified_exec/exec_command.rs:155，ExecCommandHandler::handle_call | tools_catalog.go:366–406 发布 execute；tools_execute.go:18，toolExecute 分发；tools_command_execute.go:35，toolCommandExecute。MCPX 需要 remote_session_id/purpose，支持 command、argv+shell=false、project task、runtime+script 四种输入。 |
| 长命令返回可续接 ID | codex-rs/core/src/unified_exec/process_manager.rs:508，exec_command；:526，exec_command_inner；:636 限制初次等待时间 | tools_command_execute.go:302，executeCommandTask；:339 等待；:387–409 未结束返回 accepted、execution_task_id 和下一步。 |
| 续读和交互输入 | shell_spec.rs:118，create_write_stdin_tool；unified_exec/write_stdin.rs:58，WriteStdinHandler::handle_call；process_manager.rs:867/:883，write_stdin/write_stdin_inner | MCPX 用 attach 续读、stdin 写入，tools_command_execute.go:954–993；stdout/stderr 分开且采用显式字节 offset。Codex write_stdin 空输入轮询；非空输入不直接等同于 MCPX 行为。 |
| 停止、输入边界 | process_manager.rs:989–1015：非 TTY 普通输入返回 StdinClosed，interrupt 控制字符可走 process.interrupt；TTY 才 process.write | MCPX 普通 command 启动时保留交互管道，terminal/task.go:314–321；execute 有独立 stop action，tools_command_execute.go:973–978 调用 Task.Kill。Codex 没有相同名称的独立 stop action。本次未实际运行 Codex，不能由代码对照推断其所有背压情况均通过。 |
| 输出和预算 | codex-rs/core/src/tools/context.rs:463–471，ExecCommandToolOutput 序列化 chunk_id、exit_code、session_id、output；:481–488，model_output_policy | MCPX structuredContent 提供 status/data、exit_code、outcome、分离 stdout/stderr；tools_command_execute.go:413，commandOutputText；:448，capTaskExecutionOutput。这里只验证小输出，没有验收大输出预算。 |

表中省略前缀的 Codex shell_spec.rs、unified_exec/*.rs 均位于 codex-rs/core/src/tools/handlers/；process_manager.rs 位于 codex-rs/core/src/unified_exec/。MCPX server 文件位于 internal/server/，terminal 文件位于 internal/terminal/。

公开 schema 的证据：tools_catalog.go:366–406 定义 run/attach/stop/stdin；run 要求会话和 purpose，attach 要求会话和 execution_task_id，stop 另需 purpose，stdin 另需 input。tools_execute.go:27–32 将 attach 作为实时查询、将 stop/stdin 交给写操作包装。ALL_TOOLS 可发现 MacMCPX execute connector，但本 agent 未调用它；宿主定义是否过时留给主线，不凭此声称有 connector 缺字段障碍。

## 确认问题：P1，stdin 管道背压阻塞同一 Task 的 stop

### 最小复现

在隔离会话中依次发起下列 MCP tools/call；所有工具名均为 execute：

1. run：command="exec /bin/sleep 2"、yield_time_ms=1、purpose 描述临时背压验证。返回 accepted 和 execution_task_id。
2. 并发 stdin：同一 execution_task_id，input 为 256 KiB 的字母 x。子进程不读取 stdin，写入超过管道容量。
3. 150ms 后 stop：同一 execution_task_id，purpose 描述停止临时进程。

对照组启动 "exec /bin/sleep 3"，不发送 stdin，直接 stop。

可重复执行的最小测试命令（执行时指定本工作树为 workdir）：

~~~bash
go test ./internal/server -run '^TestParityexecuteBlockedStdinDoesNotBlockStop$' -count=1 -v -timeout=30s
~~~

本 agent 实际运行的是同时包含正常基线的 focused 命令：

~~~bash
go test ./internal/server -run '^TestParityexecute' -count=1 -v -timeout=30s
~~~

### 修复前真实输出

最终一次运行的完整日志在 /tmp/mcpx-parity-execute-test.log；以下保留关键原始输出，避免临时日志清理后丢失证据：

~~~text
=== RUN   TestParityexecuteNormalAndFailureViaHTTP
    parity_execute_test.go:68: literal: status=succeeded stdout="中文 $(literal); & >" exit_code=0
    parity_execute_test.go:76: nonzero: status=failed error=process_exit stderr="parity-stderr" exit_code=7
    parity_execute_test.go:98: stdin+attach: status=succeeded outcome=succeeded stdout="received:hello" exit_code=0
--- PASS: TestParityexecuteNormalAndFailureViaHTTP (0.12s)
=== RUN   TestParityexecuteBlockedStdinDoesNotBlockStop
    parity_execute_test.go:115: control stop waited=14ms status=succeeded
    parity_execute_test.go:150: stop waited=1.867s status=succeeded; stdin: status=failed error=stdin_unavailable
    parity_execute_test.go:152: stop waited for the non-reading child to exit instead of stopping it: 1.866530208s
--- FAIL: TestParityexecuteBlockedStdinDoesNotBlockStop (2.09s)
FAIL
FAIL    mcpx/internal/server    2.733s
FAIL
~~~

正常 stop 为 14ms；仅加入 stdin 背压，stop 变为约 1.87s，与两秒睡眠自行结束相吻合。前一轮同一背压调用也实测约 1.868s。最终退出码为 1，红测被保留给主线修复后验收。

### 根因与实际影响

- internal/server/tools_command_execute.go:988–993 的 stdin 分支同步调用 task.WriteStdin(input)，不向其传入可取消 context。
- internal/terminal/task.go:958–965 的 Task.WriteStdin 在 t.mu.Lock 后一直持锁到 io.WriteString 返回。
- internal/terminal/task.go:744 起的 Task.Kill 首先获取同一个 t.mu；logsFor(:648 起) 也使用此锁。
- 因此 pipe 写满且子进程不消费时，stop 无法获取生命周期锁并终止子进程。本次直接验收的是 stop 延迟；其他查询是否超时、长期 worker 是否耗尽没有进行压力测试，不当作已证明的额外问题。
- agent 常用 stop 恢复交互卡住的任务，而该恢复动作本身也被阻塞。无限不读输入的进程可能把阻塞延长；本次用两秒自退出 fixture 控制测试副作用，没有制造永久挂死。

### 建议最小修复

在 Task 生命周期锁内只校验运行状态和取出 stdin 引用，释放该锁后再写 pipe。需要串行写入时使用独立 stdin 写锁，不阻挡 Kill/状态读取；停止流程应能够关闭管道或终止进程以唤醒写入。继续向工具返回准确写入错误；不要仅提高外层超时或建议模型重复 stdin。

修复后的验收条件是：正常基线仍通过；加入 256 KiB 背压后 stop 能及时完成、无需等子进程自然退出，原 stdin 调用也能结束。主线已告知做了生命周期锁修复，具体最终实现和复跑结果由主线记录。

## 通过项

TestParityexecuteNormalAndFailureViaHTTP（parity_execute_test.go:59 起）经过真实 MCP HTTP 验证：

- argv+shell=false 原样传递中文和 shell 特殊字符：stdout 精确为 "中文 $(literal); & >"，exit_code=0，status=succeeded。
- command 非零退出保持业务失败：stderr="parity-stderr"，exit_code=7，status=failed，error code 为 PROCESS_EXIT（测试日志用 errorCode 辅助函数转成小写）。
- 短 yield 返回 accepted + execution_task_id；小输入 "hello\n" 写入后 attach 返回 stdout="received:hello"、outcome=succeeded、exit_code=0。
- 同一背压回归中的无背压对照 stop 成功，最终实测 14ms。

这些是协议调用结果，不是仅凭内部函数返回值推测。

## 未覆盖和交接

- 本轮只交付上述一个问题。确认重试参数检查未完成，且 HTTP 返回的确认提示会被归一化；不将内部恢复参数的静态疑点列为缺陷。输出预算与大输出续读未完成验收，主线接手。
- 未验收 Windows、TTY 控制字符、Python/Node/SQLite 外部运行时、project task 发现、服务重启后的恢复、长期资源耗尽和大输出压力。
- 未运行 Rust/Codex 可执行验证；Codex 证据限固定 commit 源码对照。
- 未运行全套 Go 测试、race、CI 或部署验证；仅完成上述 focused HTTP 测试和测试文件 gofmt。
- 修改路径：internal/server/parity_execute_test.go、docs/results/tool-parity-20260926/execute.md。没有修改实现源码，没有覆盖其他 agent 的文件。
