# 主线整合记录

本轮起点 6f799d2。以下记录主线修复后的结果，各工具独立报告保留原始审查快照/红测，不改写历史证据。本轮尚未安装、提交或推送。

| 工具/共享层 | 确认问题 | 主线修复 | 当前证据 |
|---|---|---|---|
| 共享 replay | 正常完成的命令重复调用被缓存，改代码后重跑仍返回旧失败 | 正常完成允许新执行；仅中断与在途合并；响应超时正确标记；恢复只加载 interrupted 记录；移除不存在的 rerun 恢复指引 | 真实 HTTP 编程任务先失败→编辑→同命令重跑通过；replay/restart/timeout tests 通过 |
| read | 单文件续读丢预算、batch忽略预算、混合batch无续读动作 | 统一窗口预算；保留游标/预算；生成待续项动作 | TestParityread 全通过 |
| edit | 缺载荷或空replacements清空文件；恢复动作缺字段或带note | update必须且只能有一个有效载荷；显式空content允许；移除不可执行的幂等恢复动作，保留纠正说明 | TestParityedit 全通过 |
| execute | stdin背压持任务锁，stop阻塞 | 在锁外写pipe，保留生命周期锁用于状态/停止 | TestParityexecute 全通过 |
| operation_batch | 失败只传一层导致后代永久queued | 按固定点传播到所有后代，与输入拓扑顺序无关 | TestParityoperation_batch、反向三层依赖测试通过 |
| operation_manage | 续页丢limit并忽略cursor；切断UTF8 | 保留limit；有cursor继续page；完整rune边界 | TestParityoperation_manage 全通过 |
| observe | 恢复日志尺寸为0；页尾UTF8损坏 | 恢复stream文件实际尺寸；不输出不完整rune尾部 | TestParityobserve 全通过 |
| workspace | 未确认缺陷 | 无实现改动 | HTTP列表、空结果、参数纠正通过 |

阶段验证：server/terminal完整包测试、operation/terminal完整包测试、上述前两批focused race、go vet 通过。所有fixture使用临时Runtime/工作目录；没有访问真实秘密、抓私人屏幕或操作真实外部应用。

宿主缓存缺陷单列 host-contract.md。本轮服务端测试通过不代表外层连接器定义已刷新。Windows、外部平台依赖和有权限提示的交互在逐工具报告中单独注明。

## 第三、四批整合

- session：恢复和关闭改为查询真实活跃Task，避免最近100条历史过滤掉旧进程。99/100条对照回归已通过。
- runtime_read：锚点、paths和指令目录均校验物理工作区边界，拒绝父级或目录symlink越界。正常项目/目录指令保持通过。
- environment_read：compare必须有snapshot_id，runtime-only仅采集所需分区，按相同分区比较，避免无关探测与假差异。
- environment：独立agent正常保存/读取对比、错误恢复通过，未确认新缺陷。
- artifact：UTF16页尾high surrogate与后续low surrogate作为完整代理对返回，避免emoji变替换字符。原始红测已转绿。
- 共享replay：省略remote_session_id时先解析transport绑定，避免不同工作区的相同命令互取中断结果；隔离对照测试已绿。

提醒：显式idempotency_key绑定已记录的终态（包括失败），同键复读不重新执行；修改依赖后的新执行应使用新键。这与无键正常重复调用是两个契约，不将幂等保护误判为bug。

## 最后一批整合

- plan：保持失败终态幂等重放，补充回放结果应换新键重新执行的明确提示；同键业务结果稳定、新键恢复测试通过。
- progress / mcp_tool / move_out：独立审查与隔离关键流程通过，无确认实现缺陷。主线把progress、skill测试提升为注册handler边界验证。
- skill_tool：describe先读取说明，失败返回SKILL_READ_ERROR，不再把不可读entry登记成成功发现。
- secret_provide：缺少/空values在消费pending前拒绝；补正后可复用原secret_id，底层Provide也校验。只使用dummy数据。
- browser：node_id模式的keys/button/x/y组合不再静默丢弃；明确拒绝并引导支持的坐标模式。macOS实际browser仍不支持，未声称浏览器工作流流畅。
- screenshot_capture：显示器索引、坐标、尺寸、质量均声明integer，阻止分数静默截断；测试只用合成2×2图片和mock捕获器。

本轮共20个专职agent；报告保留各自原始审查快照。统一验收以本文件和最终summary为准。Codex仅源码对比，未运行其Rust测试或开展同模型耗时A/B，不能宣称绝对性能等同。
