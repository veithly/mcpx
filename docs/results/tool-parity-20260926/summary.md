# 20 工具逐项对标：最终验收

本轮实际启动并完成 20 个独立 agent，每个提交 Codex 源码对照、MCPX 实际工具调用/隔离 fixture 证据和报告；主线独立复现后修复共享实现并整体验收。Codex 参考提交为 68e0c9f5d8fd9449e97a81e92e8fcb86795713b2，位于 /tmp/mcpx-codex-cli-20260925。

没有开展同模型耗时 A/B，也没有运行 Codex Rust 测试，不宣称绝对性能等同。

## 最终矩阵

| 工具 | 状态 | 已验证结果 |
|---|---|---|
| [workspace](workspace.md) | 通过 | 列表、空清单、参数纠正 |
| [session](session.md) | 已修复 | 历史页超过100条不再漏掉活跃Task；恢复/关闭以真实进程为准 |
| [read](read.md) | 已修复 | 批量与续读保留预算，混合读取有可执行续读动作 |
| [edit](edit.md) | 已修复 | 缺载荷不再清空文件；移除无法执行的幂等恢复动作 |
| [execute](execute.md) | 已修复 | stdin背压不阻塞stop；修改后相同测试命令实际重跑 |
| [observe](observe.md) | 已修复 | 恢复stdout/stderr尺寸；UTF8分段不损坏 |
| [operation_batch](operation_batch.md) | 已修复 | 失败传递所有依赖后代，避免永久queued/running |
| [operation_manage](operation_manage.md) | 已修复 | 保留分页limit/cursor，按UTF8完整字符分段 |
| [runtime_read](runtime_read.md) | 已修复 | 指令路径限定物理workspace，拒绝父级/symlink越界 |
| [environment_read](environment_read.md) | 已修复 | compare要求snapshot_id；定向读取不做无关探测 |
| [environment](environment.md) | 通过 | 快照保存、会话绑定、对比和错误恢复 |
| [artifact](artifact.md) | 已修复 | UTF16分页不拆代理对，emoji保真 |
| [plan](plan.md) | 通过并补提示 | 保持失败终态幂等；新执行换键提示明确 |
| [progress](progress.md) | 通过 | 过程/终态、错误恢复、持久化重新附着 |
| [skill_tool](skill_tool.md) | 已修复 | 说明不可读时describe返回错误而非假成功 |
| [mcp_tool](mcp_tool.md) | 通过 | 发现/调用/参数修正；上游失败同键不重复效果 |
| [move_out](move_out.md) | 通过 | prepare不改文件、submit/幂等重复、stale拒绝 |
| [secret_provide](secret_provide.md) | 已修复 | 空values不消费pending，补正原ID可继续 |
| [browser](browser.md) | 映射修复，平台受限 | 不静默丢修饰键/按钮；macOS官方bridge仍不支持 |
| [screenshot_capture](screenshot_capture.md) | 模拟验收通过 | integer schema防目标截断；校验/捕获错误分离 |

## 跨工具根因

最严重的共享问题：正常完成的无键调用被回放缓存。测试失败→修改文件→相同命令仍拿旧失败，根本没有重跑。现在区分正常新执行、在途合并、响应中断恢复和显式幂等。另修复隐式transport会话未参与回放身份，导致不同工作区相同命令互取结果。

TestParityCodingWorkflowViaMCPHTTP 经真实 MCP HTTP 完成：workspace→session→创建错误代码→Node校验失败→读取rev→编辑→相同脚本重新执行通过→幂等重放→过期修改拒绝→progress completed→close。TestParityInterruptedReplaySeparatesTransportBindings 验证两个绑定工作区隔离。

## 验证记录

- 全仓库 go test ./... -count=1：39 个有测试包通过，0 失败。最终截图错误分类补丁后另跑相关 server/screenshot 测试和 vet 通过。
- 多批 focused race 通过，覆盖逐工具回归、共享 replay、超时恢复、依赖传播；不是全仓库 race 声明。
- go vet ./...、gofmt、git diff --check 通过。
- 签名候选程序真实 HTTP 冒烟验收通过：目录、session、编辑字节、幂等、旧schema拒绝、Node子进程、async wait、完成后取消。
- 46 个新增 TestParity 顶层测试，20 个工具专属测试文件加1个共享工作流文件。

原始日志/schema在工作树 bin/parity-evidence（gitignore）。工具报告保留独立审查时的原始红测；修复后状态以本矩阵和 [整合记录](integration.md) 为准。agent清单在 bin/parity-evidence/agents.json。

## 剩余边界

1. [宿主连接器定义仍过时](host-contract.md)：edit缺rev、execute缺argv/shell、operation_manage缺ID/会话/分页字段、部分工具缺link_id。必须刷新连接器并重验，不能用服务端测试代替外层验收。
2. macOS官方browser bridge当前仅支持Windows。本机只验证不可用返回及动作映射/确认模拟，未操作私人浏览器。
3. 截图只用mock和合成图片，未验证真实TCC、显示器和其他平台后端。
4. 缺依赖、脚本语法和业务测试失败仍是正常结果。不放宽权限、不取消rev、不吞真实错误。
5. 本轮未提交、推送或安装。常驻服务仍是上一轮6f799d2；新修改位于 /Users/rick/.codex/worktrees/mcpx-tool-reliability/mcpx，分支 codex/tool-reliability-20260925。候选文件 bin/parity-final/mcpx-server。原始旧main工作区未改。

## 复用检查

    go test ./internal/server -run '^TestParity' -count=1
    go test ./... -count=1
    go test -race ./internal/server -run '^TestParity|^TestReplay|^TestBoundedToolLate' -count=1
    python3 scripts/tool-reliability-probe.py --binary bin/parity-final/mcpx-server --output-dir bin/parity-evidence/probe
