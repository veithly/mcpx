# skill_tool 对标审查（2026-09-26）

- 范围：仅虚构本地 Markdown Skill 的 list / describe / call 与错误恢复。
- 工作树：/Users/rick/.codex/worktrees/mcpx-tool-reliability/mcpx。
- 分支 codex/tool-reliability-20260925，HEAD/base 6f799d2ddfa7ab9cbfbe0493823af6db11e918df；工作树已有他人修改，未触碰。
- 结论：正常流程、未知名称错误、revision 更新后的恢复通过；另有 1 项 P2 已复现，保留失败回归测试供主线修复。

## Codex 对照

只读本地 /tmp/mcpx-codex-cli-20260925，核对 commit 68e0c9f5d8fd9449e97a81e92e8fcb86795713b2。
- 最接近 list：codex-rs/ext/skills/src/extension.rs:579 的 list_skills，调用 providers.list_for_turn。
- 最接近 describe / Markdown call：同文件 :596 的 read_main_prompt，通过 thread_state.read_skill 读取资源，并将错误返回为 Result；:604 为实际调用点。
- 另有 codex-rs/app-server/src/request_processors/catalog_processor.rs:515 的 skills_list_response；:526 起 force_reload 清理缓存。
- 上述工作流是技能发现与说明读取，不能将其等同于 MCPX 执行 Python/Node entry；本次没有建立可执行 Skill call 的直接对应。

## MCPX 契约与实测

- tools_catalog.go:436-452 定义 list/describe/call；call 必需 remote_session_id、purpose、name，arguments 为 object，允许幂等键和执行模式。
- tools_discover.go:28 分发；:45 list，:59 describe，:140 preflight 校验 Skill 存在、revision 和参数。
- tools_ext.go:514 的 toolSkillExecute 执行并区分 skill_error、SKILL_FAILED；internal/skill/exec.go:16 的 Execute 对 Markdown 只读文件。
- 已发现 MacMCPX 的 skill_tool 连接器声明；未调用常驻服务，也未重复主线的宿主 schema/端到端 harness 对比。
- 实测复用 workspace_resolve_test.go:18 的 newWorkspaceRuntime 与 callEnvelope，HOME、MCPX_HOME、Skill 文件都在 t.TempDir；直接进入真实 toolSkillTool handler。
- 此证据属于隔离 Runtime Go fixture，不代表 MCP HTTP 或宿主连接器验收。

运行命令（工作目录为上述工作树）：

    go test ./internal/server -run '^TestParityskill_tool' -count=1 -v

真实输出摘录：

    list=1 skill; describe.instructions=# Parity v1; call.content=# Parity v1
    missing=skill_not_found; changed=skill_revision_changed; describe+call recovered=# Parity v2
    --- PASS: TestParityskill_toolListDescribeCallAndRecovery (0.58s)
    describe.status=ok instructions=<nil>; call.status=failed error=skill_error
    describe reported success despite unreadable markdown entry; expected an actionable read error
    --- FAIL: TestParityskill_toolDescribeMissingEntryMustFail (0.31s)
    FAIL mcpx/internal/server 1.454s

## P2：describe 吞掉 Markdown 入口读取错误

- 最小条件：临时 Skill 的 skill.yaml 指定 runtime: markdown、entry: absent.md，入口文件不存在。
- 复现命令：go test ./internal/server -run '^TestParityskill_toolDescribeMissingEntryMustFail$' -count=1 -v。
- 实际：describe 返回成功且无 instructions；随后 call 才返回 skill_error，见上面的已运行组合命令输出。
- 影响：无法在 describe 阶段取得必需说明，也没有读取错误提示；调用方需再 call 一次才能知道文件损坏。
- 根因证据：tools_discover.go:89 仅在 skillInstructions 无错误且非空时添加 instructions；任何读取错误均被丢弃，随后仍 remoteResult 成功。
- 最小建议：将 skillInstructions 的读取错误转为可操作失败；成功读取后再保存 discovery lease。不要增加兼容层或要求模型管理 revision token。
- 本轮只添加复现测试，未修改实现；该测试保持红色，不能宣称全部通过。

## 通过与未覆盖

- 通过：单一虚构 Skill 的 list、describe 正文、call 正文；未知名称返回 skill_not_found；变更后 call 拒绝，重新 describe 后 call 读到 v2。
- 未覆盖：Python/Node 执行、异步与取消、参数 schema 失败、符号链接边界、平台依赖、HTTP/宿主公开输出 schema。
- 显式幂等失败重放保持主线既定语义，新键指引由主线处理，本轮不追加该项调查。
- 未联网、未读取真实 Skill/凭证、未访问外部应用、未重启/部署/提交/推送；无子 agent。
- 原始日志：/tmp/mcpx-skill-tool-parity-20260926.log。
