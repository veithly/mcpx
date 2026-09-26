# read 逐工具对标审查

审查日期：2026-09-26。结论：确认 **2 项 P2 问题**，正常流程和关键边界通过。新增回归保留红测，预算与续读修复由主线接手；本 agent 没有修改实现，也没有验收主线后续修复。

## 范围与证据基线

- 工作树：/Users/rick/.codex/worktrees/mcpx-tool-reliability/mcpx
- 分支：codex/tool-reliability-20260925；审查 HEAD/base：6f799d2ddfa7ab9cbfbe0493823af6db11e918df。
- Codex 对照：/tmp/mcpx-codex-cli-20260925，commit 68e0c9f5d8fd9449e97a81e92e8fcb86795713b2；只读本地源码，没有联网或克隆。
- 执行环境：Go 1.26.6 darwin/arm64；该工作树 go.mod 声明 1.26.1。
- 只新增 internal/server/parity_read_test.go 和本报告。没有修改旧 main、其他实现或其他 agent 文件，没有 commit/push、重启、部署。
- 实测路径：newWorkspaceRuntime + operationTestSession → rt.toolHandlers["read"] → 公开参数校验、真实 handler、ARC 输出封装。请求通过 mcpresult.Request，结构化结果经 JSON 归一化检查；不是仅调用底层 file.Read。
- 隔离证据：internal/server/workspace_resolve_test.go:18–43 使用 t.TempDir，并将 MCPX_HOME、配置、workspace、状态指向临时目录。没有调用常驻服务写操作，没有访问真实凭证、屏幕或外部应用。
- 主线负责宿主 schema 对比与统一 HTTP harness；此处复用其已保存证据，不重复搭建。因此本报告的 Runtime 通过不代表外层连接器或 HTTP 端到端已验收。

所有下面列出的 Go 命令都在上述指定工作树以显式 workdir 运行。实现行号和红测结论固定于审查基线，主线共享修复之后应另记验收结果。

## Codex 最接近的工具与工作流

以下 Codex 路径相对上述克隆目录。

| MCPX 语义 | Codex 具体证据 | 对应程度 |
| --- | --- | --- |
| file / window / full | codex-rs/core/src/tools/handlers/shell_spec.rs:24，create_exec_command_tool_with_environment_id；:37–69 定义 cmd、workdir、max_output_tokens；:100–120 注册 exec_command | 最接近的是通过 exec_command 执行 cat/sed。没有找到同时具有 MCPX 四视图、rev、行内字节游标和自动续读的内置 read 工具。 |
| search / list | codex-rs/core/templates/model_instructions/gpt-5.2-codex_instructions_template.md:40，要求搜索优先使用 rg / rg --files | 使用命令搜索/列举；不是 MCPX 的结构化 matches/files、revision 和双分页游标。 |
| context | 同一 exec_command 检索后读取相关范围的工作流 | 没有发现与 MCPX SmartQueryPage 综合源码上下文组装直接对应的工具，不宣称功能实现相同。 |
| 工作目录、错误、输出限制 | codex-rs/core/src/tools/handlers/unified_exec/exec_command.rs:155，ExecCommandHandler::handle_call；:186–205 解析环境/workdir；:439–478 执行及错误处理；shell_spec.rs:198–229，unified_exec_output_schema | 返回 exit_code、session_id、output、original_token_count 等。write_stdin 是进程续接，不能等同文件分页；max_output_tokens 也不等同 max_bytes_per_file。 |

检索中出现的 read_file 不能直接算对应：handlers/mcp.rs:746 等属于 filesystem MCP 测试；handlers/extension_tools.rs:82–94 的 read_file 属于 notes 命名空间。

MCPX 对应链路：

- internal/server/tools_clean_core.go:160–205：公开四视图、items、预算和游标 schema；view 可选，remote_session_id 必需；max_bytes_per_file 描述在 :175–176、:199。
- internal/server/tools_read.go:14–69：toolRead、canonicalReadRequest、inferReadView。path 单独不能唯一推导视图是已明确的契约，不列为缺陷。
- internal/server/tools_public_adapters.go:247–260：toolSourceRead 分流及 search_mode 映射。
- internal/server/tools_source_unified.go:32、:162、:197、:573、:630、:677：file、full、混合 batch、context、search、list handler。
- internal/server/tools_source.go:48–98：sourceError；internal/server/agent_guidance.go:57–105：把内部恢复动作转为公开 read。
- internal/server/observability.go:22–60：注册参数校验和包装；internal/arc/arc.go:226–242、:591–653：内部 next_action 提升为顶层 actions[]，移除 data/error 中重复字段。最终测试检查公开 actions[]，没有将正常字段搬迁误报成丢失。

