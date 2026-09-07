# MCPX React Operator Console

## 入口与实现范围

React 工作台挂载于现有 Go HTTP 服务的 `/mcp/app/`，管理 API 位于 `/mcp/app/api/`。它读取同一个 Runtime 的真实 Workspace、Remote Session、执行任务和观测事件，不创建模拟任务，不额外启动模型服务。

这是跨平台 Web 工作台，不是新的原生 macOS 安装包。Windows 原有托盘前端仍位于 `internal/desktop/frontend`，本工作台不替换该前端。

源码入口：`internal/webui/frontend/src/App.tsx`。生产静态资源由 `internal/webui/assets.go` 嵌入 Go 二进制，部署后的机器不需要 Node。

前端依赖与锁文件现已就绪，TypeScript 检查及 Vite 正式构建已通过。侧栏支持持久化置顶、同组拖动排序和工作台软删除；运行会话优先显示。具体行为、项目绑定修复和验收方法见 [工作台组织与项目归属](workbench-organization.md)。

## 构建

需要项目 Go 工具链，以及 Node 22.12 或更高版本。以下命令在仓库根目录执行：

```sh
# 依赖安装遵循当前 Runtime 的访问策略；使用锁文件安装。
npm ci --prefix internal/webui/frontend
npm --prefix internal/webui/frontend run build

# 打包的是刚刚构建的 dist，不是 Vite 开发服务器。
go build -o ./mcpx-console ./cmd/mcpx-server
```

首次安装成功后，应保留并审查 `internal/webui/frontend/package-lock.json`，后续可重复构建使用 `npm ci --prefix internal/webui/frontend`。不要把 node_modules 放进 Git。

开发模式：启动新版本 Runtime 后，执行 `npm --prefix internal/webui/frontend run dev`，使用 Vite 输出的本机地址下的 `/mcp/app/`。Vite 将管理 API 代理到 `127.0.0.1:9090`；Runtime 不在此端口时调整 `vite.config.ts` 中的开发代理。

**不要在仍承载当前 MCP 会话的生产实例上随意试运行新二进制。** 项目 CLI 启动时会处理已有守护进程；仅换 `-addr` 不等于完全隔离。独立验收实例应同时使用独立的 `MCPX_HOME` 和监听端口，生产切换前应处理正在执行的任务。

正式访问路径为运行该新二进制的服务根地址加 `/mcp/app/`。反向代理必须转发这一子路径，并关闭 SSE 响应缓冲。

## 工作台

左侧按 Workspace 分组显示真实会话。执行中的会话显示运行任务数量，支持搜索、折叠、分页加载和项目切换。

主区域包含三个视图：

- **工作流**：按 Codex App 风格完整还原每一步。同一次工具调用的 started/completed/command.output/file.changed 事件合并为一张卡片：中文标题（如"执行命令/编辑文件"）+ 状态徽标 + 耗时；execute 卡片展示 `$ 命令`、工作目录与 exit code 标签及深色终端风格 stdout/stderr 输出块；edit 卡片展示路径标签与着色统一 diff（红删绿增 + 变更统计）；`mcp_tool` 卡片展示 `server · tool` 标题、参数表与结果文本；Agent 语义活动（目标/进展/下一步）以引用样式呈现；Runtime 通知压缩为轻量条目。原始 JSON 折叠在"原始事件数据"内，默认不展开。等宽内容统一使用 ui-monospace 字体栈，避免回退到衬线体。
- **终端**：Workspace 总览下自动聚合最近会话的全部执行任务（带会话名标签），选中会话后显示该会话的任务；按任务选择 stdout、stderr 或合并日志，增量读取、搜索、复制、跟随最新输出。运行中的任务可直接"停止此执行"（幂等，重复点击不会误停后续新任务）。终端为只读；日志不会被当成 HTML 或命令执行。
- **请求**：工作台输入框发出的就是给 GPT 的最新指令——发送后按请求 ID 排队，在 GPT 下一次工具调用时附加到返回中，并明确要求它按该指示调整行动。每条指令记录三个阶段：`queued`（等待工具调用）→ `delivered`（已附加到工具响应）→ `acknowledged`（GPT 已回执）。审批确认的主路径是 GPT 对话本身；这里的批准/拒绝按钮用于操作员不在对话旁时远程代为决定。

支持深浅主题、窄屏侧栏和减少动态效果偏好。认证凭据不会写入 localStorage；只保存主题选择。

添加 Workspace 通过内置的**文件夹选择器**完成：控制台提供只读的目录浏览 API（`GET /mcp/app/api/fs`，与所有管理接口一样需要操作员登录，仅列出文件夹名，绝不返回文件内容），对话框内从主目录出发按面包屑逐级点选，选中后一键注册。目录会解析符号链接；根目录、相对路径、不存在的目录和名称冲突会拒绝。重复添加已有路径保留原有 Workspace 名称，不复制、上传或移动文件。

## 指令投递、中断与 GPT 上下文

### 三个不同阶段

1. `queued`：请求已进入 SQLite，等待匹配的工具调用。
2. `delivered`：Runtime 已把请求附加到工具返回并记录投递。网络断开仍可能导致客户端未收到，因此不会立即消费请求。
3. `acknowledged`：同一 Remote Session 后续工具调用显式回执了请求 ID。这表示收到/已读声明，**不表示变更已经实施或任务成功**。

投递采用至少一次语义。模型必须按请求 ID 去重，不能因为请求重复出现就重复执行有副作用的操作。

选择具体会话时，新请求只投递给该会话。Workspace 总览发送的是 Workspace 级请求：同一 Workspace 的匹配会话都可能在回执前读到它；需要精确调整一个任务时应先选择对应会话。

