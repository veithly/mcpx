# artifact 逐工具对标审查（2026-09-26）

结论：确证 1 个 P2 缺陷：UTF-16 分页拆开代理对，成功响应静默损坏文本。保留红测，未修改实现。

## 范围与环境
- 工作树：/Users/rick/.codex/worktrees/mcpx-tool-reliability/mcpx；分支 codex/tool-reliability-20260925；HEAD/base 6f799d2ddfa7ab9cbfbe0493823af6db11e918df。
- 当前工作树存在其他 agent 的未提交修改；本报告只新增 artifact 专属测试及本文件。
- artifact service、encoding、handler、catalog 与 base 无 diff；HTTP 包装层使用当前工作树，不能将全部结果等同于外层常驻服务验收。
- 复用 newWorkspaceRuntime（internal/server/workspace_resolve_test.go:18）及 operationTestSession（tools_operation_test.go:19），MCPX_HOME、文件和 SQLite 均在 t.TempDir；httptest 随机端口承载真实 MCP Streamable HTTP tools/call。
- 未访问真实凭证、屏幕、外部应用；未调用常驻服务写操作、重启、部署、commit/push；未新增子 agent，未跑全套。

## Codex 对照
本地只读对照 /tmp/mcpx-codex-cli-20260925，已验证 commit 68e0c9f5d8fd9449e97a81e92e8fcb86795713b2。
- 最接近的是 MCP 资源发现和读取；未发现与 MCPX「登记工作区文件、固化 SHA、按 Artifact ID 分片」直接对应的内建工具，不主张一一等价。
- codex-rs/core/src/tools/handlers/mcp_resource_spec.rs:7 create_list_mcp_resources_tool 暴露 server/cursor；:64 create_read_mcp_resource_tool 要求 server/uri，没有分片 offset/limit。
- codex-rs/core/src/tools/handlers/mcp_resource.rs:78 ListResourceArgs::target 校验 cursor 必须绑定 server；:145 ListResourcesPayload::from_single_server 保留 next_cursor。
- codex-rs/core/src/tools/handlers/mcp_resource/read_mcp_resource.rs:54 ReadMcpResourceHandler::handle_call 规范化参数；:89–95 调用 read_resource，失败以 resources/read failed 返回模型。编码/分片由资源服务端负责。

## MCPX 公开契约与实测
- schema：internal/server/tools_catalog.go:423–436，register 必需 remote_session_id/purpose/path，list 必需 session，read 必需 session/artifact_id；limit 为读取字节数，offset 为字节偏移。
- 入口：tools_artifact_clean.go:14 toolArtifactClean；register 走 withCleanIdempotency，list/read 分发各自 handler。
- 注册：tools_artifact.go:17 toolArtifactRegister 校验文件策略和 kind，返回结构化记录及 resource_link；本次 HTTP 测试断言 ID 和资源链接存在。
- 读取：tools_artifact.go:61 toolArtifactRead 返回 text/base64、源字节游标、eof/SHA；未结束时 :84 生成 next_action（本次未自动执行该 action 对象）。
- 正常分页通过：TestParityartifactRegisterAndPaging，列表恰有 1 项；真实输出 page1 text="abc" next=3 eof=false; page2 text="def" next=6 eof=true。
- 无效 ID 和内容漂移通过拒绝检查：TestParityartifactMissingAndDrift，两者均 isError=true、status=failed、code=ARTIFACT_READ_ERROR，message 分别为 artifact not found / artifact content changed after registration；后续 list 成功，连接未卡住。
- 恢复提示限制：上述错误统一 category=internal、retryable=false、safe_to_retry_unchanged=false，retry_hint 原文为 Inspect the server log and retry with a new request id if appropriate.；本次只验收错误可见及后续调用存活，未验收自动恢复。

## P2：UTF-16 分片静默损坏非 BMP 字符
- 影响：UTF-16 报告/日志含 emoji 或其他非 BMP 字符，分页边界落在代理对中间时，模型收到替换字符，响应仍 succeeded；按返回游标重试同样损坏。
- 最小输入：report.txt 字节 FF FE 3D D8 00 DE 41 00（UTF-16LE BOM + 😀A）。通过 artifact register 得到 ID，依次调用 read(offset=0,limit=4) 和 read(offset=next_source_offset,limit=4)。
- 真实 HTTP 输出：full="😀A" page1="�" next=4 page2="�A" next=8 eof=true。
- 红测失败原文：UTF-16 pagination corrupted text: got "��A" want "😀A"。
- 根因：internal/artifact/encoding.go:236 AlignSourceWindow 只按双字节 code unit 对齐；:132 utf16BytesToUTF8 使用 utf16.Decode，将孤立代理项替换为 U+FFFD 后仍返回成功。internal/artifact/service.go:180–207 将该窗口标为 UTF-8 text。
- 最小修复建议：UTF-16 分页按完整代理对确定窗口边界，必要时读取相邻 code unit，并让 next_source_offset 对应实际交付字节；避免将有效文件被分页切出的孤立代理项当成成功文本。先使本红测通过，无需重构其他工具。

## 复现与结果
在上述工作树执行：

~~~sh
go test ./internal/server -run '^TestParityartifact' -count=1 -v
~~~

真实结果：2 PASS、1 FAIL；mcpx/internal/server 3.391s。失败项为 TestParityartifactUTF16PaginationPreservesSurrogatePair。
单独最小复现命令：

~~~sh
go test ./internal/server -run '^TestParityartifactUTF16PaginationPreservesSurrogatePair$' -count=1 -v
~~~

完整本次输出：/tmp/mcpx-parity-artifact-20260926.log；专属测试：internal/server/parity_artifact_test.go:75、:89、:110。

## 未覆盖
- 已从 ALL_TOOLS 发现 mcp__codex_apps__macmcpx_artifact；定义包含 register/list/read 及本次所需字段，未调用常驻服务，未作宿主 schema 差异验收（由主线负责）。
- 未覆盖 UTF-16BE、UTF-8 跨字节片段、任意非对齐 offset、二进制、大文件资源上限、并发改写、跨会话权限及注册重放；不能据此宣称全边界通过。
- Codex 仅源码对照，未运行 Rust 测试；无外部平台实测。所有结论限定为本地隔离 Runtime HTTP 与所列源码证据。
