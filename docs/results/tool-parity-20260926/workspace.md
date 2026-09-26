# workspace 逐工具对标审查

结论：本次三条关键路径通过，未确认需要修复的 workspace 问题。

## 范围与证据基线

- 工作树：`/Users/rick/.codex/worktrees/mcpx-tool-reliability/mcpx`。
- 分支：`codex/tool-reliability-20260925`；HEAD：`6f799d2ddfa7ab9cbfbe0493823af6db11e918df`。
- 测试实际运行于含其他 agent 并行改动的工作树；共享响应层已有改动，因此不将结果宣称为纯 base 或常驻服务验收。
- 本工具 handler、注册定义、公开 adapter、Registry 与 HEAD 无 diff。
- 仅新增本文及 `internal/server/parity_workspace_test.go`；未修改实现、旧 main、部署或常驻服务。
- 行号均相对于上述工作树，Codex 行号相对于下述只读克隆。

## Codex 对照

对照目录 `/tmp/mcpx-codex-cli-20260925`，已核对 HEAD 为 `68e0c9f5d8fd9449e97a81e92e8fcb86795713b2`。
本次检查的原生工具路径中未发现直接等价的“已注册项目清单”工具；最近的是执行前的环境及 cwd 选择流程：

| 实现 | 具体证据 | 与 workspace 的关系 |
|---|---|---|
| `ExecCommandEnvironmentArgs` | `codex-rs/core/src/tools/handlers/unified_exec.rs:53-60` | 接收 `environment_id`、`workdir`，不维护项目注册清单。 |
| `resolve_tool_environment` | `codex-rs/core/src/tools/handlers/mod.rs:160-177` | 省略 ID 时选 primary；未知 ID 返回 `unknown turn environment id`。 |
| `ExecCommandHandler::handle_call` | `codex-rs/core/src/tools/handlers/unified_exec/exec_command.rs:155,186-205` | 从已选环境解析 cwd；没有环境或路径解析失败时返回模型可见错误。 |

MCPX 提供发现入口，Codex 上述流程消费已有环境；无需为追求工具名一致扩展 workspace。Codex 未编译运行，本项为源码对照。

## MCPX 契约与边界

- `registerCleanCoreTools`：`internal/server/tools_clean_core.go:118-138`，公开输入为 object、空 properties、`additionalProperties:false`，只读注解。
- `withEmbeddedActivitySchema`：`internal/server/tools_catalog.go:259-262`，workspace 特意跳过 activity 参数注入。
- `toolWorkspace`：`internal/server/tools_public_adapters.go:28-29`，转发到 `toolWorkspaceList`。
- `toolWorkspaceList`：`internal/server/runtime.go:1101-1127`，鉴权后列清单，排除 control 中已删除项目；输出 `name/path/description` 和明确项目选择提示。
- `remoteRequest`：`internal/server/tools_remote_session.go:70-84`，鉴权但不强制创建 Remote Session。
- `Registry.List`：`internal/workspace/registry.go:61-71`，持读锁并按注册顺序返回。
- 该工具只列出注册信息，不负责注册或检测路径当前可用性；输出提示通过 `session(workspace_path=<explicit absolute path>)` 处理未注册项目。

## 实际调用与结果

新增单个测试 `TestParityworkspaceHTTPListAndRecovery`，含 empty/registered 两个子用例。
复用 `newWorkspaceRuntime`（`internal/server/workspace_resolve_test.go:18-46`），所有项目、配置、数据库均位于 `t.TempDir()`，`MCPX_HOME` 被隔离。
调用真实 `registerTools` + MCP Streamable HTTP + SDK `CallTool`，只调用 workspace；HTTP 使用随机 loopback 端口，无真实凭证。

最小复现命令（工作目录为上述工作树）：

```sh
go test ./internal/server -run '^(TestWorkspaceListDoesNotRequireRemoteSession|TestParityworkspaceHTTPListAndRecovery)$' -count=1 -v
```

真实结果节选，完整日志：`/tmp/mcpx-workspace-parity-20260926.log`。

```text
attempt=0 status=succeeded workspaces=0 guidance=true text=true
unexpected parameter: isError=true code=invalid_arguments
attempt=2 status=succeeded workspaces=0 guidance=true text=true
attempt=0 status=succeeded workspaces=2 guidance=true text=true
unexpected parameter: isError=true code=invalid_arguments
attempt=2 status=succeeded workspaces=2 guidance=true text=true
--- PASS: TestParityworkspaceHTTPListAndRecovery (0.10s)
--- PASS: TestWorkspaceListDoesNotRequireRemoteSession (0.03s)
PASS
ok  mcpx/internal/server 0.873s
```

1. 正常流程：`workspace({})` 无 session 成功；断言 alpha/beta 顺序、名称、路径、description 与 Registry 一致。既有 bearer handler 测试验证非空 description 及合法 token。
2. 空注册边界：返回可解码的 `workspaces:[]`，不是 null；项目选择提示和文本内容均存在。
3. 错误恢复：`workspace({"unexpected":true})` 返回 MCP `isError=true`，错误代码在 wire 中为 `INVALID_ARGUMENTS`（日志 helper 转小写）；改回 `{}` 后同一连接成功。实际错误消息为 `validating root: unexpected additional properties ["unexpected"]`。

首次测试断言误将现有 `errorCode` helper 的小写输出与大写比较；更正测试后通过，不属于产品缺陷。

## 问题、建议及未覆盖项

- 确认问题：无；严重度及最小修复：不适用，不建议无证据重构。
- 未覆盖：control 数据库故障/删除过滤、注册目录被移动或删除、超大清单、并发注册、HTTP 无效鉴权；不将这些静态可能性定为缺陷。
- 未验收：真实 MacMCPX connector、外层常驻服务、外部平台；本 agent 未调用连接器，不声称存在缺字段或过时问题。
- 宿主 schema 对比、统一端到端 harness、session 注册工作流由主线负责；此处仅做 workspace 的隔离协议验证。
