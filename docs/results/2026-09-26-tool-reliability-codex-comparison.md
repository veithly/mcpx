# MCPX 工具可靠性诊断与 Codex CLI 对比

## 初次验收状态（安装前快照）

完成四项运行/编辑修复、schema 失配诊断和日志分类改进。分支 codex/tool-reliability-20260925，基线 38174f8（0.9.17）。本报告记录初次验收时的状态，当时尚未提交、推送或安装；随后用户已授权安装并推送。实际安装需核对已签名二进制的 build commit、运行进程和远端分支。连接器缓存刷新单独验证。

原目录 /Users/rick/Documents/Project/mcpx 的 main 停在 e32ecff（0.9.15），有 28 个已跟踪文件修改和其他未跟踪文件。最初复现在该目录完成；发现与运行服务版本不同时，将本次修改迁移到独立工作树，恢复原目录原有改动集合。旧目录构建的程序没有安装。

## 近期日志

窗口：2026-09-20 00:00 UTC 至 2026-09-25 晚间首次查询快照。来源：~/.mcpx/logs/mcpx-launchd.log、audit.jsonl、只读 ~/.mcpx/state/mcpx.db。下面是调用/结果记录数，不是独立事故数，同一异步失败会被多次查询。

| 结果 | 数量 | 结论 |
|---|---:|---|
| execute / PROCESS_EXIT | 273 | 包含测试断言、编译失败、脚本语法错误、缺文件；并非全部是 Runtime 故障 |
| execute / COMMAND_NOT_FOUND | 39 | 27 条含 command not found，9 条含 No such file；既有未安装 mcporter，也有已安装 Node 未进入子进程 PATH |
| execute / REMOTE_SESSION_ERROR | 37 | 示例为 context canceled，外部取消触发原因未全部证明 |
| edit / INVALID_ARGUMENTS | 35 | 34 条缺 rev；连接器定义是 base_sha256，在线服务要求 rev |
| edit / MATCH_AMBIGUOUS | 21 | 应使用唯一上下文或已读版本上的行范围，歧义保护不能移除 |
| edit / STALE_REVISION | 14 | 应重新读取和生成修改，不能去掉并发修改保护 |
| edit / MATCH_NOT_FOUND | 11 | 另行复现 CRLF 表示不一致；不能断言这 11 次均为换行问题 |
| operation_manage / OPERATION_FAILED | 454 | 多为失败执行的再次观察；发现失败摘要携带 exit_code=0 |
| operation_manage / OPERATION_ALREADY_COMPLETED | 28 | 取消/完成竞争可复现；Resume 的终态错误仍保留，不能声称这 28 条全部消除 |
| operation_batch / BAD_REQUEST | 30 | 步骤字段层级错误、嵌套 operation_manage 等 |

服务日志另有 19 条 read TOOL_TIMEOUT、9 条 read 结果保存超时，跨越先前 worker 修复前后，未作为本次新根因重复修复。参数校验在普通调用日志之前返回，审计也不包含全部失败，因此日志与持久化结果计数不同。

两个在线 MacMCPX 连接均返回 0.9.17，build=e32ecff+upstream-70e1e43-uncommitted，revision guard=compact_rev；本任务可调用连接器定义却只支持 base_sha256。这是在执行正文之前发生的协议失配，原样重试无效。

## Codex CLI 源码对比

