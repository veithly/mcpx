# 工作台：项目、会话与系统权限

## 项目导航

侧栏默认只展开项目层，不展开任何项目的会话。点击名称打开该项目的优先会话；点击箭头才展开会话。搜索会临时展示匹配结果，不改变原来的折叠状态。

活跃项目位于其他项目之前：实际命令仍在执行，或最近一次命令开始/结束距服务端当前时间不足180秒。长命令不会因为启动超过三分钟而被藏到普通项目。普通工具调用、打开页面、读取日志、HTTP轮询以及会话 last_active_at 更新均不续期这个窗口。每次侧栏刷新会重新计算，默认轮询间隔两秒。

同组保留置顶和手工排序。活跃组之间与普通组之间可分别排序，不允许以拖动绕过活跃置顶规则。会话拖动只调整展示顺序，不能改变其工作区归属。

点击项目的优先会话按实际运行命令、其他正在执行的工作、近期命令、命令时间和会话时间选择；候选来自所有会话，而不是仅从当前加载的侧栏页面选择。进入之后不会因为后台刷新自动跳到其他会话。项目总览通过会话切换条或右键菜单单独打开。

## 多会话并行

项目内会话保持各自的完整 remote_session_id。顶部切换条显示名称、短标识和执行状态，完整标识用于URL和请求。右侧新标签页入口允许同时查看同一项目内的不同会话；新标签页不会替换其他标签页的会话选择。浏览器返回恢复原工作区和会话；标签页标题使用所选工作区名称。

工作台展示已有的真实Agent会话，不会创建一个空记录来冒充新的GPT对话。新的Agent工作需要在客户端为同一workspace打开独立session。工作台的指令输入仍可限定为当前会话，或明确在项目总览中发送项目级指令。

右键与“更多”按钮打开浮动菜单，不向侧栏插入按钮组。支持方向键、Home/End、Escape和焦点恢复，靠近屏幕边缘自动调整位置。软移除保留项目文件和审计数据；正在工作的会话仍受后端保护。

## 原生文件夹选择

“添加项目 → 选择项目文件夹”打开**运行Runtime的电脑**上的系统对话框。macOS使用AppKit NSOpenPanel，Windows使用系统文件夹选择对话框，Linux使用可用的zenity。浏览器不上传项目文件；原页面目录枚举入口已移除。无桌面环境时可以展开手工绝对路径输入，但没有另一个页面文件浏览器。

对话框只能由经认证操作员主动点击打开，写接口继续检查同源和CSRF。重复点击不创建多份对话框。取消不注册项目；关闭页面取消待完成选择；选定目录后仍需点击添加。选择器最多等待两分钟，超时或不可用返回明确错误。原生选目录不等价于授予Runtime所有其他文件或系统能力的权限。

## macOS权限与MCPX审批是两层

MCPX项目执行模式（审批/完全访问）保存在Runtime状态中。同项目的多个会话复用该模式，不需要每个会话重复设置；明确拒绝、安全策略与需要确认的移出操作不被取消。

macOS Files and Folders、Full Disk Access、Accessibility、Screen Recording 则由系统管理，不能用一个网页勾选代替授权，也不存在应用可以自行静默批准所有类别的公共入口。工作台“系统权限”只显示当前可执行程序和签名类型，并按主动点击打开对应系统设置；不通过轮询受保护目录来探测授权、不修改TCC.db、不把本地记忆标记当成真实系统许可。

重复提示需要分别排查签名身份、安装路径和具体权限类别。临时（ad-hoc）签名的程序在重建后可能成为系统看来不同的代码身份；只有固定路径并不能保证旧授权延续。可复用的签名身份应在后续更新中保持同一证书和标识。已有证书时可以签署候选二进制：

```sh
MCPX_SIGN_IDENTITY='现有代码签名证书的名称或指纹' \
  bash scripts/sign-macos-stable.sh bin/mcpx-server-workbench
```

脚本拒绝空身份和`-`临时身份，不创建信任根或重置授权。应签署构建候选文件，再通过正常安装流程替换固定位置的程序。首次从临时签名迁移到证书签名，或系统策略要求时，用户仍可能需要在系统设置中重新授权。广泛文件访问可由用户在Full Disk Access中明确添加当前Runtime；辅助功能、屏幕录制等按实际功能需要分别授予。

参考：Apple 文件访问控制 https://support.apple.com/guide/security/controlling-app-access-to-files-secddd1d86a6/web 。有关命令行工具稳定签名和TCC身份的Apple工程师说明见 https://developer.apple.com/forums/thread/718331 。

## 验证

```sh
npm --prefix internal/webui/frontend run build
npm --prefix internal/webui/frontend test
go test ./internal/server ./internal/nativeui -run 'TestConsole|TestSidebar|TestNative|TestSelectedDirectory' -count=1
go test -race ./internal/server ./internal/nativeui -run 'TestConsole|TestSidebar|TestNative|TestSelectedDirectory' -count=1
```

macOS桌面环境可显式运行 `MCPX_NATIVE_UI_QA=1 go test ./internal/nativeui -run TestNativeMacDialogCancellation -count=1`：打开真实NSOpenPanel并用其内部计时器取消，不选择用户文件，也不自动批准隐私权限。`MCPX_BROWSER_QA=1 go test ./internal/server -run '^TestConsoleBrowserQA$' -v` 启动临时数据库、三个项目及同项目两个真实并行命令，供浏览器验收，最多八分钟并自动清理。

生产工作台静态资源嵌入Go二进制。仅修改源码或编译候选不会更新现有Runtime进程；部署需要单独完成固定位置安装和服务重启，避免打断其他正在执行的会话。
