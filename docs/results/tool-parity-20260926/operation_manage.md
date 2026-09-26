# operation_manage 逐工具对标审查

审查日期：2026-09-26。结论：隔离 MCP HTTP 正常生命周期通过；确认两项 P2 分页缺陷。修复前专属测试为 1 PASS、2 FAIL。主线已确认复现并接手 ResultPage、Service.Result、operationResultView 修复，本 agent 不修改实现，也不继续扩展检查。

## 范围与验证环境

- 工作树：/Users/rick/.codex/worktrees/mcpx-tool-reliability/mcpx；分支 codex/tool-reliability-20260925；HEAD/base 为 6f799d2ddfa7ab9cbfbe0493823af6db11e918df。
- 运行的是该工作树当前源码，包含主线及其他 agent 的未提交改动。复现时 Result 与 operationResultView 的相关区段相对 base 未变；稍后观察到 operation/service.go 的并行改动位于 reconcile 的依赖跳过传播，不是本 agent 的修改。
- 实际工具链为 go1.26.6 darwin/arm64；本工作树 go.mod:3 要求 go 1.26.1，未更换工具链。
- 已读取适用全局/工作树 AGENTS.md、RTK.md，以及只读 Codex 仓库的相关指南。采用最小充分检查，只运行 TestParityoperation_manage 前缀。
- newWorkspaceRuntime（internal/server/workspace_resolve_test.go:18–43）将 MCPX_HOME、配置、SQLite 和工作区放在 t.TempDir；open 模式，无凭证。HTTP 服务为 httptest.NewServer 的随机 loopback 端口。
- 未修改旧 main、常驻服务、实现源码或其他 agent 文件；未访问真实凭证、私人屏幕或外部应用；没有重启、部署、commit/push、联网、新 clone 或子 agent。
- 宿主 schema 全面对比、统一端到端 harness 和 replay 修复由主线负责，本报告只记录与本工具直接相关的障碍。

## Codex 最接近的工具/工作流

只读对照仓库 /tmp/mcpx-codex-cli-20260925，git rev-parse HEAD 实际返回 68e0c9f5d8fd9449e97a81e92e8fcb86795713b2。没有运行 Codex 测试，不声称它没有同类问题。

下表的 Codex 路径均相对该克隆根目录。

| MCPX 能力 | Codex 函数/行号 | 对应程度 |
| --- | --- | --- |
| 通过句柄等待异步执行并读取输出 | codex-rs/core/src/tools/handlers/shell_spec.rs:117–157，create_write_stdin_tool；session_id 必填，空 chars 用于 poll，另有 yield_time_ms/max_output_tokens。handlers/unified_exec/write_stdin.rs:58–106 的 WriteStdinHandler::handle_call 解析后调用 manager | 最接近 operation_manage(wait/result)，但 Codex 是进程会话，MCPX 是持久化 Operation |
| 未知句柄、同句柄并发 | codex-rs/core/src/unified_exec/process_manager.rs:883–905，write_stdin_inner，对未知 ID 返回 UnknownProcessId，同进程加交互锁 | MCPX 的未知 ID 和跨 Remote Session 拒绝有本次实测 |
| 等待与终态输出 | 同文件 :1017–1030 限定等待并收集输出，:1073–1109 区分活跃进程和退出，输出 process_id/exit_code/chunk_id；codex-rs/core/src/tools/context.rs:380–393 定义 ExecCommandToolOutput | 与 MCPX accepted/succeeded、state、next_cursor/续页 action 对照 |
| 取消 | process_manager.rs:988–1008，非 TTY 的中断输入调用 process.interrupt，错误路径可 terminate；TTY 发送输入 | 并非 MCPX 独立持久化 cancel 契约；本次只实测完成后 cancel |
| 稳定结果分页 cursor、步骤确认 token 和 resume | 上述 write_stdin schema 没有对应字段 | 没有直接对应，不推断整个 Codex 仓库不存在其他恢复机制 |

## MCPX 公开契约与处理路径

