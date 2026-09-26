# move_out 对标审查（2026-09-26）

结论：本轮关键路径通过，无确证缺陷；不建议推测性重构。

## 范围与环境
- 工作树：/Users/rick/.codex/worktrees/mcpx-tool-reliability/mcpx。
- 分支 codex/tool-reliability-20260925，HEAD/base 6f799d2ddfa7ab9cbfbe0493823af6db11e918df。
- 验证对象为当前共享工作树，含其他 agent 未提交修改；不能等同于纯 base 或常驻服务验证。
- Runtime、Workspace、文件、模拟回收站均为 t.TempDir；用户确认仅 fixture 模拟。
- 未修改实现、旧 main；未调用常驻服务写操作、真实应用、凭证、重启、部署、commit/push。

## Codex 对照（只读）
- 本地 /tmp/mcpx-codex-cli-20260925，已核验 commit 68e0c9f5d8fd9449e97a81e92e8fcb86795713b2。
- 最近对应：apply_patch 的删除操作；不是回收站协议。所查 tools/apply-patch 源码没有 move_out 或 confirmation_uuid 对应项。
- codex-rs/core/src/tools/handlers/apply_patch.rs:316，ApplyPatchHandler::handle_call：337 解析，360 验证路径及文件系统，377 执行已验证 patch。
- codex-rs/apply-patch/src/lib.rs:470，apply_hunks_to_files：536 DeleteFile 分支，552 拒绝目录，561 remove(recursive=false, force=false)，588 保留删除内容到 delta。
- MCPX 独有 prepare→用户确认→submit、冻结 manifest、服务端 UUID、系统回收站、目录与 symlink 支持；不将差异当作缺陷。

## MCPX 契约与实现证据
- internal/server/tools_clean_core.go:262–296：prepare 需 session/purpose/targets；目标仅 path/rev/expected_sha256，类型由服务端推断；submit 需 session/confirmation_uuid。
- internal/server/tools_workspace_delete.go:66 toolMoveOut 分派；82 toolWorkspaceMoveOutPrepare 冻结清单；151 toolWorkspaceMoveOutCommit 核对身份、Workspace、用途、TTL 和状态。
- 同文件 :606 inferMoveOutTargetKinds 要求普通文件 revision；:735 lstatMoveOutTarget 校验路径和策略；:757 rejectSymlinkParentComponents 拒绝 symlink 父路径。
- 同文件 :471 moveOutPrepareResult 返回三字段 submit 动作；:522 moveOutCommitReplay 返回持久化结果；:532 moveOutCommitResponseData 给出成功/失败计数及目标预览。
- 已发现 MacMCPX move_out connector；按分工未重复宿主 schema 对比，也未调用常驻服务。以下直接验证隔离 MCP HTTP 的注册工具。

## 实际调用与结果
- 新增 internal/server/parity_move_out_test.go:71 TestParitymove_outRoundTrip。
- 使用现有 newWorkspaceRuntime、installMoveOutTrashMocks，注册真实工具并通过 httptest MCP Streamable HTTP 调用。
- prepare 不移动文件；将返回的三字段 submit 原样发回；文件移至模拟回收站且字节完整；原样重放 submit 成功。
- 真实输出：`prepare unchanged; returned submit executed unchanged; moved_count=1; replay=true; temporary trash bytes intact`。
- 新增 :108 TestParitymove_outRecovery：旧 rev prepare 返回 IsError=true；文件内容保持不变。
- 真实错误：`code=STALE_REVISION`，`message=rev does not match current file`，`safe_to_retry_unchanged=false`。
- 真实提示：`Read the current revision and regenerate the operation.`；本响应无结构化 actions，不宣称已执行错误恢复动作。
- 现有 TestMoveOutPrepareInfersWorkspaceKindAndIdempotency、TestMoveOutSimplifiedSchemaKeepsFileRevisionGuard 通过（move_out_ergonomics_test.go:11、61）。
- 现有目录原子移出/确认缺失/幂等重放、stale submit/路径越界/仅预览用途、symlink 不跟随测试通过（tools_move_out_test.go:82、183、341）。

## 复现命令及验收
在上述工作树运行：
```sh
go test ./internal/server -run '^(TestParitymove_out|TestMoveOut)' -count=1 -v
```
实测 7 项全部 PASS，`ok mcpx/internal/server 5.577s`，退出码 0。
完整日志：/tmp/mcpx-parity-move-out-20260926.log。
只新增工具专属测试和本报告；未跑全套。

## 问题、修复建议及未覆盖
- 可复现缺陷：无；严重度/修复：不适用。
- macOS Finder/TCC、Windows 回收站、真实 Linux 桌面回收站未验收；本轮使用命令 mock。
- 不覆盖真实用户确认 UI、确认超时/服务崩溃恢复、并发路径替换或系统回收站恢复操作。
- 无结构化 stale 恢复动作，已验证其拒绝行为与文字提示；未进一步运行重新读取及重新 prepare 流程。