## 实际通过项

新增 TestParityreadNormalAndRecovery 经注册 handler 验证：

1. full 读取 a.txt 内容为 "abcdef\nneedle\n"，检查内容、revision 字段及可见文本；offset=1、limit=1 返回 "needle\n"。
2. search(limit=1) 返回命中，原样执行公开 actions[0].arguments 成功取下一页；context、list 返回非空结果。
3. missing.txt 返回 FILE_NOT_FOUND，原样执行其公开 read(list) 恢复动作成功。
4. ../config.yaml 越界读取和 line_byte_offset=999 被拒绝。越界目标本身也位于隔离 fixture 父目录，不涉及真实配置。

实际输出摘录：

    --- PASS: TestParityreadNormalAndRecovery
    search: succeeded, nonempty matches
    context: succeeded, nonempty files
    list: succeeded, nonempty files
    missing file: code=FILE_NOT_FOUND; returned read(list) recovery succeeded
    boundary rejected: path=../config.yaml line_byte_offset=<nil>
    boundary rejected: path=a.txt line_byte_offset=999

同时运行的既有 focused 测试均通过：

- TestReadPathsAreHardScope：search/context 的 paths 硬边界。
- TestReadLongLineNextActionAdvancesWithinLine：window-only batch 长行逐段续读。
- TestReadBatchRaisesContinuationBudgetForUTF8Rune：小字节预算下 UTF-8 后续预算调整。
- TestFileReadDecodesUTF16ForModelAndWindow：UTF-16 full/window 解码。

实际命令：

    go test ./internal/server -run '^(TestParityreadNormalAndRecovery|TestReadLongLineNextActionAdvancesWithinLine|TestReadBatchRaisesContinuationBudgetForUTF8Rune|TestReadPathsAreHardScope|TestFileReadDecodesUTF16ForModelAndWindow)$' -count=1 -timeout=90s -v

结果：**PASS，5 个顶层测试，ok mcpx/internal/server 2.229s**。完整输出保存在 /tmp/mcpx-parity-read-passing-20260926.log。

## P2-1：单文件字节预算在 batch 中被忽略，续读动作也丢失预算

临时 a.txt 为 "abcdef\nneedle\n"，full.txt 为 "x"。以下调用均由测试添加临时 remote_session_id。

单文件第一次调用：

    {"view":"file","path":"a.txt","limit":1,"max_bytes_per_file":2}

首次返回 "ab"。公开续读动作 arguments 为以下内容，缺少 max_bytes_per_file；仅临时 session ID 用占位符代替：

    {"limit":1,"line_byte_offset":2,"offset":0,"path":"a.txt","remote_session_id":"<fixture-session>","view":"file"}

原样执行动作的真实输出：

    continuation: {"content":"cdef\n","truncated":true}
    returned next_action lost max_bytes_per_file: got 5 bytes, want <= 2

batch 请求：

    {"items":[{"path":"a.txt","mode":"window","limit":1}],"max_total_bytes":100,"max_bytes_per_file":2}

真实输出：

    window item: {"content":"abcdef\n","total_bytes":7,"truncated":true}
    batch ignored max_bytes_per_file: got 7 bytes, want <= 2

追加 {"path":"full.txt","mode":"full"} 进入混合实现后，a.txt 仍返回 7 字节，total_bytes=8，同样失败。

最小复现命令（所选测试已在下文总命令中实际执行）：

    go test ./internal/server -run '^TestParityread(SingleContinuationPreservesByteBudget|BatchHonorsPerFileBudget)$' -count=1 -timeout=90s -v

证据与原因：

- tools_source_unified.go:142 使用 readWindowMaxBytes 实现单文件预算；:156 / :334–346 的 readContinuationArguments 没有保留该预算。
- :74 向 source.ReadBatch 直接传配置 MaxReadBytes，忽略请求 max_bytes_per_file；混合窗口 :269–273 也只比较配置上限和剩余总预算。
- 回归位于 parity_read_test.go:148、:166，单文件续读及 batch 的 window/mixed 子测试均失败。

影响：客户端显式要求小块结果时，batch 首次调用和单文件自动续读均可能超出请求预算，增加上下文消耗及缩小后重试。当前证据没有证明服务崩溃或死锁。

建议最小修复：window batch 复用 readWindowMaxBytes；混合 window 再取剩余总预算的较小值；续读动作保留适用预算。full 的完整二进制语义应继续明确处理预算不足，不能截断后伪装完整成功。无需重构整个 read。

## P2-2：混合 full/window 批量截断缺少可执行恢复动作

