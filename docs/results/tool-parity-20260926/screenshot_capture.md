# screenshot_capture 逐工具审查

- 范围：/Users/rick/.codex/worktrees/mcpx-tool-reliability/mcpx，分支 codex/tool-reliability-20260925，HEAD 6f799d2；共享工作树已有其他改动。
- 仅新增本工具测试与报告；未修改实现、调用常驻服务、截图、申请权限、部署或提交。
- 使用 t.TempDir 隔离 Runtime、真实 MCP Streamable HTTP、无原生 capturer 的 Service、合成 2×2 PNG。

## Codex 对照
- 只读源码 /tmp/mcpx-codex-cli-20260925，已核实 HEAD 68e0c9f5d8fd9449e97a81e92e8fcb86795713b2。
- 最近内建工具为 view_image，读取已有图片，不负责截屏，不能宣称有直接 screenshot_capture 对应。
- codex-rs/core/src/tools/handlers/view_image_spec.rs:16 create_view_image_tool：path 必填；detail 在支持时以 high/original 枚举公开（:24），输出 image_url（:53）。
- codex-rs/core/src/tools/handlers/view_image.rs:94 ViewImageHandler::handle_call：:98 检查图像输入能力；:134 校验 detail；:162–187 检查文件及解码；:200–214 返回图像。
- CUA 可核实边界是 MCP 集成：codex-rs/core/src/mcp_tool_call_tests.rs:1281 对 node_repl/cua_repl 验证请求元数据；codex-rs/config/src/computer_use.rs:9 提供应用访问配置。
- 在上述检索范围未找到原生截图 handler；不推测 CUA 内部实现，未运行 CUA。

## MCPX 契约与调用链
- internal/server/tools_catalog.go:543 注册工具，remote_session_id/purpose 必填，数值字段原为 number，mode/compression 为无枚举字符串。
- internal/server/tools_screenshot.go:19 toolScreenshotCapture → changeRequest(:20) → intPayload(:25–30) → Capture(:32) → metadata + MCP ImageContent(:38–42)。
- internal/server/runtime_context.go:274 changeRequest 校验 purpose、会话、owner/editor；internal/screenshot/capture.go:67 Capture 先 normalizeRequest 再调用底层捕获。
- ALL_TOOLS 已发现 MacMCPX 连接器，定义含 purpose、remote_session_id 及截图参数，未遇到缺字段阻碍。未调用连接器；宿主 schema 对比与统一 harness 由主线负责。

## 实测通过
- TestParityscreenshot_captureProtocol：空 purpose、mode=window、region width=0、display=-1、format=gif、quality=101、max_width=16385 均返回 IsError。
- 参数测试注入 &screenshot.Service{}，其底层 capture 为 nil，绕过校验会失败且无法真实截图；全部正常返回，证明未触达原生 capture。
- mock 首次返回 synthetic capture failure，第二次同会话、省略可选参数调用成功；原始输出：recovery images=1 calls=2。
- 返回恰好一个 image/png ImageContent，字节与合成 PNG 完全一致。默认参数可通过 handler；默认压缩完整路径未在此 mock 中执行。
- 现有 TestCaptureCompressionAndResize 用合成 320×180 图像验证 small JPEG 输出 100×56、字节数及摘要存在；连同 TestCaptureValidation、TestFitDimensionsPreservesAspectRatio 均 PASS。

## 确认问题与最小修复
### P2：小数显示器索引被静默截断，错误目标进入捕获
- 最小输入：{"remote_session_id":"<fixture session>","purpose":"validate synthetic screenshot fixture","display":-0.5}。
- 根因：tools_catalog.go:545 的 number 允许小数；tools_remote_session.go:356 intPayload 将 float64 转 int；负小数变为 0，后续 display<0 校验失效。
- 真实输出：mock call=3 display=0 mode=""；调用成功。仅 mock 证据，未访问显示器。
- 按主线要求，末尾断言改为“拒绝且 capturer 调用仍 2”，不再锁定错误行为；修复前红测输出：
  display=-0.5 must be rejected before capture: rejected=false calls=3 want=2 protocol_error=<nil>
- 主线已完成 display/x/y/width/height/quality/max_width/max_height 的 integer schema 修复；测试要求协议校验错误或工具 IsError，且无新增 capture。

### P2：参数错误归为内部错误，恢复提示误导
- 最小输入同上，将 display 替换为 mode="window"。
- 实际结果：code=SCREENSHOT_ERROR，category=internal，origin=mcpx，message="mode must be fullscreen or region"。
- retry_hint="Inspect the server log and retry with a new request id if appropriate."；可能引导排查日志或重复请求，正确动作应为修正参数。
- 根因：tools_screenshot.go:34 统一包装校验与捕获失败为 screenshot_error；建议区分 validation 错误并提示修正参数。此项仅报告，未改实现。

## 最小复现命令与结果
工作目录固定为首行工作树；只执行 focused 测试：
~~~sh
go test ./internal/server -run '^TestParityscreenshot_capture' -count=1 -v
go test ./internal/screenshot -run '^(TestCaptureCompressionAndResize|TestCaptureValidation|TestFitDimensionsPreservesAspectRatio)$' -count=1 -v
~~~
- 本 agent 最后实测为修复前 FAIL（exit 1，1.485s）：前置校验、恢复、图像断言通过，末尾拒绝小数断言红测。
- 主线最新反馈：integer schema 修复后 TestParityscreenshot_capture、Browser 组均绿，focused race 通过；这是主线提供的验收结果，本 agent 未重复运行。
- 第二条 PASS（0.297s）；实际运行使用独立临时 TMPDIR，仅处理合成图像。
- 日志：/tmp/mcpx-parity-screenshot-capture.log、/tmp/mcpx-parity-screenshot-capture-regression.log。
- 新增：internal/server/parity_screenshot_capture_test.go、docs/results/tool-parity-20260926/screenshot_capture.md。

## 未验收
- macOS TCC、真实显示器/坐标、Linux/Windows 后端、外部进程超时与平台恢复均未运行；禁止真实截图是明确边界。
- 8 MiB 上限、100MP 极值/溢出、完整默认编码未动态验收；静态实现见 capture.go:124、:154、:140，不作为通过项。
- 参数错误归类问题未获修复/复测证据，保持待处理；integer schema 已按主线反馈通过验收。无宿主服务部署或生产结论。
