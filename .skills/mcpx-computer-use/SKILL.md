---
name: mcpx-computer-use
description: 通过 MCPX 的 open-computer-use 上游服务查看和操作用户指定的本机桌面应用。适用于原生应用验收、截图、点击、滚动、键盘与表单操作；不代替源码文件和 shell 工具。
---

# MCPX Computer Use

通过 `mcp_tool` 使用全局 `open-computer-use` server；server 来自 `~/.mcpx/.mcp.json`，因此任意 MCPX Workspace 都可发现。不要另起一次性 CLI 执行每个点击，确保元素状态属于同一个服务进程。

1. 先 list(server) 确认接入，再 describe 实际工具 schema；不要猜参数或元素编号。
2. macOS 需要辅助功能与屏幕录制权限。权限缺失时让用户通过 Open Computer Use doctor 的引导授权；不操纵 TCC 数据库或代点授权。握手成功不等于权限获批。
3. 授权后用 list_apps 找到用户明确指定应用，然后 get_app_state 取得最新可访问性树与截图。每轮交互、页面跳转、弹窗或失败后重新读取状态。
4. 优先当前 element_index 的语义动作/set_value，再考虑坐标。动作返回成功后还要核对界面实际变化；需要视觉验收时保留真实截图证据。
5. 默认不启用全局鼠标回退。移动窗口、文字拖选、Finder 拖放等操作可能不被后台 drag 支持，不能把无报错当作动作生效。
6. 仅查看用户指定应用。发送消息、删除、上传、购买、审批等动作需要确认，不读取无关私人内容。不能通过 GUI 绕过 MCPX 文件策略、路径边界或 move_out 确认。

源码与文件日志读写仍用 read/edit，命令仍用 execute，执行结果通过原 Task/Operation 和 observe 核实。成功启动进程、编译成功与视觉交互验收分别报告。全局安装目录为 `~/.mcpx/tools/open-computer-use`；权限状态需要以该全局副本的 `doctor` 结果为准。
