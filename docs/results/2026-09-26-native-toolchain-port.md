# MCPX 原生编程工具链验证记录

**状态：主线原生验收通过，尚未提交、推送或部署。** 本记录对应源码逻辑移植，不对应此前委托 Codex CLI/exec-server
的方案。旧 `2026-09-26-codex-toolchain-refactor.md` 仅作为历史记录，其通过结果不得
复制为本次原生 backend 的完成证据。本文不宣称全面逐行等价、完整 sandbox 或模型 agent。

工作树：`/Users/rick/.codex/worktrees/mcpx-tool-reliability/mcpx`。
分支：`codex/codex-toolchain`。
上游源码参考：`68e0c9f5d8fd9449e97a81e92e8fcb86795713b2`。

## 本工作项已交付

- README、tools/guidance、计划改为 `internal/unifiedexec` / `internal/patch` 本地源码移植，
  明确禁止外部 Codex/exec-server/apply_patch wrapper 或 fallback。核心名称仍为
  `exec_command`、`write_stdin`、`apply_patch`，保留 `process_sessions` 语义。
- CI 移除 Node/npm 安装 Codex 和 Codex 版本检查，改跑 native 包与 `^TestProgramming`
  server 集成（含 Evidence），构建后执行 native HTTP smoke 并保存输出。
- HTTP probe 仅依赖 Python 标准库和 MCPX candidate；在临时 HOME、MCPX_HOME、Workspace
  与最小 PATH 下调用真实 HTTP 三核心。PATH 中 `codex`、`codex-exec-server`、`apply_patch`
  均为写标记并返回 97 的失败陷阱；调用任一即失败，不容许吞掉错误后报告通过。
- probe 不查找真实 Codex，不设置 Codex 环境变量，不调用模型/API，不操作真实用户文件
  或常驻服务。不支持的平台失败而非 skip；不提供默认或隐式的外部 baseline。
- `third_party/codex` 保存上游原文 LICENSE、NOTICE 及 SOURCE.md。源码来源清单仅说明
  移植的选定逻辑；实现文件的 derived-from、commit、版权/修改注释由实现负责人维护。
- 历史委托报告只新增一段 superseded 标记，原正文保留。

## 本工作项验证

| 检查 | 结果 / 证据 |
| --- | --- |
| 工作树与分支 | 已读取确认是上述工作树 / 分支；保留既有未提交改动 |
| 上游 commit | `git rev-parse HEAD` 与固定 commit 一致 |
| LICENSE / NOTICE | 与参考 checkout 的根文件逐字节一致；SHA-256 见下 |
| Python syntax | 通过：用 Python `compile()` 编译 probe 源码；未启动 candidate |
| prompts/guidance focused tests | 通过：`go test -p 1 ./internal/server/prompts ./internal/server/guidance -count=1`，两包均 ok |
| CI YAML 与命令语法 | 通过：Ruby 标准库 YAML 加载、所有 run 块 `bash -n`，确认 native backend / `^TestProgramming` / HTTP smoke 入口 |
| 隔离环境 / 三个失败陷阱 | 通过：在含空格与单引号的临时路径构造隔离环境，三个假命令分别写入自身名字并返回 97；不等于 HTTP smoke 通过 |
| 不支持平台 | 通过：focused 调用将平台模拟为 win32，报告 failed、checks 为空、返回 1，没有启动服务 |
| 旧依赖残留 | 当前拥有文件中没有 Codex 安装、查找、环境依赖或旧测试前缀入口；历史报告正文按要求保留 |
| 修改文件 whitespace | 通过：限定拥有路径的 `git diff --check`；新文件另做 trailing whitespace / EOF 检查 |

上述 focused 检查运行于 macOS（2026-09-26）。三条陷阱命令仅为本 probe 创建的临时 shell
脚本，验证中未启动真实 Codex、exec-server、外部 apply_patch 或 MCPX candidate。

上游原文 SHA-256：

```text
LICENSE d17f227e4df5da1600391338865ce0f3055211760a36688f816941d58232d8dc
NOTICE  9d71575ecfd9a843fc1677b0efb08053c6ba9fd686a0de1a6f5382fd3c220915
```

## 主线最终验收（2026-09-26）

三个 sub-agent 分别完成原生进程管理、补丁源码移植、依赖清理与许可证。主线移除 server 对旧 adapter 的引用，改用 unifiedexec/patch，移除 Binary/Version 参数与能力字段。旧 internal/codexexec 和 internal/codexpatch 均已删除；核心没有外部 Codex fallback。

| 验收项 | 命令 / 判据 | 结果 |
| --- | --- | --- |
| Native backend | `go test ./internal/unifiedexec ./internal/patch -count=1` | 通过；默认不执行差分基准 |
| Server 集成与 Evidence | `go test ./internal/server -run '^TestProgramming' -count=1` | 通过，含真实 HTTP、PTY、审批、文件修改和交付证据 |
| 无外部 binary 依赖 | 必跑测试与 smoke 的 PATH 中放置三个失败陷阱 | 通过；启动、工具调用、退出均无陷阱标记 |
| 全仓库 | `go test ./... -count=1` | 38 个有测试包通过，0失败 |
| 竞态 | native 两包及 server 编程/审批/停止 focused race | 通过；执行层13个顶层测试，补丁19个顶层含可选差分 |
| Candidate 构建 | Go 1.26.6 darwin/arm64；普通和 CGO_ENABLED=0 | 通过，mcpx-local签名和strict验证通过 |
| 真实 HTTP smoke | `python3 scripts/toolchain-probe.py --binary bin/native-port/final/mcpx-server --output-dir bin/native-port/final/probe` | 8项通过，backend=mcpx-native |
| 平台范围 | 完整 MCPX CGO_ENABLED=0 Linux amd64 / Windows amd64 交叉构建 | 通过；macOS运行验收，跨编译不等于其它平台实跑 |
| 源码归属复核 | SOURCE.md / 源文件出处注释及上游fixture | 已保存Apache-2.0、NOTICE、固定commit与修改说明 |
| 可选外部差分 | 显式 MCPX_PATCH_DIFFERENTIAL_BINARY=/opt/homebrew/bin/codex；Codex0.155.1 | 190个差分子项通过（字节/输出/退出码）；不构成生产依赖 |

最终普通构建文件：bin/native-port/final/mcpx-server；SHA-256：90b9007724fc626fec7cb5fed00ce958f58a2a0ee9f6b29328a171b7be3de9b6。结果与日志位于 bin/native-port（gitignore）：final-all.log、native-race.log、server-race.log、patch-differential.log、final-vet.log、final/probe/probe-result.json。

本轮修复过程中真实发现macOS PTY父进程退出时进程组SIGKILL偶发EPERM；参照上游进程组处理，在组信号被拒绝后枚举并复核组成员再终止，未把失败跳过。

已知差异：Windows原生PTY尚未移植，tty=true明确ErrUnsupported；普通Windows进程使用Job Object。MCPX的workspace/权限校验不是完整Codex OS sandbox。补丁用os.Root限定文件访问，不能排除别的进程同时写同一inode；非local environment header明确拒绝。默认NormalizeToLF遵循指定上游实际行为，preserve只由明确UpdateMode选择。进程shell snapshot、完整模型循环和完整沙箱编排未移植。

未 commit、push、部署或重启。原旧main和AGENTS未改；server、go.mod与backend由相应实施方修改。本机常驻服务仍是上一轮6f799d2，当前候选尚未安装。
