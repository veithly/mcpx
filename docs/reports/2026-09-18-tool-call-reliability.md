# 本机工具调用可靠性排查（2026-09-18）

## 范围与证据

本地 `main` 工作区，HEAD `4b934594`，合并目标为已经 fetch 的 upstream `3c6ba4a5`（v0.9.15）。
保留已有工作台/UI 改动；不提交、不推送、不改生产配置、不重启服务。
常驻服务仍为 `0.9.11 / 4b934594-dirty`，以下修复尚未安装。

只读分析本机 `~/.mcpx/state/mcpx.db` 的 `tool_results`、`observation_events`，
以及 `mcpx-launchd.log`、`audit.jsonl`、`cloudflared.log`。未导出业务请求正文、凭证或截图。
统计窗口为 UTC+8 的 9 月 15 日零点至已有最后一条结果（9 月 18 日 21:31:09）。
`tool_results` 共 11,860 条，其中 703 条为 failed；这是工具结果记录数，不是用户操作数或网络失败率。
异步子步骤、管理查询和重复请求可能分别占记录，未到达服务的请求不在此表内。

## 发现与修复

| 证据 | 原因/边界 | 本轮处理 |
| --- | --- | --- |
| `mcp_tool` 39 条失败中 36 条为 `MCP_RESULT_TOO_LARGE`；响应 274,482–752,008 字节 | 点击、按键、桌面状态返回的图片超出共用 262,144 字节预算；不能因此推断操作未执行 | 增加独立 `limits.max_mcp_result_bytes`，默认 4 MiB；普通文本预算不变。全局/项目均可配置，超限仍明确报错，不静默截断上游图片/结构化数据 |
| 前一轮自动重试的真实 stdio 测试观察到一次逻辑调用产生 3 次副作用 | 协议报错或结果丢失并不意味着无副作用 | 删除无差别重试；保留同一幂等键的持久化结果重放。协议错误与超大结果均验证不重复调用 |
| `runtime_read` 33 条 `REMOTE_SESSION_REQUIRED`；已关联的多数输入明确带 workspace，查询 project/instructions | 输入契约允许 workspace，但处理函数强制要求 session | 复用显式 workspace 解析；无需先创建 session。仍拒绝无目标、未知项目、会话与项目冲突，不自动选择项目 |
| 5 条 `RUNTIME_START_ERROR` 明确为 Node 不在 PATH；launchd PATH 仅系统目录 | 直接运行解释器不加载登录 shell，Homebrew Node 存在却未被发现 | macOS 直接程序调用先遵循 PATH；仅裸名称缺失时检查 `/opt/homebrew/bin`、`/usr/local/bin`。保留显式路径、argv 原样和 `exec.ErrDot` 防护，不运行 shell 初始化脚本 |
| 搜索/上下文慢调用中有明确指定 paths 的请求，搜索最长约 137 秒 | 作用域仅在逐文件阶段过滤，目录遍历仍扫描全工作区 | 在遍历时剪去无关目录，搜索与上下文共用相同作用域解析；缺失/越界路径不扩大扫描范围，目标文件仍经过权限过滤 |

有限响应预算不是安全地重做动作的依据。上游成功结果保留成功状态，上游 `isError=true` 仍为失败。
同键重取结果不会重新启动已完成的上游动作。不同键或无键请求不能据此承诺 exactly-once。

### 搜索基准

合成工作区：1 个作用域内目标文件、2,000 个无关目录下文件，不读取真实项目业务内容。

```text
go test ./internal/source -run '^$' -bench '^BenchmarkScopedSearchLargeUnrelatedTree$' -benchmem -benchtime=200ms -count=3

优化前：2.048 / 2.522 / 2.072 ms/op；约 580 KB/op，6176 allocs/op
优化后：0.0715 / 0.0713 / 0.0719 ms/op；约 28 KB/op，146 allocs/op
```

中位数约改善 29 倍。这不代表所有线上搜索都会改善同样倍数，也不代表全部超时已经消除。
回归测试还覆盖 nested/exact-file/overlapping/root/missing/escape/denied scopes。

## 上游合并收敛

上一轮以整文件取上游的方式处理冲突，丢掉了本地有效行为。本轮恢复：

- 会话创建幂等键的跨 workspace 冲突保护。
- `VersionConflictError` 的实际产生路径、当前版本与 `ErrConflict` 解包。
- 环境快照的乐观锁，避免覆盖并发版本更新。
- 配置临时文件写入、同步后原子替换，保留上游新增的失效 workspace 清理。
- 使用上游当前 `Replayed` 契约，删除上一轮补回但未正确工作的旧 token 标记。
- 对齐本地当前异步调用/schema 契约，恢复被覆盖的 operation 超时/错误摘要测试。

没有恢复上游已经移除且无调用者的 token/patch 接口。
Git 仍处于未提交 merge，工作树中已解决的代码不代表已创建 merge commit。

## 验证

已先复现后修复：重复副作用、图片超限、只读查询误要求 session、launchd PATH 下 Node 失败、作用域查询仍扫描无关文件。
最终全量 `go test ./... -count=1`（44 个包条目，含无测试包）、`go vet ./...`、CGO 关闭构建均通过。
server/config/remotesession/terminal/mcpproxy 五个包的完整 race 检查通过；
搜索优化后的 source 完整 race 检查也通过。`git diff --check` 通过，无未解决的 Git 冲突项。

脱敏诊断与测试输出保存在 `/tmp/mcpx-reliability-20260918.BO1sUr/`：
`repro-2.log` 为核心缺陷复现，`full-test-final.log` 为最终全量回归，`race.log` 为五包竞态检查。
临时构建产物仅供验证，尚未以 `mcpx-local` 身份签名，不用于覆盖常驻服务。

本轮修改文件均已 gofmt；仓库整体仍有此前存在的格式差异：
`internal/server/console_snapshot.go`、`internal/server/process_tree_cancel_test.go`、
`internal/server/runtime_review_regression_test.go`。未为了格式检查改动这些无关文件。

## 仍需区分的风险

- 448 条 `PROCESS_EXIT` 是子命令非零退出，31 条 `COMMAND_NOT_FOUND` 是命令不存在；不能统称 MCPX 宕机。
- 常驻日志记录 20 次响应超时（9 月 15–16 日，read 17、execute 2、session 1）。本轮只优化已复现的 scoped traversal，不承诺消除所有 I/O/无作用域大搜索耗时。
- 6 次结果持久化失败包含 5 次 deadline 和 1 次 SQLite busy；未改动生产数据库或放宽持久化语义。
- 隧道日志有 TLS 握手报错、取消及超时行；这些日志行不等同失败工具调用次数。未调整网络或隧道设置，也不能把上述代码修复视为网络修复。
- 线上改进尚未测量。需用户授权后按仓库规则构建、`mcpx-local` 签名、安装并重启，再验证真实工具流与新日志；本轮构建只放临时目录，不覆盖 CLI/常驻二进制。
