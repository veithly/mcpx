# edit 工具对标审查（2026-09-26）

审查时确认 **P1 缺少更新载荷会清空文件、P2 幂等冲突发布不可执行恢复动作**，均经隔离 MCP HTTP 实际调用复现。保留原始红测供主线整合。正常编辑链路、批量预检和已有持久化故障恢复测试通过。

**交付状态：主线已告知两项 edit 问题修复。本 agent 按收尾指令没有复验最终修复；以下是修复前证据，不是修复后测试结果。** 本 agent 只新增专属测试与本文，未修改实现源文件。

## 范围与环境

- 唯一工作树：/Users/rick/.codex/worktrees/mcpx-tool-reliability/mcpx；分支 codex/tool-reliability-20260925；核对 HEAD/base 为 6f799d2ddfa7ab9cbfbe0493823af6db11e918df。
- Codex 只读源码：/tmp/mcpx-codex-cli-20260925；核对 commit 为 68e0c9f5d8fd9449e97a81e92e8fcb86795713b2。未联网、新 clone 或运行 Codex 构建测试。
- 已阅读适用 AGENTS.md 和 RTK.md。本工作树 go.mod 要求 1.26.1，实际 Go 为 go1.26.6 darwin/arm64。
- 测试复用 newWorkspaceRuntime（internal/server/workspace_resolve_test.go:18）：MCPX_HOME、配置、SQLite、workspace 全在 t.TempDir()。使用 httptest.NewServer 随机本地端口和官方 SDK StreamableClientTransport，经过公开 schema、注册 handler、ARC 输出，检查磁盘字节、isError、structuredContent、actions 和重放前后的 inode/mtime。
- 所有 shell 命令显式使用指定工作树。未编辑旧 main，未调用常驻服务写操作，未访问真实凭证、私人屏幕、外部应用，未重启、部署、commit/push、启动子 agent 或运行全套测试。
- 主线同时修改共享工作树的全局 tool_replay.go、tool_response.go 及对应测试；这些不属于本审查。复现后曾确认 edit 专属实现文件相对 HEAD 无差异。测试运行于当时共享工作树，不能声称整个工作树是纯 base 状态。

## Codex 对照

Codex 没有与 MCPX edit 同名同契约的直接对应工具；最接近的是 apply_patch。以下 Codex 路径相对 /tmp/mcpx-codex-cli-20260925/codex-rs，MCPX 路径相对指定工作树。行号为审查时版本；主线修复后可能移动。Codex 对照是源码证据，未动态执行。

| 维度 | Codex 函数与行号 | MCPX 函数与行号 |
|---|---|---|
| 公开输入 | core/src/tools/handlers/apply_patch_spec.rs:9，create_apply_patch_freeform_tool；18–26 行定义 freeform + Lark。core/assets/tools/apply_patch.lark:1–16 定义 add/delete/update/move | internal/server/tools_clean_core.go:207–260，editItem 和注册；JSON create/update/rename、rev、range、replacements、exact bytes；update/rename 条件要求 rev |
| 解析验证 | core/src/tools/handlers/apply_patch.rs:316，ApplyPatchHandler::handle_call；337–367 行 parse、环境选择和 verify；386–400 行向模型返回错误 | internal/server/tools_edit.go:88，toolEdit；89–136 行会话、解析、删除边界和策略。parseCleanEdits:478，520–526 行拒绝旧 base_sha256 并要求 rev |
| 空更新保护 | apply-patch/src/streaming_parser.rs:53，ensure_update_hunk_is_not_empty；53–70 行拒绝空 update hunk。parser.rs:309–320 有对应断言 | internal/edit/apply.go:420，applyUpdate；430–437 行无 range/非空 replacements 时回退完整内容替换，见 P1 |
| 定位、格式 | apply-patch/src/file_update.rs:26，derive_new_contents_from_chunks；48–75 行区分 NormalizeToLf/PreserveLineEndings。compute_replacements:87 使用上下文定位 | internal/edit/apply.go:440 的 applyLineRange 和 :482 的 applyReplacements；精确片段要求唯一，已实测歧义拒绝与 CRLF 保留 |
| 执行、输出 | core/src/tools/handlers/apply_patch.rs:582–608 通过 ToolOrchestrator/ApplyPatchRuntime 执行并发事件。apply-patch/src/lib.rs:862，print_summary 输出 A/M/D 路径 | internal/server/diff_preview.go:57–117，editResponseData 返回逐文件 diff、readback、changed lines、edit_id、重放标记和有界预览 |
| 部分失败、重试 | apply-patch/src/lib.rs:314，ApplyPatchFailure 带已提交 delta；apply_hunks_with_options:423–451 保留失败及 delta；所审阅输入无文件 rev、业务 idempotency_key 同等字段 | tools_edit.go:137–190 Claim/replay，240–294 处理 in_doubt；edit_idempotency.go:69–90，replayStoredEdit 按持久化状态重放；已有相关故障恢复测试实跑通过 |

