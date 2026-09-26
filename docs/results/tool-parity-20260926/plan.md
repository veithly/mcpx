# plan 逐工具对标审查

- 范围：指定 worktree，codex/tool-reliability-20260925，HEAD 6f799d2ddfa7ab9cbfbe0493823af6db11e918df。
- 实测包含其他 agent 的未提交改动；初次核查 plan/service 和 clean_idempotency 对 HEAD 无 diff；最终调用已观察到主线并行补充的失败重放提示。
- 结论：正常流程通过；失败终态的同键稳定重放符合显式幂等契约，新执行使用新键。先前恢复指引不足已由主线补充，注册 handler 实测可见；未确认其他缺陷，不改变幂等语义。

## Codex 对照（本地 clone 68e0c9f5d8fd9449e97a81e92e8fcb86795713b2）

- codex-rs/core/src/tools/handlers/plan_spec.rs:7-57 的 create_update_plan_tool：必填 plan 数组，step/status；可选 explanation。
- codex-rs/protocol/src/plan_tool.rs:9-29：pending/in_progress/completed，拒绝未知字段。
- codex-rs/core/src/tools/handlers/plan.rs:66-110 的 handle_call/parse_update_plan_arguments：解析、发送 PlanUpdate；Plan mode 禁用。
- 同文件 :22-44 的 PlanToolOutput 返回 Plan updated（code mode 为 {}）。
- 最接近的是清单整体更新；该工具没有 MCPX 的计划 ID、依赖图、证据完成、单独读取及 deliver，没有直接对应，不视为差距。

## MCPX 契约与实际调用

- internal/server/tools_catalog.go:407-421：create/read/advance/complete/block/replan/deliver；写动作要求 purpose，幂等键可选。
- internal/server/tools_plan.go:17-53：任务 title 必填、local_id 用于创建依赖引用；证据要求 kind/reference_id。
- internal/server/tools_plan_clean.go:15-42：read→get、advance→start_task；除 deliver 外写动作经 withCleanIdempotency。
- internal/server/tools_plan.go:56-135,193-207：返回计划、任务或交付数据；planMap 移除内部 goal。
- internal/plan/service.go:120-194：完成要求证据，启动检查依赖，错误路径回滚；blocked 可再次 advance。
- fixture internal/server/workspace_resolve_test.go:18-43 使用 t.TempDir；MCPX_HOME、workspace、数据库、配置全部隔离，无常驻服务写入。
- 先前 TestParityplanCreateAdvanceRead 输出 create=ok advance=ok read=ok task_status=in_progress（直接 handler 路径）。
- 先前 TestCleanCorePlanEvidenceAndArtifactWorkflow 通过（internal/server/clean_core_p1_p3_test.go:158-260）：创建幂等重放、推进、隔离编辑/执行/产物证据、完成及交付 ready=true。
- 最终恢复测试使用 rt.toolHandlers["plan"]；internal/server/observability.go:22-59 证明它是绑定 schema validator、instrumentTool 和 boundedTool 的注册 handler。
- 不将注册 handler 调用表述为 MCP HTTP 端到端验收。正常链测试也已改为注册 handler，最终仅重跑下述恢复测试。

## 已验证边界：依赖失败重放与新键恢复（先前提示不足已补充）

- TestParityplanDependencyRecovery：创建 first 和依赖它的 second；带键 advance second 被拒；replan 移除依赖；同键重试；read；新键执行。
- 首次真实错误：PLAN_INVALID_REQUEST，category=validation，retryable=false；message 为 plan dependency is not satisfied: task ... depends on ...（省略随机 ID）。
- 首次失败提示为 Correct the request arguments and retry.，actions=<nil>；先前直接 handler 重放也只有此通用提示。最终注册 handler 重放明确提示：This is the recorded failed outcome for this idempotency_key, not a new execution. Inspect any partial effects; after correcting the cause, use a new idempotency_key for an intentional new execution.（已观察到主线修改）。
- 同键重试继续 failed；read 为 todo 且 depends_on=<nil>；换新键则 ok、in_progress。上述重放与恢复本身均为期望行为。
- 先前提示来源：internal/server/tools_plan.go:217-236 仅为证据错误提供专门恢复动作，依赖错误走普通 terminalError。
- internal/server/clean_idempotency.go:178-186,225-229 明确持久化并重放失败终态，保持现有契约。
- 最小建议已由主线落实于实测重放输出：明确缓存失败，并提示修复原因后以新键发起新执行。本 agent 不修改实现，无额外修复建议。
- 测试断言同键失败状态、错误码、原因、类别及 retryable 稳定，允许重放补充恢复提示；并验证依赖修复已持久化、新键成功。未修改实现。

## 复现与验收

工作目录：/Users/rick/.codex/worktrees/mcpx-tool-reliability/mcpx

    go test ./internal/server -run '^TestParityplanDependencyRecovery$' -count=1 -v

- 最终结果：TestParityplanDependencyRecovery PASS，退出码 0。日志：/tmp/mcpx-plan-parity-recovery-20260926.log。
- 先前错误断言产生的红测已撤回，不作为实现缺陷或验收失败证据。
- 未覆盖：宿主 schema/连接器、MCP HTTP、并发/重启、跨会话授权、async、其他图边界及外部证据平台；未运行 Codex 测试。
- 未运行全套、常驻服务写操作、部署、重启、commit/push。只修改 parity_plan_test.go 和本报告。