### 工具返回边界

`instrumentTool` 在具体工具处理结束后读取队列，因此工具执行期间新收到的请求也可以出现在这次返回中。一般工具把信息放在 `structuredContent.context.operator_control`；透明代理工具保留上游结构，通过 MCP 元数据和附加文本投递，避免改写上游结果契约。

模型在下一次工具调用中，以原 `remote_session_id` 携带：

```json
{
  "remote_session_id": "沿用服务端返回的会话 ID",
  "acknowledge_requests": ["沿用 operator_control.requests 中的请求 ID"]
}
```

其他字段仍按实际工具 schema 填写。回执在操作处理前验证，并从执行参数中移除。未投递、不同 Workspace、不同会话或非法 ID 的回执不能通过；批量回执是原子的。

`operator_control` 是经过管理端认证的 **用户级请求和配置**，不是系统提示，不覆盖模型宿主自身的安全规则。

### 中断

工作台输入框默认只发送指令（kind=steer）。停止执行的能力保留在终端视图：运行中的任务旁提供"停止此执行"，先持久化通知再终止该任务的本地进程（幂等键为任务 ID，重复触发不会误停后续新任务）。停止结果和失败项会单独返回，不能把 HTTP 202 当作所有进程都已经退出。

记录变更与发送停止信号之间不是跨 SQLite/操作系统的原子事务；进程停止失败会报告错误，请核对真实执行状态。

中断不能撤销已经完成的文件写入或外部副作用，也不能终止外部 GPT 的推理。空闲 GPT 不会被此控制台主动唤醒；它需要下一次调用 MCPX 工具才能看到排队通知。若旧调用在请求进入后已经返回，该通知保留到下一次匹配调用。

## 审批模式与完全访问

模式按 Workspace 存储，未设置时为 `approval`。

审批模式保留现有命令策略：允许操作直接执行，需要确认的操作暂停，明确禁止的操作仍拒绝。管理端可对待确认命令批准或拒绝；决定严格绑定原 pending ID 和命令摘要，不授予一个任意命令通行证。现有 GPT 用户确认流程仍可使用。

完全访问模式必须在 UI 中主动勾选授权。它免除该 Workspace 允许范围内的重复 MCPX 确认，同时在工具返回中告知模型当前模式。明确 deny、角色权限、路径边界、文件 revision guard 和宿主平台约束仍保留；已有明确拒绝的原待审批命令不会被模式切换默默重新批准。`move_out` 仍保留 prepare/submit 清单协议。

模式设置不会自动让之前阻塞的 GPT 重试，也不会替 ChatGPT 或其他客户端关闭它们自己的确认机制。设置保存后立即影响后续 MCPX 调用。

## 管理端认证

工作台使用独立的操作员登录，会话 Cookie 为 HttpOnly、SameSite=Strict，HTTPS 下启用 Secure，有效期 12 小时。写操作需要 CSRF token，并检查请求来源；登录还需要同源自定义请求头。普通 OAuth 客户端访问令牌不等于操作员凭据。

Bearer 模式使用 Runtime 管理令牌；OAuth 模式使用已配置的运维口令。Open 模式仅允许来自本机、Host 也为本机的访问，不能远程免密接管。登录尝试和同时活动的会话/事件流均有限额。

工作台是机器所有者的管理入口，**不是多租户用户门户**。管理账号能看见此 Runtime 的全部已注册项目，切勿与不可信用户共享管理口令。

已有 `/mcp/oauth/authorize` 页面也进行了视觉升级，但仍为无需 JavaScript 的服务端授权页；客户端、回调、PKCE 和 scope 字段保持原协议。给客户端授权不会自动授予 Workspace 完全访问。

## 存储与容量

新增表位于现有 Runtime SQLite 数据库，不放进项目目录：`workspace_access`、`operator_requests`、`operator_deliveries`、`operator_approvals`。请求在重启后保留；工作台登录会话则只在进程内存中，重启需要重新登录。

每个请求最多 8,000 UTF-8 字节；每个 Workspace 最多 200 条未回执请求；单次最多投递 16 条；请求面板显示最近 100 条。事件时间线在浏览器保留最多 1,200 条，终端保留最近约 256 Ki 字符，原 Runtime 日志仍按其自身保留策略存储。历史已回执请求当前没有自动清理策略，长期大量使用时需监控数据库大小。

事件流使用持久化 sequence、Last-Event-ID 和断线补发。日志采用独立字节偏移。SSE 连接只发送已经提交的观测事件，不能把心跳当作模型仍在积极工作的证明。

## 验证

无需前端依赖即可运行核心模型测试：

```sh
node --experimental-strip-types --test internal/webui/frontend/src/model.test.mjs
```

后端与集成测试：

```sh
go test ./internal/control -count=1
go test ./internal/server -run 'TestConsole|TestOperatorRequest|TestFullAccess' -count=1
go test ./internal/oauth -count=1
go test ./... -count=1
go vet ./...
```

新增覆盖包括：持久化与幂等、并发入队、回执原子性、跨会话隔离、同源/CSRF、Open 模式访问边界、访问模式隔离、精确命令拒绝、调用中补充请求、真实进程中断、重复中断不误停新任务，以及 SSE 断线游标回放。

发布前仍必须完成前端依赖安装、TypeScript 检查、Vite 正式构建、桌面/窄屏浏览器验收，并在真实新 Runtime 上走通登录、选任务、日志、请求投递、回执和权限切换。不能仅凭 Go 测试通过就标记 UI 已验收。
