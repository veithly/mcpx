# environment 逐工具对标审查

结论：本次正常保存／读取／对比与无效会话错误恢复路径通过，未确认新的阻断问题，无需新增实现修复。

## 范围与基线
- 工作树：/Users/rick/.codex/worktrees/mcpx-tool-reliability/mcpx。
- 分支 codex/tool-reliability-20260925，HEAD 6f799d2ddfa7ab9cbfbe0493823af6db11e918df；含其他 agent 的未提交修改。
- Codex 只读源码：/tmp/mcpx-codex-cli-20260925，已核对 commit 68e0c9f5d8fd9449e97a81e92e8fcb86795713b2。
- 隔离 Runtime、临时 SQLite／workspace、MCP Streamable HTTP；PATH 设为 t.TempDir，外部探测程序不可用。
- 不访问常驻服务、不执行真实工具链、不读真实凭证或屏幕内容；不改实现、不部署、不提交。

## Codex 对照
- 未找到与 MCPX environment 环境报告持久化、按 snapshot_id 对比直接对等的公开工具；最接近的是内部 shell snapshot 工作流。
- Codex core/src/shell_snapshot.rs:151 ShellSnapshot::build、:181 build_for_cwd、:248 try_create：采集 shell 执行状态。
- 同文件 :307 校验快照，:317 失败删除临时文件，:321 rename 完成落盘；:241 将构建失败转换为无快照结果。
- core/src/unified_exec/shell_snapshot.rs:33 Session::prewarm_shell_snapshots，:40 检查 exec_command 可用性：快照服务于命令执行启动。
- 上述路径均相对 Codex codex-rs；仅源码对照，未运行 Rust 测试。MCPX 保存的是环境诊断报告，不能据此要求复制 shell 状态恢复功能。

## MCPX 契约与实现证据
- internal/server/tools_catalog.go:362：environment 必填 remote_session_id；可选 sections，枚举来自 environment.ValidSections；没有 action。
- tools_public_adapters.go:62 toolEnvironment → :304 toolEnvironmentSnapshotCreate，强制 save_snapshot=true。
- tools_environment.go:17 toolEnvironmentInspect：解析会话、校验 sections、采集、保存，并更新会话的 EnvironmentSnapshotID。
- internal/environment/store.go:65 Save 写 SQLite；:93 Get 读取 JSON；:116 Compare 产生差异。
- sections 限制返回视图；保存完整报告用于后续比较。本次已检查返回隐藏 toolchains，而数据库保留 toolchains。

## 实际调用与输出
复用 parity_environment_read_test.go 的隔离 HTTP client；本次只新增 parity_environment_test.go。

正常路径（新增测试 :40–65）：
1. fixture 在临时 workspace 创建 Remote Session。
2. MCP environment({remote_session_id: ID, sections: ["runtime"]})。
3. Service.Get 核对快照存在、会话归属、完整报告；remote.Get 核对快照绑定。
4. MCP environment_read({remote_session_id: ID, snapshot_id: 返回ID, sections: ["runtime"]})。

实际输出：
- save after error: status=succeeded snapshot_present=true runtime_present=true filtered_toolchains=true persisted_full_report=true session_binding=true
- read compare: status=succeeded base_matches=true changes=0

错误路径（新增测试 :34–39）：
- MCP environment({remote_session_id: "rs_missing", sections: ["runtime"]})。
- 实际输出：status=failed isError=true code=NOT_FOUND。
- message 起始为“remote session not found：remote_session_id 必须原样复制 session 返回的完整值”，并提供 session(list/open) 恢复指引。
- 随后同一 HTTP client 使用有效会话成功保存；错误没有导致后续请求卡住。

## 最小复现命令与结果
在上述工作树运行：

go test ./internal/server -run '^TestParityenvironmentSnapshotRoundTripAndRecovery$' -count=1 -v

结果：PASS；测试 0.06s，包总计 1.055s。

go test ./internal/environment -run '^TestSnapshotPersistenceDigestAndComparison$' -count=1 -v

结果：PASS；覆盖持久化读取、动态字段不改变静态 digest、架构变化报告 breaking 差异。

## 修复建议与未验收项
- 已确认缺陷：无；严重度／缺陷最小命令／修复方案不适用。保留上述最小回归测试即可。
- environment_read 的 runtime-only 多余探测与缺 compare ID 假成功已归主线，本报告不重复；本次运行含其当前修复，不能声称外层 6f799d2 服务已更新。
- 未验收真实工具链探测耗时、探测失败质量、数据库写故障／取消、跨 principal 权限及并发快照；没有运行全套测试。
- 已发现 MacMCPX environment connector，但按分工未重复宿主 schema 对比，且禁止常驻写入，因此未直接调用连接器。
- 本次通过仅覆盖隔离 HTTP 与当前工作树；不代表外层 connector、其他平台或常驻服务验收。