实际克隆至 /tmp/mcpx-codex-cli-20260925，官方仓库 [openai/codex](https://github.com/openai/codex)，固定提交 68e0c9f5d8fd9449e97a81e92e8fcb86795713b2，提交时间 2026-09-25 15:26:34 UTC。静态阅读实现，未构建/测试 Codex Rust 工程。

| 设计点 | Codex 实现 | MCPX 改进 |
|---|---|---|
| 命令参数 | codex-rs/core/src/tools/handlers/unified_exec.rs 明确 cmd、shell、login、tty、yield/timeout | 复用 MCPX 已有 command/argv/runtime，不另造执行框架 |
| 执行环境 | core/src/unified_exec/process_manager.rs:1420、1448 构造并传入 cwd/env | 原来仅补顶层 executable 查找，现修复子进程继承 PATH |
| 长任务 | process_manager.rs:636、867 有界等待、存活进程句柄、write_stdin 续读 | 保留 execution_task_id/operation_id，验证 async wait 和终态竞争，避免重放副作用 |
| 文件编辑 | apply-patch/src/file_update.rs:47、seek_sequence.rs:12 按行处理和换行保留；逐级匹配精确/空格/Unicode 标点 | 仅统一可确认等价的换行表示；保留唯一匹配和版本校验，未采用可能改变语义的模糊缩进/标点匹配 |
| 错误反馈 | 退出码、输出、进程句柄分开；补丁错误有具体上下文 | 修复跨步骤误取退出码；参数失败给实际 schema 指纹，日志给错误码/类别 |

[官方 Apply Patch 文档](https://developers.openai.com/api/docs/guides/tools-apply-patch)也强调实际应用后报告 completed/failed 与失败上下文；该 API 文档不等同于 CLI 工具协议。

## 改动

1. terminal/process_command.go：macOS 子进程 PATH 追加 /opt/homebrew/bin、/usr/local/bin，保持原 PATH 优先。在设置工作目录后生成环境，保留 PWD；短任务、长任务、直接进程共用。保留显式路径和 exec.ErrDot，ExtraEnv 仍可覆盖。
2. edit/apply.go：source、match、replacement 使用同一逻辑 LF 表示，再按原编码/换行写回，防止匹配失败或双 CR。保留 stale、唯一匹配、路径与预写保护。
3. envelope/envelope.go：按确定结果容器提取失败退出码，跳过成功步骤，避免 map 遍历或无关数据污染。
4. operation/service.go：取消已结束操作返回原终态，保持 state/event/sequence；控制台只将实际 cancelled 的操作列为已取消。Resume 保持原验证。
5. server/observability.go：参数失败返回 tool_schema_revision、schema_source=tools/list，edit 明示 rev 和连接器刷新方法。参数校验失败进入日志，补 error_code/error_category，不记录参数、输出或凭证。
6. 新增五个 Go 回归测试文件和 scripts/tool-reliability-probe.py，对候选二进制使用临时 home、临时数据库、随机 loopback 端口验收。

## 验证

先失败再通过：Node spawnSync ENOENT / env: node not found；CRLF match not found；失败批次 exit_code=0；完成后取消 operation already completed；schema 失配缺恢复指纹。

最终通过：

- 全仓库 go test ./... -count=1。
- 五个相关包完整 go test：terminal、edit、envelope、operation、server。
- 关键路径 go test -race：daemon PATH、编辑、stale/歧义保护、取消、HTTP 链路和错误返回。
- 上述五包 go vet、gofmt、git diff --check。
- 固定 mcpx-local 签名和严格验签；Identifier=com.mcpx.server、Authority=mcpx-local。
- 签名程序真实 HTTP：tools/list、session、read/edit、CRLF 字节回读、幂等重复、旧 schema 拒绝、Node 子进程、async wait、完成后取消；结束后关闭临时进程。

候选文件：bin/mcpx-server.reliability，版本 0.9.17，build=38174f8-tool-reliability-uncommitted，date=2026-09-25。SHA-256：a97e4d8d7ac50fc19d88aa5319e77d6292d8f5f6ae0142145605c06de741572a。

复用命令：python3 scripts/tool-reliability-probe.py --binary bin/mcpx-server.reliability --output-dir /tmp/mcpx-reliability-probe

首次临时验收触发 macOS Unix socket 路径长度限制；改为 /tmp 短路径后通过，未扩大范围修改 observer socket。

## 安装要求与边界

初次验收没有更新常驻服务。获得安装授权后，重新检查活跃操作，保留已安装程序副本，同目录暂存候选、验签和运行后原子 mv 替换，再重启 launchd。不要直接 cp 覆盖正在运行的 inode，也不要从落后的原 main 重建安装。

连接器须刷新 tools/list 或重新连接，确认 edit 暴露 rev 后再验证真实 read→edit。当前工具没有直接刷新连接器缓存的能力；仅重启服务不能证明缓存已更新。遵循当前唯一契约，不增加 base_sha256 别名。

没有安装缺失的 mcporter/PIL/yaml，没有修其他项目的测试/编译错误，没有改变权限或绕过编辑保护；不保证任意脚本永不出错。Windows 未实机验收。
