# 宿主工具定义与真实 tools/list 的差异

证据：2026-09-26 使用已安装提交 6f799d2 的二进制，在隔离 Runtime 发起真实 MCP tools/list，并与本任务 ALL_TOOLS 暴露的 MacMCPX TypeScript 入参比较。不是根据旧文档推测。

| 工具 | 宿主缺失字段 | 宿主额外字段 | link_id |
|---|---|---|---|
|artifact|||False|
|browser|整个工具不可用||None|
|environment_read|acknowledge_requests||True|
|execute|argv, expected_workspace, shell, workspace_transition|scope|False|
|mcp_tool||user_confirmed|False|
|move_out|||False|
|observe|edit_id, offset||True|
|operation_batch|operation_id, run_id||True|
|operation_manage|confirmation_token, cursor, limit, operation_id, operation_ids, remote_session_id, step_id, timeout_ms||False|
|plan|acknowledge_requests, action, activity, evidence, execution_mode, idempotency_key, operations, plan_id, plan_task_id, purpose, reason, remote_session_id, summary, tasks||False|
|read|line_byte_offset||True|
|runtime_read|acknowledge_requests||True|
|session|acknowledge_requests, git_identity_path, remote_name, run_id||True|
|skill_tool||user_confirmed|False|

edit.edits[] 另缺少 rev/content_base64/expected_format/newline_policy，暴露旧 base_sha256。这个脚本只检查根级参数，嵌套字段另行核验。

结论：外层宿主工具定义与当前服务不一致。execute 缺 argv/shell 等，operation_manage 缺实际定位操作所需字段，plan 失去 action。依靠 agent 推断不能修复公开契约。需要宿主刷新 schema 后重新验收；本轮服务端修复不宣称已改变外部缓存。原始快照仅保存在 gitignore 的 bin/parity-evidence。
