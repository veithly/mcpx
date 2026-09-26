# operation_batch 逐工具对标审查

审查日期：2026-09-26。结论：确认 **1 个 P1 问题**——三层依赖链的首步失败后，失败状态只传播一层，批次停在 running。正常三步依赖链及原有双 read 批次测试通过。本次只新增测试和报告，修复留给主线。

## 范围与证据环境

- 工作树：/Users/rick/.codex/worktrees/mcpx-tool-reliability/mcpx；分支 codex/tool-reliability-20260925；HEAD/base 均为 6f799d2ddfa7ab9cbfbe0493823af6db11e918df，已实际读取 Git 状态。
- Codex 对照：/tmp/mcpx-codex-cli-20260925，已验证 HEAD 为 68e0c9f5d8fd9449e97a81e92e8fcb86795713b2；只读源码，没有联网或运行 Codex 测试。
- 运行的是共享工作树当前源码。已有其他 agent 的未提交修改；关键的 internal/operation/service.go、internal/server/tools_operation.go、operation_runtime.go、tools_catalog.go 在检查时相对 HEAD 无 diff。因此不能把此次测试声称为纯净 base 全仓验收，但确认问题所在实现仍与 base 一致。
- 复用 newWorkspaceRuntime（internal/server/workspace_resolve_test.go:18）：MCPX_HOME、配置、SQLite、workspace 全在 t.TempDir；open auth，无真实凭证。通过 callOperationTool（internal/server/tools_operation_test.go:40）调用已注册 handler，包含公开输入处理和 structuredContent 输出；子步骤实际执行 read，没有 mock executor。
- 未启动 HTTP listener，没有调用常驻服务或外部应用；未重启、部署、commit、push。没有子 agent。HTTP 端到端和宿主 schema 总体对比由主线负责。

## Codex 最接近的实现

没有找到可与 MCPX 持久化 operation_id + depends_on DAG 一一对应的 Codex 工具；最接近的是多个工具调用的并发调度工作流。以下均为上述固定 Codex checkout 内的路径、函数和行号：

| 对照点 | Codex 证据 | MCPX 证据与差异 |
| --- | --- | --- |
| 并发准入与互斥 | codex-rs/core/src/tools/parallel.rs:125，ToolCallRuntime::handle_tool_call_with_source；138 查询并行能力，202–225 使用读/写锁包围 dispatch | tools_operation.go:82–85 将 !ReadOnly 或 OpenWorld 设为 Exclusive；internal/operation/service.go:654–675 使用 workspace 读/写锁。MCPX 另有持久化 DAG 调度。 |
| 并发能力来源 | tools/router.rs:237，ToolRouter::tool_supports_parallel；tools/handlers/mcp.rs:148，McpHandler::supports_parallel_tool_calls，使用显式 opt-in 或 read_only_hint | MCPX 从注册工具元信息选择互斥性，不接受调用方自行声明步骤并行安全。 |
| 错误和取消 | tools/parallel.rs:77，handle_tool_call；99–110 将非 Fatal 错误转为模型可见响应；246–276 处理取消、已完成结果和 aborted response | MCPX operation_runtime.go:226，operationResult 将 failed 公共响应转为执行错误；service.go:681–712 持久化步骤结果，再由 reconcile 汇总。此次问题位于额外的 DAG 后继传播层，不能归因为 Codex 某字段缺失。 |

Codex 路径表中省略的前缀均为 codex-rs/core/src/；MCPX 的 tools_operation.go 与 operation_runtime.go 前缀均为 internal/server/。不据此要求 MCPX 增加 Codex 没有的接口或做重构。

## 公开契约与 handler 检查

静态检查到的约束如下；未单独运行的边界不计为动态验收通过：

- internal/server/tools_catalog.go:326–340：operations 每项要求 id、tool、arguments，可带 depends_on；步骤对象禁止额外字段，arguments 保留业务对象；最大条数用 operation.MaxSteps。公开 batch schema 提供 run_id、operation_id、remote_session_id、purpose、operations。
- tools_operation.go:22–100，toolOperationBatch：要求 owner/editor；校验稳定 ID、非空/过长批次、步骤类型、clean-core 可用性；禁止嵌套 operation_batch、operation_manage、secret_provide；预校验全部步骤后才 Submit。
- tools_operation.go:254–281，validateOperationToolArguments：从已注册 schema 验证子参数，并注入父 session/purpose；operation_runtime.go:73–100，executeOperationStep 执行时同样固定父 session、删除 execution_mode，避免递归异步提交。
- internal/operation/service.go:1043–1089，validateSpec：拒绝重复步骤 ID、未知依赖、依赖环；Submit:151–169 使用包含计划的摘要约束稳定 operation_id 重用。
- tools_operation.go:399–442，operationView：返回每步状态/依赖；完成后的 wait 携带 steps[].result；未完成 wait 有紧凑错误摘要。正常用例验证实际文件内容能回到公开结果。

本轮发现的 MacMCPX connector 名称为 mcp__codex_apps__macmcpx_operation_batch。当前工具目录声明有 operations、purpose、remote_session_id、activity、acknowledge_requests、link_id，**没有 run_id / operation_id 入参**，因而无法通过该声明表达仓库 schema 中的稳定批次 ID。这是目录声明的确切缺口，未尝试多传字段、换工具绕过或实际调用常驻服务；是否宿主缓存/部署差异由主线验证，不把此项扩大为第二个已运行证实的实现缺陷。

## 实际运行结果