最小请求：

    {"items":[{"path":"a.txt","mode":"window"},{"path":"full.txt","mode":"full"}],"max_total_bytes":2}

注册 handler 返回 succeeded，公开 actions[] 缺失。真实业务结果摘录，省略 format/rev 等无关字段：

    {
      "budget_bytes":2,
      "results":[
        {"path":"a.txt","mode":"window","ok":true,"content":"ab","truncated":true,"next_offset":0,"next_line_byte_offset":2},
        {"path":"full.txt","ok":false,"error":{"code":"RESULT_BUDGET_EXCEEDED","message":"batch result budget exhausted"}}
      ],
      "total_bytes":2,
      "truncated":true
    }

真实失败输出：

    truncated mixed batch has no executable action; exhausted full item has no recovery
    --- FAIL: TestParityreadMixedBatchProvidesContinuation

最小复现命令（该测试已在总命令中实际执行）：

    go test ./internal/server -run '^TestParityreadMixedBatchProvidesContinuation$' -count=1 -timeout=90s -v

证据与原因：

- tools_source_unified.go:238–245 在预算耗尽时添加逐项错误但不生成恢复参数；:289–299 对 window 仅输出 next_offset / next_line_byte_offset。
- 对比 window-only 分支 :109–125 的 next_action，混合分支没有对应动作，ARC 无法产生公开 actions[]。这不是 ARC 吞掉已有动作。
- 回归位于 parity_read_test.go:190；实际检查注册 handler 最终输出，而非仅检查未包装的内部 data。

影响：依赖服务端动作继续的客户端会停在部分结果；模型须自行重组原 items、mode、游标及未读取项，容易重读前缀或遗漏 full 文件。可以人工恢复，故定为 P2，不宣称完全不可恢复。

建议最小修复：收集截断窗口和未读取项的剩余请求，保留各自 mode、游标及预算，生成公开 read 续读动作。若单个 full 项大于整个预算，应明确提供增大预算或改 window 的恢复选择，避免生成原地循环动作。

## 宿主连接器限制

ALL_TOOLS 可发现 mcp__codex_apps__macmcpx_read，实际签名顶层及 items[] 都缺少 line_byte_offset；当前源码公开 schema 在 tools_clean_core.go:167、:185 包含该字段。

主线保存的 bin/parity-evidence/catalog-drift.json:134–142 同样记录 read missing_fields=["line_byte_offset"]；原始证据在同目录 server-tools.json、host-tools.json。本 agent 读取现有差异证据，没有重复进行宿主 schema 对比。

确切障碍：长行续读需要传回行内字节偏移，但宿主连接器无法表达该参数。没有对连接器传未定义字段，也没有通过 execute 或其他工具绕过契约/权限，没有创建常驻会话。服务端 fixture 续读通过不代表宿主路径已通过；连接器刷新与统一端到端验收归主线。

## 原始红测、交付与未覆盖

实际执行：

    go test ./internal/server -run '^TestParityread' -count=1 -timeout=90s -v

结果：**exit 1；1 个顶层测试通过、3 个顶层回归测试失败**，其中 batch 的两个子测试均失败；这 3 个测试对应上述 2 个问题。最终原始输出为：

    --- PASS: TestParityreadNormalAndRecovery (0.07s)
    --- FAIL: TestParityreadSingleContinuationPreservesByteBudget (0.03s)
    --- FAIL: TestParityreadBatchHonorsPerFileBudget (0.07s)
        --- FAIL: TestParityreadBatchHonorsPerFileBudget/window (0.04s)
        --- FAIL: TestParityreadBatchHonorsPerFileBudget/mixed (0.04s)
    --- FAIL: TestParityreadMixedBatchProvidesContinuation (0.03s)
    FAIL mcpx/internal/server 0.743s

完整日志：/tmp/mcpx-parity-read-20260926.log。保留目标契约断言，没有 skip 或以断言缺陷存在来伪造通过。

编写测试时曾遇到字符串转义导致的编译错误，以及初版误从 data 取 next_action 导致的测试 panic；两者都已修正，以上结论基于修正后检查公开 actions[] 的实际执行结果，不将测试作者错误列为产品缺陷。之后仅补充了注释和日志标签，未修改回归断言；又完成上述 5 项通过测试。

未覆盖：外层连接器调用、OAuth/远端网络、主线统一 HTTP harness、非 macOS 平台、并发修改文件一致性、超大文件资源消耗、二进制/图片端到端展示，以及全部参数组合。没有跑全套、race 或生产检查。Codex 只读源码对照，没有运行 Codex 测试。

用户已明确由主线接手 read 预算与续读修复。本 agent 按要求停止新增调查和测试，交付原始红测与证据；不宣称主线修复已通过验收。
