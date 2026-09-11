# 在 MCPX 中使用 Open Computer Use

## 接入状态与方式

本机已把官方 npm 包 `open-computer-use@0.3.5` 安装到全局共享目录 `~/.mcpx/tools/open-computer-use`，不修改全局 npm 或 Codex 配置。`~/.mcpx/.mcp.json` 注册全局 `open-computer-use` stdio server，所有 MCPX Workspace 默认继承；项目级同名 server 仍可显式覆盖。原生可执行文件使用绝对路径启动，避免后台 Runtime 的 PATH 缺少 Node。

重新安装使用：

```sh
npm install --prefix "$HOME/.mcpx/tools/open-computer-use" --no-audit --no-fund --save-exact open-computer-use@0.3.5
```

在其他机器上，可使用已安装 CLI 的绝对路径，配置格式如下；合并到已有 `mcpServers`，不要覆盖其他服务：

```json
{
  "mcpServers": {
    "open-computer-use": {
      "type": "stdio",
      "command": "/absolute/path/to/open-computer-use",
      "args": ["mcp"]
    }
  }
}
```

当前 server 已位于 `~/.mcpx/.mcp.json`，使用规范位于 `~/.mcpx/skills/mcpx-computer-use/SKILL.md`，因此其他 Workspace 无需复制配置即可通过 `mcp_tool` 使用。`mcpx` 项目的 `.mcp.json` 不再定义同名 server，也会继承全局配置。调用路径为 AI → MCPX `mcp_tool` → 本地 stdio server。

## 验证与权限

macOS 要求 14+。本机全局原生程序版本已核对为 0.3.5，MCPX 已成功握手并发现九个工具；`mcpx` 与 `CoolLanding-Replica` 两个实际 Workspace 都已成功发现该全局 server。当前 `doctor` 报告 `accessibility=granted, screenRecording=granted`。

```sh
"$HOME/.mcpx/tools/open-computer-use/node_modules/open-computer-use/dist/Open Computer Use.app/Contents/MacOS/OpenComputerUse" doctor
```

若未来权限被系统重置，需要在系统设置中重新为 Open Computer Use 授予“辅助功能”和“屏幕与系统音频录制/屏幕录制”。Runtime 的“完全访问”不等于系统 TCC 授权，不能通过脚本改授权库或绕过系统授权。

授权后的复验使用 MCPX：先 `mcp_tool(action=list, server=open-computer-use)`，然后 describe `list_apps` 与 `get_app_state` 的实时 schema，最后调用目标应用状态。只有取得真实应用截图/可访问性树才算桌面读取验收，点击等操作另做目标效果验收。

## 操作边界

保持同一个 MCP server 会话，以便使用当前 `get_app_state` 返回的 `element_index`。每轮交互、窗口变化或操作失败后重新读取状态；不能猜测或沿用过期元素编号。优先可访问性元素与 `set_value`，必要时才用坐标。

服务提供 `list_apps`、`get_app_state`、`click`、`perform_secondary_action`、`scroll`、`drag`、`type_text`、`press_key`、`set_value`。没有默认开启 `OPEN_COMPUTER_USE_ALLOW_GLOBAL_POINTER_FALLBACKS`。需要全局鼠标路径时必须另行明确授权；默认后台拖拽可能无法移动窗口或执行 Finder 拖放，返回没有报错也必须复查实际界面变化。

桌面权限覆盖真实用户会话，不受源码 Workspace 路径沙箱的同等隔离。只查看用户指定应用；不读取无关聊天、密码管理器或其他敏感内容。发送、删除、上传、购买、审批等外部可见或高风险动作先确认。源码/日志继续走 MCPX `read/edit/execute/observe`，不能用 GUI 绕过文件策略或 move_out 确认流程。

## 上游资料

- 仓库与安装：https://github.com/iFurySt/open-codex-computer-use
- 平台与工作流：https://github.com/iFurySt/open-codex-computer-use/blob/main/skills/open-computer-use/SKILL.md
- 工具与鼠标行为：https://github.com/iFurySt/open-codex-computer-use/blob/main/skills/open-computer-use/references/usage.md

上游实现会更新。安装固定版本；升级后重新验证权限、工具 schema、截图返回和实际操作效果。不要把工具握手或 CLI 退出码当作桌面任务已经完成。
