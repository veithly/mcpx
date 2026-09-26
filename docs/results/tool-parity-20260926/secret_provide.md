# secret_provide 对标审查

- 范围：仅此工具；工作树 /Users/rick/.codex/worktrees/mcpx-tool-reliability/mcpx，分支 codex/tool-reliability-20260925，HEAD/base 6f799d2ddfa7ab9cbfbe0493823af6db11e918df。
- 检验对象为当前工作树（包含主线并行改动），不声称常驻服务已验收。只添加本工具测试和本报告；无实现修改、部署、重启、commit/push。
- 环境：newWorkspaceRuntime 将 MCPX_HOME、配置、工作区、SQLite 全部放入 t.TempDir；仅虚构 dummy secret。

## Codex 对照（只读本地克隆）

- 克隆 /tmp/mcpx-codex-cli-20260925，已核验 HEAD=68e0c9f5d8fd9449e97a81e92e8fcb86795713b2。
- 在 codex-rs/core/src/tools 搜索 secret_provide 无命中；未找到直接对应的公开工具，以下只是接近的环境/审批机制。
- codex-rs/protocol/src/shell_environment.rs:54 create_env、:61 create_env_from_vars、:90 populate_env：从进程环境与策略生成执行环境。
- codex-rs/protocol/src/config_types.rs:240 ShellEnvironmentPolicy，:251 set 明确注入变量，:261 Default 默认继承全部且 ignore_default_excludes=true；不能声称默认自动过滤所有凭证。
- codex-rs/core/src/exec_policy.rs:281 ExecApprovalRequest、:327 create_exec_approval_requirement_for_command_platform：围绕命令、审批策略及权限进行执行判定，不等价于 secret_id 挂起/补交。

## MCPX 契约与调用

- internal/server/tools_catalog.go:550 注册 secret_provide；必填只有 remote_session_id、purpose。secret_id、values 可选；values 允许空对象及空字符串。
- internal/server/tools_ext.go:165 toolSecretsProvide：无 ID 走 Cache；有 ID 走 Provide；返回 ref 名称和 persisted=false；:193 起含 ResumeExec 分支。
- internal/secrets/store.go:162 Provide 先 TakePending，再校验归属并写缓存；:77 Set 忽略空值；:187 Cache 同样跳过空值。
- ALL_TOOLS 已发现 MacMCPX 的 secret_provide 定义，含 purpose、remote_session_id、secret_id、values；未发现此工具所需业务字段缺失。未调用连接器；宿主 schema 与 HTTP harness 留给主线。
- 实际调用 rt.toolHandlers["secret_provide"]，经过注册 wrapper/schema 校验及真实 handler/store；不是仅调用 Store 的单元推测。

## 已证实问题：P1 空输入消费挂起 ID，补正后无法恢复

- 最小过程：fixture 建立待处理 secret_id；提供该 ID 并省略 values（或 {}、{"password":""}）；再用相同 ID 提供有效 dummy 值。
- 真实输出节选：
  - missing / empty_object：empty isError=false data=map[cached_refs:[] persisted:false secret_id:sec_…]
  - empty_value：empty isError=false data=map[cached_refs:[password] persisted:false secret_id:sec_…]
  - 三种输入补正后均：corrected retry isError=true，code=SECRET_ERROR，message="unknown secret_id"。
  - retry_hint="Request the required permission or provide the required secret."，safe_to_retry_unchanged=false；补正值仍无法复用已消费 ID。
- 影响：未实际提供有效凭证却报告成功，挂起请求被消耗；调用方按提示补交仍失败。空字符串还产生误导的 cached_refs。
- 根因证据：tools_ext.go:188 调用 Provide；store.go:163 先 TakePending、:177 后写值；Set 跳过空值，但 tools_ext.go:204 仍用 sortedKeys(values) 宣称缓存。
- 最小修复建议：schema 将 values 设为必填并要求至少一个属性；handler/store 在消费 ID 前校验非空名称和非空值。失败保留 pending；返回 refs 仅包含实际缓存成功的项。未知/已消费 ID 应提示重新发起所需操作，而非仅补交值。
- 已留下应通过但当前失败的回归测试，未把错误行为固化成通过断言。

## 实测通过与复现命令

工作目录必须为上述工作树：

    go test ./internal/server -run '^TestParitysecret_provide' -count=1 -v

单独复现缺 values 的恢复故障：

    go test ./internal/server -run '^TestParitysecret_provideEmptyValuesPreservesPending$/missing$' -count=1 -v

- NormalNoEcho：通过；无 ID 缓存和有 ID 提交均成功，完整 CallToolResult 序列化不含 dummy 值，其他会话读取不到缓存。
- InvalidRequests：通过；缺 session→REMOTE_SESSION_REQUIRED，缺 purpose→INVALID_ARGUMENTS，错误 ID→SECRET_ERROR/unknown secret_id，均 IsError=true。
- WrongSessionRecoverable：通过；错误会话拒绝，正确会话随后提交成功。
- EmptyValuesPreservesPending：失败，missing / empty_object / empty_value 三个子例均复现上述同一问题；整体 go test 为 FAIL。
- 完整本机输出：/tmp/mcpx-secret-provide-parity-20260926.log。新增测试：internal/server/parity_secret_provide_test.go。

## 未验收

- 常驻 MacMCPX、真实凭证/配置、用户外部应用、私有屏幕均未访问；无 HTTP/宿主端到端验收。
- ResumeExec/SSH/askpass、TTL 到期、进程恢复、并发提交竞态、磁盘/审计日志不泄漏未做动态验证；正常响应不回显不能替代这些验证。
- Codex 仅静态对照，无 Rust 构建或执行；本次只跑上述 focused Go tests，未跑全套。
