# MCPX 原生编程工具链移植

当前指令取代此前委托 Codex CLI/exec-server 的方案。工作目录固定为
`/Users/rick/.codex/worktrees/mcpx-tool-reliability/mcpx`，分支为 `codex/codex-toolchain`。
保留已有未提交改动，不编辑旧 main，不 commit、push、部署或重启常驻服务。

## 目标与范围

`exec_command`、`write_stdin`、`apply_patch` 的核心实现在 MCPX 内部运行。
参照本地 `/tmp/mcpx-codex-cli-20260925` 的固定源码 commit
`68e0c9f5d8fd9449e97a81e92e8fcb86795713b2`，将选定逻辑移植到 Go。
不得通过 Codex 可执行文件、exec-server、模型或 API 提供核心能力。
禁止外部可执行文件 wrapper 及转调它们的 fallback 设计。

本工作项仅拥有 README、HTTP probe、CI、tools/guidance YAML、本计划、新验证报告及
`third_party/**`。backend、server Go、go.mod 由主线负责；不改 AGENTS，不启动子 agent。
整仓重构验收、完整 sandbox、模型 agent、部署和历史兼容均不在本工作项范围内。

## 目标架构

- `internal/unifiedexec`：MCPX 本地进程启动、pipes/PTY、stdin、整数句柄、等待与输出缓冲。
- `internal/patch`：Codex 格式补丁的解析、匹配与 Add/Update/Delete/Move 文件操作。
- `internal/server/tools_programming.go`：认证、Remote Session、Workspace、权限与 console
  审批；三个编程工具直接返回 output/exit_code/session_id，不套 ARC 或自动重放。
- `internal/server/programming_test.go` 和 `programming_evidence_test.go`：
  以 `TestProgramming` 为共同前缀的真实 server 集成测试。
- 保留 `process_sessions`；移除外部执行器的 Binary/Version 依赖与版本状态字段。

MCP 的 `apply_patch` 用 `input` 字符串包装原始补丁；可选 `remote_session_id` 是路由
上下文。MCPX 保留自身权限边界；选定源码逻辑的 Go 移植不代表全面逐行等价，
也不包含上游完整 sandbox、审批编排或模型 agent。

## 验证顺序

1. 本工作项先检查 Python syntax、YAML 加载、CI 命令与旧依赖残留。
2. 主线 backend ready 后运行 `go test -p 1 ./internal/unifiedexec ./internal/patch -count=1`。
3. 运行 `go test -p 1 ./internal/server -run '^TestProgramming' -parallel 2 -count=1`，
   包括 Evidence，不能因 Codex 缺失而 skip。
4. 构建 MCPX candidate，再运行 `scripts/toolchain-probe.py --binary <candidate> --output-dir <isolated-output>`；
   用临时状态和真实 MCP HTTP 测试三核心。
5. smoke 的服务 PATH 仅含所需系统命令与 `codex`、`codex-exec-server`、`apply_patch`
   三个失败陷阱。任一调用都写标记并令最终结果失败；不查找真实 Codex，不设置 Codex 环境变量。
6. 非支持平台或缺少必需能力应失败，不能用 skip/空测试伪装验收。

差分验证只有在用户显式选择外部 baseline 并指定路径及版本后才可运行 Codex；
默认 CI、必跑测试和 HTTP smoke 不探测、不启动外部 baseline。本 probe 不提供差分模式。

## 来源与交付证据

`third_party/codex/LICENSE` 和 `NOTICE` 原样保留上游文本；`SOURCE.md` 固定 commit，
列出移植逻辑的上游路径、目标包、Go 改写与省略范围。不 vendor 整个上游仓库。
每个复制或翻译上游源码的实现文件需由其实现负责人附 derived-from 路径、commit、
OpenAI 版权和修改说明；本工作项不越权改 backend Go。

验收证据写入 `docs/results/2026-09-26-native-toolchain-port.md`。主线在 backend ready 后
填写实际命令、平台、结果和限制；此前委托 Codex 二进制的报告不构成本次原生实现的证据。
