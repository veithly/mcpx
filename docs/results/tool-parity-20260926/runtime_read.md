# runtime_read 逐工具对标审查

- 日期：2026-09-26；范围仅 runtime_read。结论：正常流程通过，确认 1 个 P2 边界缺陷（两条复现路径）。
- 工作树：`/Users/rick/.codex/worktrees/mcpx-tool-reliability/mcpx`，分支 `codex/tool-reliability-20260925`，HEAD/base `6f799d2ddfa7ab9cbfbe0493823af6db11e918df`。
- 共享工作树有其他 agent 的修改；本审查未改它们。核心 schema、adapter、instruction handler/解析器与 base 的定向 diff 为空。公共结果包装器存在其他人的改动，测试结果代表本轮工作树。
- 只写本报告及 `internal/server/parity_runtime_read_test.go`；没有联网、子 agent、服务操作、提交或部署。

## Codex 对照（只读源码）

- 固定 checkout：`/tmp/mcpx-codex-cli-20260925`，核实 HEAD 为 `68e0c9f5d8fd9449e97a81e92e8fcb86795713b2`。
- 无直接同名、同时覆盖 capabilities/project/instructions 的工具；对应能力分布在工具发现及指令加载流程。
- `codex-rs/core/src/tools/handlers/tool_search_spec.rs:16` 的 `create_tool_search_tool`，`:94` 描述 BM25 deferred-tool 发现；对应 MCPX capabilities 的能力发现用途，但不是运行时总览。
- `codex-rs/core/src/agents_md.rs:58` 的 `load_project_instructions`、`:125` 的 `read_agents_md`、`:192` 的 `agents_md_paths` 对应适用指令链。`:150` 经 ExecutorFileSystem 与 sandbox 读取正文，`:156` 起按剩余字节预算截断；`:217` 起从项目根至 cwd 构建目录链。
- `codex-rs/core/src/tools/handlers/mcp_resource/read_mcp_resource.rs:78-102` 解析/规范化 server、uri，检查可访问 server，再读取资源并把错误返回模型；这是资源读取流程，不能称为 runtime_read 等价工具。
- 未运行 Codex 测试；没有声称其通过同一越界用例。

## MCPX schema、handler 与实测

- `internal/server/tools_catalog.go:354-357`：公开字段 remote_session_id、workspace、view、anchor_path、paths；view 枚举 capabilities/project/instructions，无必填字段，read-only 注解。
- `internal/server/tools_public_adapters.go:263-280`：显式 view 优先，否则非空 anchor_path/paths 推导 instructions，默认 capabilities。
- `internal/server/tools_manage.go:45-55` 分派三种视图；`runtime.go:1012` 返回能力、版本及 revision，`tools_source.go:24` 返回项目摘要。
- `internal/server/tools_instruction.go:12-49`：解析显式工作区，发现指令并返回 descriptors、instruction_revision；paths 额外返回逐路径 resolution。这里没有指令正文，不据此单独判为缺陷。
- 复用 `workspace_resolve_test.go:18` 的 newWorkspaceRuntime：MCPX_HOME、配置、SQLite、工作区全部位于 t.TempDir；测试通过已注册 `toolHandlers["runtime_read"]` 实际调用，非仅调用路径解析函数。
- 新增正常测试 `parity_runtime_read_test.go:11`：空参数 capabilities、显式工作区 project、自动推导 instructions 均 isError=false；核实 runtime 字段及子目录指令 ID。
- 现有 TestRuntimeReadWorkspaceViewsDoNotRequireSession 验证 project/instructions 无需 Session，遗漏工作区返回 WORKSPACE_REQUIRED，冲突 Session/工作区被拒绝。

## P2：指令链越出工作区后仍成功返回，并标记 active

- 最小布局由回归测试创建：临时 home/demo 为注册工作区；home/outside/AGENTS.md 为 21 字节测试文件；demo/linked 为指向 outside 的目录符号链接。全部为隔离测试数据。
- 请求一：`{"workspace":"demo","anchor_path":"../outside"}`。真实返回节选：

```text
isError=false
"instructions":[{"active":true,"applies_to":"../outside/**","bytes":21,"id":"dir:../outside","name":"AGENTS.md" ...}]
outside instruction exposed as active: dir:../outside
```

- 请求二：`{"workspace":"demo","paths":["linked"]}`。真实返回节选：

```text
isError=false
"resolution":{"by_path":{"linked":[{"active":true,"applies_to":"linked/**","bytes":21,"id":"dir:linked" ...}]},"conflicts":[]}
outside instruction exposed as active: dir:linked
```

- 两者还返回同一测试文件 SHA256 `sha256:f793ab9eaf38caae3424785601951d3618b77ebc71867de183fce90194f0b0d8`。实际证明了工作区外文件被读取用于摘要、元数据越界返回及规则适用范围错误；没有证明正文泄露或自动执行外部规则。
- 根因：`internal/instruction/instruction.go:72-95` 拼接目录时未拒绝 `..`、未检查物理路径边界；`:220-225` 的 Lstat 仅排除末级文件符号链接，父目录符号链接仍会被跟随；`:108` 使 paths 复用同一逻辑。
- 工作流影响：调用方得到成功与 active=true，可能把工作区外规则当成适用规则继续读取/执行后续计划；当前返回无错误恢复提示。
- 最小修复建议：统一校验 anchor_path 与每个 paths 元素的工作区相对边界；通过现有安全文件访问方式约束父目录符号链接的物理目标，并在读取/hash 前拒绝越界。返回明确路径校验错误，提示改用工作区内相对路径；保留显式配置的 global 指令来源。

## 测试命令与结果

以下命令均使用 workdir `/Users/rick/.codex/worktrees/mcpx-tool-reliability/mcpx`：

```sh
go test ./internal/server -run '^(TestRuntimeRead|TestRuntimeCapabilities)' -count=1 -v
# PASS，ok mcpx/internal/server 0.483s
go test ./internal/server -run '^TestParityruntime_read' -count=1 -v
# TestParityruntime_readNormal PASS；RejectOutsideInstructions 两个子例 FAIL；总耗时 0.253s
go test ./internal/server -run '^TestParityruntime_readRejectOutsideInstructions$' -count=1 -v
# 最小独立复现命令；该测试已由上一条实际执行，预期当前实现失败
```

- 边界测试保留“不能成功暴露外部指令”的正确断言，修复后应转绿；本 agent 未改实现源文件。
- 未覆盖：真实 HTTP transport/宿主 schema 端到端验证由主线负责；本轮仅隔离已注册 handler。
- 已从 ALL_TOOLS 发现 MacMCPX runtime_read connector，定义含六个业务字段和必填 link_id；没有发现阻止本轮 fixture 的字段缺失。未调用常驻 connector，未核实两个账号链接的运行时状态。
- 未覆盖真实鉴权、外部 MCP/Skill 依赖、Windows 路径、超大指令及并发文件替换；这些不视为通过，也不据静态猜测新增缺陷。