Codex 支持删除不意味着 MCPX 应把删除加入 edit；MCPX 当前契约将删除交给 move_out，本次未改变该边界。

## P1：省略更新载荷或空 replacements 导致静默清空

前置文件 keep.txt 内容为 keep this content 加换行。使用当前合法 rev，省略所有更新载荷或传 replacements: []，公开调用均返回 succeeded、isError=false，文件变成零字节。模型漏字段、批量计划生成空替换列表时可能无意丢失内容。

最小业务请求，测试自动创建隔离 SESSION 和真实 REV：

    {"remote_session_id":"<SESSION>","purpose":"isolated edit parity audit","edits":[{"path":"keep.txt","operation":"update","rev":"<REV>"}]}

另一触发是加入 replacements: []。不是缺少 revision 的请求。最小复现命令（workdir 为指定工作树）：

    go test ./internal/server -run '^TestParityeditMissingUpdatePayloadMustNotTruncate$' -count=1 -v

修复前定向复跑真实输出摘录，退出码 1：

    mode=omitted isError=false status=succeeded code= file=""
    missing update payload must reject without changing bytes; got status=succeeded isError=false file=""
    mode=null-content isError=true status=failed code=invalid_arguments file="keep this content\n"
    mode=empty-replacements isError=false status=succeeded code= file=""
    missing update payload must reject without changing bytes; got status=succeeded isError=false file=""
    mode=explicit-empty-content isError=false status=succeeded code= file=""
    --- FAIL: TestParityeditMissingUpdatePayloadMustNotTruncate (0.22s)
    FAIL    mcpx/internal/server    0.594s

对照组：显式 content: "" 合法清空；content: null 被公开 schema 拒绝且不写入。确认缺陷限定为缺少载荷和空 replacements 的静默清空。

原始根因：tools_clean_core.go:248–252 仅要求 rev，没有要求更新模式；parseCleanEdits:495–511 解码为 FileEdit.Content string（internal/edit/types.go:54），丢失“省略”和“显式空值”的区别；applyUpdate:430–437 无条件回退 Content。

最小修复建议：在 parseCleanEdits 原始 JSON item 上验证 update 明确选择载荷模式，拒绝缺载荷和空 replacements，保留显式空 content；同步 schema 的模式约束及 replacements minItems: 1。不能只检查 Content 非空，否则会禁止合法清空。主线已告知修复，本 agent 未复验最终实现。

测试：internal/server/parity_edit_test.go:117，TestParityeditMissingUpdatePayloadMustNotTruncate。原始日志：/tmp/mcpx-parity-edit-payload-20260926.log。

## P2：幂等冲突恢复动作违反 edit 自身输入契约

第一次 key=parity-conflict 创建 once.txt（first 加换行）成功；第二次同 key 改为 changed 加换行，正确返回 IDEMPOTENCY_CONFLICT，原文件不变。但顶层 actions[0] 发布 id=edit、type=continue，arguments 只有 remote_session_id 和未声明字段 note。

测试原样将返回 action 的 id/arguments 发回隔离 MCP HTTP，证明恢复动作不可执行。最小命令：

    go test ./internal/server -run '^TestParityeditConflictRecoveryMustBeExecutable$' -count=1 -v

修复前定向复跑真实输出摘录，退出码 1：

    conflict recovery tool=edit arguments={"note":"use a new idempotency_key; do not reuse the conflicting key","remote_session_id":"rs_ZybLzLvl3gWtXayF"}
    published recovery action is rejected by its own input contract
    --- FAIL: TestParityeditConflictRecoveryMustBeExecutable (0.07s)
    FAIL    mcpx/internal/server    0.629s

恢复响应的真实字段摘录：

    isError=true
    status=failed
    error.code=INVALID_ARGUMENTS
    error.category=validation
    error.retryable=false
    error.details.execution_state=not_started
    error.details.safe_to_retry_unchanged=false
    error.details.schema_source=tools/list
    error.message=validating root: unexpected additional properties ["note"]; edit update/rename requires rev returned by read. Refresh tools/list or reconnect the connector if its tool definition differs.

原始根因：editIdempotencyConflict（internal/server/edit_idempotency.go:93）在 100–103 行生成不完整 Recovery.Arguments；tools_clean_core.go:254–260 要求 purpose、edits，且不声明 note。internal/arc/arc.go:591 的 actionsFrom 和 :657 的 canonicalAction 把它公开为顶层动作。静态也确认 purpose/edits 缺失，但实测首先报告未知 note，未将未出现的错误消息冒充运行证据。

