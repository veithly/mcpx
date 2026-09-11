# 工具失败与恢复契约

MCPX 区分“本次工具失败”与“整个 Runtime 不可用”。在请求已抵达且响应连接仍可写的前提下，已注册工具应返回非空工具结果，而不是把异常抛给 SDK、只留下日志，或无限等待。

## 结果边界

所有公开工具经过同一参数校验、异常隔离、错误封装和有界响应入口。工具执行错误使用 `CallToolResult.isError=true`，正文说明错误原因和下一步，`structuredContent.error` 提供机器可读的分类与恢复信息。原始 Go error 被转换为工具结果后不再和结果一起返回，避免 SDK 丢弃结果。未知 JSON-RPC 方法、未知工具名和无法解析的协议消息仍属于协议层错误，不伪装成已执行工具。

错误主要字段包括 `code`、`category`、`message`、`retryable`，以及 `details` 中的 `origin`、`retry_hint` 和 `safe_to_retry_unchanged`。兜底失败另外携带可用的 request/session 标识和执行状态；已有明确 Task/Operation ID 的工具继续保留自己的日志和状态恢复动作。没有真实任务 ID 时不会生成一个虚假的查询对象。

| 情况 | 返回和处理 |
|---|---|
| 错误参数、额外字段、错误动作分支 | `INVALID_ARGUMENTS`；工具体尚未执行。说明具体字段/分支错误，按当前 schema 修正再调用。 |
| 命令非零退出、找不到命令 | 保留退出码及已有 stdout/stderr；归类到命令执行，不据此宣称 MCPX 崩溃。 |
| panic、空错误结果、不可序列化结果 | 捕获后提供错误正文、结构化失败及安全恢复提示。不能据此假设副作用没有发生。 |
| 上游连接/执行超时或依赖故障 | `MCP_CONNECT_TIMEOUT`、`MCP_CALL_TIMEOUT`、`MCP_CALL_FAILED` 等，明确归属上游。 |
| 审批等待 | 保留 waiting_confirmation 和原确认动作，不标成执行失败，也不绕过确认。 |
| 调用响应期限耗尽 | `TOOL_TIMEOUT`；响应等待结束不等于实际工作已停止，先用原请求/Task/Operation 查询结果，不重复副作用。 |
| 并发容量耗尽 | `TOOL_BUSY`，本次未执行；等待已有调用结束后有限退避重试。 |

`retryable=false` 不表示用户目标不可能完成，而是不要不加判断地原样重复调用。允许按错误纠正参数、修复依赖、查询已有结果或选择其他已知受支持的操作。权限拒绝、用户取消和明确安全边界不能通过重试绕过。

## 同步等待与长期任务

公开同步响应的保护期限为 **45 秒**；实际连接断开或更早的调用方期限可能先发生。普通调用最多同时占用32个响应工作槽，查询/恢复类工具另留8个槽位。槽位随实际执行退出而释放，不会因客户端超时就假装已经停止，从而避免反复超时制造无界残留工作。

响应等待和工作执行期限分离。连接断开或响应等待结束时，已启动的工作可继续运行并记录结果；普通工作另受执行期限保护。不能强制终止忽略取消的 Go 函数，也不能回滚已发生的副作用。工作台运行计数保持到处理函数实际退出；晚到结果通过独立记录上下文保存。记录系统本身失效时仍不能保证保存成功。

长命令使用现有执行 Task 与 `execute(action=attach)` / `observe`；长工具工作使用 `operation_batch` 或该工具公开支持的异步模式，再用 `operation_manage` 查询。不要给没有公布 `execution_mode` 的只读工具添加该字段。内部 Operation 的子步骤继续由 Operation 自己管理期限，不被同步45秒限制错误地截断。

## 结果核实闭环

`accepted`、`completed_in_call=false` 或 Task `running` 都不是最终结果。复用原 Task ID 调用 `execute(action=attach)` 或 `observe(view=task|logs)`，复用原 Operation ID 调用 `operation_manage(action=status|wait|result)`，直到得到终态。不要重复运行构建以代替查询。即便短命令已在首次调用内结束，也会保留 `execution_task_id` 作为可复查的执行回执。