- 源 schema：internal/server/tools_catalog.go:341–352。action 为 status/wait/result/cancel/resume；operation_id 与 operation_ids 二选一；批量最多 32 个，只支持 status/result；另有 step_id、timeout_ms、confirmation_token、cursor、limit。flat 公开 schema 的已有断言见 internal/server/public_catalog_test.go:404–436，本次没有运行该非专属测试。
- toolOperationManage：internal/server/tools_operation.go:103–184。先取 Remote Session；cancel/resume 要求 owner/editor；检查每个 Operation 的 RemoteSessionID；wait 设默认值和时长上限，result 交给 Service.Result。
- parseOperationTargets：同文件 :191 起，检查单/多 ID 互斥、数量、重复 ID和批量动作限制。静态阅读不代表这些组合已经通过本次动态验收。
- Service.Wait：internal/operation/service.go:310–348，读取持久状态并等待 active.done；Cancel（:400–438）保留已完成结果；Resume（:440–524）检查 waiting-confirmation 步骤和 token 后重新入队。resume 本次仅阅读，未实际验收。
- operationResponse 将运行/等待超时映射为 accepted，终态和确认状态映射为对应 envelope。operationResultView（internal/server/tools_operation.go:568–588）生成结果和续页信息；最终公开机器输出将续页动作放在 actions[].arguments，本测试直接执行该返回值。

当前工具目录发现 mcp__codex_apps__macmcpx_operation_manage，但声明的两个参数分支仅有 acknowledge_requests、action、activity，没有 operation_id、operation_ids、step_id、confirmation_token、cursor、limit、timeout_ms。因此当前声明无法定位和续读某个隔离 Operation。未添加定义外字段、未调用另一常驻工具绕过契约；宿主差异定位及修复交主线。不能据此声称常驻部署 handler 已通过本报告测试。

## 实际运行与通过项

专属文件：internal/server/parity_operation_manage_test.go。

parityOperationManageHTTP（:18 起）复用隔离 Runtime，注册真实工具，以 SDK Client.Connect 和 CallTool(Name=operation_manage) 通过 MCP Streamable HTTP 进入 schema 校验、handler 和公开输出链路。fixture 通过隔离 operations.Submit 加确定性内存 executor 生成数据；分页复用 submitOperationForTest（internal/server/tools_operation_test.go:52 起），不启动 shell 或外部程序。这验证 operation_manage，不声称覆盖 execute/operation_batch 的完整提交链路。

最小完整检查命令（显式指定 checkout）：

~~~sh
go -C /Users/rick/.codex/worktrees/mcpx-tool-reliability/mcpx test ./internal/server -run '^TestParityoperation_manage' -count=1 -v
~~~

修复前最后一次实际运行：退出码 1，mcpx/internal/server 0.679s，1 PASS、2 FAIL。日志在 /tmp/mcpx-parity-operation-manage-20260926.log。真实精简输出：

~~~text
pending wait: status=accepted state=running
status: status=succeeded state=succeeded
result: status=succeeded state=succeeded
cancel: status=succeeded state=succeeded
cross-session status: operation belongs to another Remote Session
unknown status: operation not found
--- PASS: TestParityoperation_manageNormalLifecycle (0.08s)
--- FAIL: TestParityoperation_managePaginationNextAction (0.04s)
--- FAIL: TestParityoperation_managePaginationUTF8 (0.06s)
FAIL mcpx/internal/server 0.679s
~~~

TestParityoperation_manageNormalLifecycle（:51–112）实际证明：10ms wait 在操作仍受 channel 阻塞时返回 accepted；释放后 wait 成功且步骤值为 normal-ok；status/result 成功；完成后 cancel 不篡改 succeeded；另一 Remote Session 查询被拒绝；未知 ID 返回明确错误。日志分别记录 FORBIDDEN 和 OPERATION_NOT_FOUND。

## P2：照返回 action 续页时忽略 cursor，重新返回完整对象

证据：TestParityoperation_managePaginationNextAction，测试文件 :114–133。

~~~sh
go -C /Users/rick/.codex/worktrees/mcpx-tool-reliability/mcpx test ./internal/server -run '^TestParityoperation_managePaginationNextAction$' -count=1 -v
~~~