影响：跟随服务建议必定多走一次失败调用，错误文案还引导刷新 schema，容易误诊为宿主定义过时。幂等冲突保护本身通过，没有第二次写入。

最小修复建议：删除该分支不可执行的 Recovery，把“改变业务参数需新 key”保留在最终可见的文字指导；若保留 action，必须含完整有效参数及正确新 key，不能把 note 放入 arguments。无需修改全局 replay。主线已告知修复，本 agent 未复验。

测试：internal/server/parity_edit_test.go:148，TestParityeditConflictRecoveryMustBeExecutable。允许修复时移除不可执行 action；若仍发布则实际调用不能因输入契约拒绝。原始日志：/tmp/mcpx-parity-edit-recovery-20260926.log。

## 通过项和测试结果

| 路径 | 动态证据 |
|---|---|
| 预览、创建、read rev、range update、同 key 重放、rename | TestParityeditNormalWorkflow 经 MCP HTTP 通过；预览不创建文件，中文带空格路径成功，CRLF 保留，diff 含 +TWO，readback 存在，重放 inode/mtime 不变，rename 旧路径消失且新路径内容正确 |
| 过期 rev | TestParityeditValidationBoundaries/stale 返回 stale_revision，原字节不变；执行公开 read action 成功取得当前 rev |
| 批量预检、歧义、越界、行数上限 | ambiguous-batch/range-bounds/line-cap 分别返回 match_ambiguous/range_out_of_bounds/too_many_changes；原文件不变，批次前面的 create 未落盘，1001 行拒绝 |
| 持久化状态与故障恢复 | 实跑通过 TestEditReplayFollowsRecordState、TestEditInDoubtOriginalStateRerunsSameKey、TestEditInDoubtPartialWriteStaysInDoubt、TestReviewFirstEditCompleteFailureRecoversSameStore，覆盖状态区分、未写入时同 key 恢复、部分写入保持不确定、完成持久化故障后不重复写入 |

通过组命令，workdir 均为指定工作树：

    go test ./internal/server -run '^(TestParityeditNormalWorkflow|TestParityeditValidationBoundaries|TestEditReplayFollowsRecordState|TestEditInDoubtOriginalStateRerunsSameKey|TestEditInDoubtPartialWriteStaysInDoubt|TestReviewFirstEditCompleteFailureRecoversSameStore)$' -count=1 -v

结果：6 个顶层测试通过，退出码 0，ok mcpx/internal/server 1.955s。完整日志：/tmp/mcpx-parity-edit-passing-20260926.log。

新增测试总入口：

    go test ./internal/server -run '^TestParityedit' -count=1 -v

原始结果：2 个顶层通过、2 个顶层失败，FAIL mcpx/internal/server 1.325s。失败就是 P1/P2，未跳过或改为接受错误行为的绿色测试。日志：/tmp/mcpx-parity-edit-20260926.log。专属测试文件 gofmt 已通过。

测试编写时曾误从 error.recovery 读取恢复动作；已按 ARC 实际契约改为直接读取原始 structuredContent.actions，并重新运行以上测试。最终 P2 是真实公开 action 调用失败，不是测试读取错误。

## 宿主阻碍和未覆盖项

- 工具目录实际发现 mcp__codex_apps__macmcpx_edit，其声明仍暴露 base_sha256，缺少当前必需 rev，以及 content_base64/newline_policy/expected_format。当前 handler 拒绝 base_sha256 并要求 rev，因此宿主声明不能表达有效 update/rename。未调用连接器、未注入隐藏字段、未绕过契约。
- 主线已保存 bin/parity-evidence/server-tools.json、host-tools.json、catalog-drift.json 并负责宿主 schema 对比；本任务不重复统一 harness。隔离 HTTP 验证不等于 MacMCPX 连接器验收通过。
- 未测真实断电/进程 crash、磁盘满、跨进程并发写入、符号链接竞争、Windows/Linux、真实外部鉴权网络。fixture 故障注入只证明相应分支。
- 未扩大 exact bytes、UTF-16、超大 diff 分页验证；没有把其他仅静态发现的分支列为确认缺陷，没有全套、race 或部署验收结论。
- 主线修复后的绿色验证由主线完成；本 agent 按收尾指令未新增调查或测试。

## 本 agent 交付文件

- /Users/rick/.codex/worktrees/mcpx-tool-reliability/mcpx/internal/server/parity_edit_test.go
- /Users/rick/.codex/worktrees/mcpx-tool-reliability/mcpx/docs/results/tool-parity-20260926/edit.md