全部命令的 workdir 为 /Users/rick/.codex/worktrees/mcpx-tool-reliability/mcpx。

1. 已有真实 handler 用例：

~~~sh
go test ./internal/server -run '^TestOperationBatchRunsAndRecordsChildSteps$' -count=1 -v
~~~

结果 PASS，mcpx/internal/server 0.881s。两个 read 步骤完成，并由原测试核验子步骤观测事件；此结果不代表严格的最大并发数验收。

2. 新增两个 focused 回归：

~~~sh
go test ./internal/server -run '^TestParityoperation_batch' -count=1 -v
~~~

最后一次结果：正常链 PASS（0.06s）；失败传播 FAIL（0.54s）；包退出码 1，耗时 1.550s。测试用正确行为作为断言，故保留红测，**未修复，也未宣称整组通过**。

完整原始输出：/tmp/mcpx-operation-batch-parity-20260926.log。日志里的 public_response 是测试从真实 structuredContent 抽取的状态/错误字段，不是伪造的协议响应全文。

正常链输出：

~~~json
{"next_action":null,"state":"succeeded","steps":[{"error":{},"id":"a","state":"succeeded"},{"error":{},"id":"b","state":"succeeded"},{"error":{},"id":"c","state":"succeeded"}],"submit_status":"accepted","wait_status":"succeeded"}
~~~

TestParityoperation_batchDAGSuccess（测试文件:16）另断言 b 的公开结果中存在真实文件内容 parity-ok，并验证每个子步骤的开始时间不早于其依赖完成时间。

## P1：失败只传播一层，后继永久排队

**最小流程：** a 读取不存在的 missing.txt；b 依赖 a；c 依赖 b。b/c 都是只读目录操作。仅一个失败根、没有其他可运行分支，排除了其他步骤完成事件触发额外 reconcile 的影响。

~~~json
{
  "remote_session_id": "<隔离 fixture session>",
  "purpose": "隔离验证 operation_batch 失败依赖传播",
  "operations": [
    {"id":"a","tool":"read","arguments":{"view":"file","path":"missing.txt"}},
    {"id":"b","tool":"read","arguments":{"view":"list"},"depends_on":["a"]},
    {"id":"c","tool":"read","arguments":{"view":"list","limit":1},"depends_on":["b"]}
  ]
}
~~~

**最小可运行复现：** 在上述 workdir 运行：

~~~sh
go test ./internal/server -run '^TestParityoperation_batchFailurePropagatesTransitively$' -count=1 -v
~~~

该测试已在前述组合命令内实际运行。测试文件:63–90 从 operation_batch 获得 operation_id，然后调用 operation_manage(wait, timeout_ms=500) 并读取隔离持久记录。真实输出为：

~~~text
public_response={"next_action":null,"state":"running","steps":[{"error":"FILE_NOT_FOUND: source path not found","id":"a","state":"failed"},{"error":null,"id":"b","state":"skipped"},{"error":null,"id":"c","state":"queued"}],"submit_status":"accepted","wait_status":"accepted"}
transitive failure did not terminate: status=accepted operation=running a=failed b=skipped c=queued; want failed/failed/failed/skipped/skipped
--- FAIL: TestParityoperation_batchFailurePropagatesTransitively (0.54s)
~~~

**根因证据：** internal/operation/service.go:834–848 的 reconcile 建立一次 byID 快照，只遍历一次；将 b 写成 skipped 后，没有刷新快照或继续传播，c 看到的 b 仍是 queued。850–864 重读后 aggregateState（927–964）因仍有 queued 返回 running；enqueueReady（759–789）只在所有依赖 succeeded 时入队，故 c 无法执行。没有其余活跃步骤能触发下一次传播。Service.Wait（310–346）只等待/读取，不会重新 reconcile。

**影响与证据边界：** 500ms 实测已确认 failed/skipped/queued 的停滞状态；结合上述调度路径可确定，在无新的外部状态干预时，该批次没有内部推进路径，并非单纯执行较慢。调用者继续 wait/status 只会多等或重试，不能自行到达 failed 终态。本轮未长时间空等、未模拟重启恢复。

**建议最小修复：** 只改 reconcile 的后继失败传播：在内存状态图中循环传播至无新增 skipped，再持久化并汇总，或采用等价的后继队列。不能只更新 byID 后维持单次按 step_id 顺序的循环，因为 Get（266–268）按 ID 排序，不保证拓扑顺序。保留未受失败影响的独立分支。修复后此红测应变绿，并补逆 ID 顺序的依赖链验证。本 agent 未修改实现。

## 通过与未覆盖

通过：真实 read 批次提交/结果等待、三层成功链的依赖顺序、真实文件内容输出、双 read 子步骤事件记录；缺失文件错误码在停滞响应中仍清楚可见。

未验收：HTTP 传输/宿主 schema 完整性、稳定 ID 重放（主线负责 tool_replay）、语义确认及 resume、权限拒绝、环/重复/超量请求的动态验证、长任务取消与重启恢复、最大并发和大结果分页、外部应用/平台能力。此次两个回归没有额外外部依赖；没有运行全套测试、race、构建或 CI。

修改文件仅为 internal/server/parity_operation_batch_test.go 和本报告。交付下一步是主线修复 service.go 的传递失败传播，再运行上述 focused 回归。

主线交接：用户已确认主线独立运行 TestParityoperation_batchFailurePropagatesTransitively，复现 a=failed、b=skipped、c=queued，并接手 internal/operation/service.go 修复。本报告记录本 agent 修复前的直接实测结果；不代表主线修复后的验收状态。