fixture 持久化 {"value":"012345678901234567890123456789"}，首轮 result 使用 limit=10，返回 next_cursor=10。第二轮直接执行公开 actions[0].arguments，其中有 operation_id/action/cursor/remote_session_id，却没有 limit。真实输出：

~~~text
first chunk="{\"value\":\"" next_cursor=10 advertised_limit=<nil>
following next_action: result={"value":"012345678901234567890123456789"} next_cursor=""
continuation did not preserve cursor/chunk contract: want offset=10; got {"value":"012345678901234567890123456789"}
~~~

第二轮成功返回完整业务对象，缺少原分页的 offset/chunk，cursor 没有生效。按 chunk 拼接的客户端中断，需要特判或额外读取；完整结果仍在，不夸大为不可恢复的数据丢失。

根因证据：

1. internal/server/tools_operation.go:573–585，operationResultView 未在续页 action 中保留有效 limit。
2. internal/operation/service.go:356–378，Service.Result 未传 limit 时回到 64KiB；len(raw)<=limit 的完整返回发生在解析 cursor 之前，造成分页结构切换。

最小建议：只有空 cursor 的首次读取走完整结果快捷分支；非空 cursor 必须解析/校验并维持 chunk 返回。续页 action 保留实际有效 limit。现有红测转绿即可验收，无需扩展执行架构。

## P2：UTF-8 跨页切断后损坏，拼接不能恢复原文

证据：TestParityoperation_managePaginationUTF8，测试文件 :135–162。

~~~sh
go -C /Users/rick/.codex/worktrees/mcpx-tool-reliability/mcpx test ./internal/server -run '^TestParityoperation_managePaginationUTF8$' -count=1 -v
~~~

fixture 持久化 {"value":"中文"}；每次明确带 limit=11，按 next_cursor 连续取页，因此独立于上一项丢 limit 问题。真实输出：

~~~text
offset=0 next_offset=11 chunk="{\"value\":\"�" next_cursor="11"
offset=11 next_offset=18 chunk="��文\"}" next_cursor=""
UTF-8 pagination changed payload: want="{\"value\":\"中文\"}" got="{\"value\":\"���文\"}"
~~~

所有页均 succeeded，cursor 正常结束，但“中”变成三个 U+FFFD。消费者拼回后的文本失真，同样分页重试无法恢复。持久化原文未被读取覆盖；确认的问题是传输分页丢失，不是数据库原文丢失。

根因：internal/operation/service.go:385–390，Service.Result 按任意字节 end 截取 raw[offset:end]，转 string 再 JSON marshal，非法 UTF-8 被替换字符覆盖。

最小建议：维持文本 chunk 契约，按合法 UTF-8 边界选择切点，cursor 指向真实切点；较小 limit 时也须保证前进量，校验 cursor 不位于字符内部。以现有红测转绿验收。

## 未覆盖、并行修复与交付

- 本次未验收常驻 connector 的真实调用；其当前定义缺少业务定位字段。外层实际部署状态不由隔离 HTTP 测试证明。
- 未验收 resume 的用户确认、错误 token 组合、重启恢复、活跃外部进程取消及平台权限；仅完成后取消已动态通过。
- 未验收批量混合终态、32 项总预算、超大结果、非法 cursor、极小 limit 和等待上限。未测项不列为确认缺陷。
- Codex 对照仅指定 commit 的静态阅读，无运行验收。
- 主线明确确认两个红测并接手 ResultPage/service.Result/tools_operation.operationResultView 修复。本报告是修复前证据快照，不宣称修复后仍失败；修复后的测试结果由主线补充。
- 本 agent 仅新增两个专属文件：
  - /Users/rick/.codex/worktrees/mcpx-tool-reliability/mcpx/internal/server/parity_operation_manage_test.go
  - /Users/rick/.codex/worktrees/mcpx-tool-reliability/mcpx/docs/results/tool-parity-20260926/operation_manage.md

主线下一步：完成局部修复后运行同一前缀，期望三项通过；无需重做宿主 schema harness 或扩大本审查范围。
