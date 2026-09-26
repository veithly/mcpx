# Codex 原实现驱动的 MCPX 编程链路重构

> **Superseded（历史记录）**：本报告描述此前委托 Codex CLI/exec-server 的方案，已被当前 MCPX 原生源码移植任务取代。这里的测试、版本和完成结论不能作为原生 `internal/unifiedexec` / `internal/patch` 的验收证据；新结果见 [原生工具链验证记录](2026-09-26-native-toolchain-port.md)，其中未验证项目保持待主线验证。以下历史正文保留原样。

## 结果

按用户新要求替换逐工具修补路线。四个实施 sub-agent 分别完成执行后端、补丁后端、公开目录与契约迁移、持久证据迁移；主线负责网关接入、审批、生命周期、删除旧代码和最终验收。

工作树 /Users/rick/.codex/worktrees/mcpx-tool-reliability/mcpx，分支 codex/codex-toolchain。基于 6f799d2 和上一轮尚未提交的可靠性修复；原始 diff 快照在 bin/codex-refactor/pre-refactor.patch。原 /Users/rick/Documents/Project/mcpx 的用户改动未改。本轮未提交、推送、安装、迁移生产数据库或重启常驻服务。

## 唯一编程核心

| 模型入口 | 实际实现 | 调用与结果 |
|---|---|---|
| exec_command | 持久 Codex exec-server → Codex process/start/read → Codex PTY/process backend | cmd/workdir/shell/login/tty/yield_time_ms/max_output_tokens；output/exit_code或整数session_id |
| write_stdin | 同一 Codex exec-server 的 process/write/read | session_id/chars/yield_time_ms/max_output_tokens；消费新的输出，不重复返回旧块 |
| apply_patch | Codex 原生 --codex-run-as-apply-patch 或 argv0 apply_patch stdin入口 | 原始Codex补丁，支持Add/Update/Delete/Move，保留上游输出与退出码 |
| 文件读取/检索 | exec_command | rg/cat/sed等真实命令，无独立read视图、rev令牌或预取协议 |

对照源码 clone 68e0c9f5d8fd9449e97a81e92e8fcb86795713b2；实际运行的是本机 codex-cli 0.155.1，不能声称其二进制来自该clone。握手原样保留 executorVersion；CI 固定安装 @openai/codex@0.155.1 并跑真实后端和HTTP验收。

没有新Go patch匹配器或Go进程引擎fallback。Codex缺失或协议不支持时明确失败。对短/长patch均解析官方npm launcher到原生Codex，避免launchd最小PATH缺node；exec helper有独立临时CODEX_HOME，不读取用户Codex配置、无需模型或API key。binary选择支持MCPX_CODEX_BINARY和macOS常见安装路径。

## 删除与迁移

- 删除 internal/edit 全包、internal/source 自研检索全包、仅供旧runtime模式使用的 internal/sqlitequery。
- 删除 toolRead/toolEdit/toolExecute/toolCommandExecute 入口与旧公开schema；不留alias或test-only适配器。
- 删除旧edits/replacements/rev模型编辑协议、观察edit_id/diff分页、旧workspace_transition执行协议。
- 新核心不进入ARC、自动replay、outer Operation；非零命令退出保留为命令结果而非MCP程序错误。
- 管理工具仍管理会话、计划、产物、历史、扩展。operation_batch拒绝包装新核心；进程续接唯一走write_stdin。
- plan证据引用工具 _meta[mcpx/request_id]，验证同session的tool_results、正确工具、成功终态和exit_code=0；deliver不再读取clean_edit_records/terminal_tasks证明新编程效果。
- 工作区归因只认成功apply_patch结果中的mcpx/patch_paths。失败patch可能有部分效果，不能将全部路径宣称成功；命令写文件或缺少收据返回unknown。
- 控制台审批仍拦截未授权命令；console stop关闭该session的Codex执行连接；会话有活跃进程时拒绝关闭，恢复返回process_sessions及实际executor版本。

旧terminal包仅用于仍有调用的管理面/扩展内部任务与日志，不再作为公开编程核心；state/migrations.go的旧建表属于既有数据库迁移记录，不存在旧编程入口兼容分支。

## 保持一致的验收方式

1. Codex原实现作为执行依赖，而不是同名字段仿制；没有不同引擎的静默fallback。
2. 固定验证版本和源码参考，升级Codex必须通过相同差分/集成测试。
3. 原始patch两边差分：112组成功组合（raw/heredoc、空白、Add/Update/Delete/Move），比较字节、输出、退出码，另测畸形输入和路径拒绝。
4. 真实Codex进程测试：退出码、合并输出、工作目录、login、并发、多次poll无重复、PTY stdin/Ctrl-C、读取消后续接、关闭后进程清理、30秒上游保留窗口后读取。
5. 真实MCP HTTP：发布目录不含read/edit/execute；读取→patch→再次同命令读到新内容；进程续接；审批；路径/策略拒绝；失败不吞；计划证据与交付。

## 验证记录

- go test ./... -count=1：38个有测试包通过，0失败。
- go test -race ./internal/codexexec ./internal/codexpatch ./internal/plan ./internal/workspacechanges -count=1：通过。
- server新编程/证据/审批/进程停止focused race：通过；最后错误部分输出保留和心跳同步测试再次通过。
- go vet ./...、gofmt、git diff --check：通过。
- 普通build与CGO_ENABLED=0 release build：通过；候选二进制mcpx-local固定身份签名及严格验签通过。
- 最终签名程序通过 scripts/toolchain-probe.py 的7组真实HTTP检查：目录、原补丁和新读取、输出与非零退出、write_stdin续读、PTY、移动删除失败、工作区与命令拒绝。
- 全仓库测试发现旧心跳测试用20ms睡眠产生负载相关flaky，改为等待真实通知的同步测试；重复3次通过后全仓库通过。

证据日志：bin/codex-refactor/acceptance-all.log、race-backends.log、race-integration.log、race-latest.log、vet-final.log、final/probe/probe-result.json。新候选 bin/codex-refactor/final/mcpx-server；当前安装仍为6f799d2。

## 明确差异与边界

- MCP没有Codex freeform工具声明，所以apply_patch用单个input字符串包原始patch；remote_session_id是远程路由上下文，可在transport绑定后省略。
- 这里复用执行/PTY/patch实现，没有嵌入Codex模型推理循环、完整shell snapshot、完整sandbox/approval编排。MCPX操作员审批与Codex模型提权参数不同，不暴露未实现的sandbox_permissions。
- Workspace工作目录与patch路径预检不是OS沙箱；命令仍按主机权限执行，patch存在检查后的symlink/hard-link并发边界。未声称任意不可信命令被文件系统隔离。
- 输出采用接近Codex的head/tail预算，token数量是bytes/4估计。上游已丢弃的输出无法恢复。
- Codex进程句柄不跨MCPX重启复活；持久调用记录可查询，但不能伪造仍在运行。
- 此次是破坏性契约重构。部署后连接器必须重新tools/list，确认exec_command/write_stdin/apply_patch及字段完整；未刷新外部宿主缓存，不声称当前在线连接器已切换。
- macOS真实核心链路已验收；Windows/Linux平台的全部沙箱和交互行为未实机穷尽验证。

## 复用命令

    go test ./internal/codexexec ./internal/codexpatch -count=1
    go test ./internal/server -run '^TestCodexProgramming|^TestCodexEvidence' -count=1
    go test ./... -count=1
    MCPX_CODEX_BINARY=/opt/homebrew/bin/codex python3 scripts/toolchain-probe.py --binary bin/codex-refactor/final/mcpx-server --output-dir bin/codex-refactor/probe