文件读取、状态和日志查询、Task 等待以及 Operation 查询不进入防重复执行的回放缓存。相同查询参数要得到新状态，不能把上一次的 `running` 或旧文件内容缓存十分钟。真正执行命令、文件写入等有副作用的调用仍保留回放/幂等保护；要取回某次历史原始工具结果，使用 `observe(view=history, request_ids=[原请求ID])`，读取其 `results`。

历史时间范围支持 RFC3339（含时区）、日期和 Unix 毫秒，数字必须完整匹配；不能把 `2026-09-11T...` 解析成 2026 毫秒。`kinds=[command]` / `[task]` 使用当前公开工具 `execute`，不匹配已移除工具名。

空 stdout/stderr 只说明这次没有标准流内容。日志文字回复也必须包含 Task、status、outcome 和已知退出码。Unity 等使用 `-logFile` 时还应读取该文件尾部的明确构建结论。`exit_code=0`、构建日志成功、源码修改已写入、页面视觉与交互通过，是不同层次的证据。

大输出提供字节偏移与续读动作；`output_truncated` 表示本次响应不完整，`log_truncated` 表示保留策略可能已丢弃部分原日志，不能混为一谈。使用返回的 `stdout_next_offset` / `stderr_next_offset` 逐页读取，不自行按字符数算偏移。文件读取类似地遵循返回的 `next_offset` / `next_cursor`。版本校验写入后，再用新读取的 SHA-256 和内容与编辑回执比较。

新增回归入口：`go test ./internal/server -run 'TestVerification|TestReplay' -count=1`。它验证实时读改读、相同参数轮询、恢复回放边界、日期与命令筛选、执行句柄、空日志回执和日志续读。

## 上游 MCP

握手超时与子进程生命周期分离：初始化成功后解除握手计时器对进程的取消关联，后续调用受调用上下文管理。初始化失败或超时会取消所属进程；关闭时先请求进程退出，再等待传输关闭，避免被不结束的上游阻塞。

上游工具的业务内容与 structuredContent 保持透明，包括上游 isError=true 的原始第一段正文。失败时可以追加一段幂等的恢复提示，不改写上游的业务数据，也不把其错误自动当成 MCPX 服务故障。

上游已经返回但结果超过预算时，不能为了获得较小输出而直接重复一次写操作。先核对真实效果；仅对确认安全的只读/幂等查询调整分页或 limit。不可序列化的返回值在进入 SDK 发送路径前被替换为可靠错误，避免客户端无限等待。

## 502、断网与服务进程退出的边界

反向代理先行返回502/504、客户端已断开、进程被操作系统终止或机器断网时，MCPX 自己无法在已经中断的连接上补发正文。这类情况只能表述为“本次响应未收到，执行结果未知”，不能自动推断命令没执行，更不能无限重复副作用请求。

模型恢复顺序：先查看已返回的 Task/Operation/请求记录或文件版本；只读请求有限重试；写请求先核对效果并复用已有幂等键。无法取得状态时应报告具体未验证之处，而不是从一次502直接断言所有工具都坏了。服务端错误正文、结构化恢复动作和默认 Agent 指引共同支持这个流程，但不能强迫外部宿主自动再调用。

## 验证入口

```sh
go test ./internal/server -run 'TestToolFailure|TestToolArguments|TestToolResponse|TestToolCapacity|TestToolCancelled|TestEarlyTool|TestAllPublishedOutput|TestCancelledCallStill|TestUpstreamErrorHint|TestEnvelopeFailure' -count=1
go test ./internal/mcpproxy -run 'TestMCPHandshake|TestMCPUnresponsive' -count=1
go test ./... -count=1
go test -race ./internal/server ./internal/mcpproxy ./internal/envelope ./internal/arc -count=1
go vet ./...
```

协议测试同时包含内存传输与真实 Streamable HTTP 客户端，生命周期测试启动独立 stdio 上游子进程。测试用项目和数据库均为临时数据，不替换正在服务用户的 Runtime。修改源码和测试通过不等于生产进程已经加载新版本，部署时需要单独替换运行二进制。
