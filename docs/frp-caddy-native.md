# FRP + Caddy 原生部署 MCPX

本文介绍如何把运行在开发机上的 MCPX 安全地接入公网，并且 **不使用 Docker**：

- 公网 VPS：原生运行 **Caddy + frps**。
- 开发机：原生运行 **MCPX + frpc**。
- Caddy 是唯一公网 HTTP/HTTPS 入口。
- frps 的 MCPX 代理端口只监听 VPS 的 `127.0.0.1`，不会直接暴露公网。

最终公网 MCP 地址示例：

```text
https://mcp.example.com/mcp
```

如果你希望用 Docker Compose 管理 Caddy/frps/frpc，请改看 [FRP + Caddy + Docker Compose 部署 MCPX](frp-caddy-docker-compose.md)。

## 1. 架构

```text
ChatGPT / Remote MCP Client
            │
            │ HTTPS :443
            ▼
┌────────────────────────────────────┐
│ 公网 VPS                           │
│                                    │
│ Caddy :80/:443                    │
│      │                             │
│      ▼                             │
│ 127.0.0.1:19090                   │
│      │                             │
│    frps :7000                     │
└──────┼─────────────────────────────┘
       │ FRP tunnel
       │ TLS + token authentication
       ▼
┌────────────────────────────────────┐
│ 开发机                             │
│                                    │
│ frpc                               │
│   │                                │
│   ▼                                │
│ 127.0.0.1:9090                    │
│ MCPX                               │
│   ├── Workspace                   │
│   ├── Git / Go / Node / Docker   │
│   ├── Local Skills                │
│   └── Upstream MCP                │
└────────────────────────────────────┘
```

公网只需要开放：

```text
80/tcp    Caddy HTTP / ACME
443/tcp   Caddy HTTPS
443/udp   可选，HTTP/3
7000/tcp  frpc → frps
```

**不要开放 `19090`。**

## 2. 前置条件

假设：

```text
VPS 公网 IP：203.0.113.10
域名：mcp.example.com
FRP 控制端口：7000
FRP MCPX 代理端口：19090
MCPX 本机端口：9090
```

DNS：

```text
mcp.example.com  A  203.0.113.10
```

需要准备：

- 一台具有公网 IPv4/IPv6 的 VPS。
- 域名已经解析到该 VPS。
- VPS 上可安装 Caddy、frps。
- 开发机上已安装 MCPX、frpc。
- 开发机可以主动访问 VPS 的 `7000/tcp`。

本文示例配置使用现代 TOML 格式。frps 与 frpc 应保持相同或兼容版本，升级时先阅读对应版本的 release notes。

## 3. 开发机配置 MCPX

MCPX 原生运行在开发机，并继续只监听 localhost。

全局配置默认位于：

```text
~/.mcpx/config.yaml
```

公网 Remote MCP 推荐使用 OAuth：

```yaml
server:
  host: 127.0.0.1
  port: 9090

  # 公网请求经过 Caddy + frp 到达本机。
  disable_localhost_protection: true
  trust_proxy_headers: true

auth:
  mode: oauth
  oauth:
    password: "请替换成强随机口令"
    server_url: "https://mcp.example.com"
    token_ttl: 86400

security:
  commands:
    default: confirm

  files:
    max_read_bytes: 1048576
    max_patch_files: 20
    max_patch_lines: 2000

limits:
  max_result_bytes: 262144
```

生成 OAuth 口令：

```bash
openssl rand -hex 32
```

这里：

```yaml
oauth:
  server_url: "https://mcp.example.com"
```

填写的是公网 Origin，**不要追加 `/mcp`**。

客户端真正使用的 Endpoint 是：

```text
https://mcp.example.com/mcp
```

如果 Workspace 尚未注册：

```bash
./bin/mcpx workspace register /path/to/your/project
```

启动 MCPX：

```bash
./bin/mcpx
```

确认 MCPX 仍只监听：

```text
127.0.0.1:9090
```

先做本机 OAuth metadata 测试：

```bash
curl -i http://127.0.0.1:9090/.well-known/oauth-protected-resource
curl -i http://127.0.0.1:9090/.well-known/oauth-authorization-server
```

MCPX 只提供 Streamable HTTP `/mcp`，不要配置旧版 `/sse`。

## 4. VPS 安装 frps

从 frp 官方 Releases 下载与你系统架构匹配的压缩包，解压后把 `frps` 安装到系统 PATH，例如：

```bash
sudo install -m 0755 frps /usr/local/bin/frps
```

确认：

```bash
frps --version
```

创建配置目录：

```bash
sudo mkdir -p /etc/frp
```

生成认证 Token：

```bash
openssl rand -hex 32 | sudo tee /etc/frp/token >/dev/null
sudo chmod 600 /etc/frp/token
```

把同一个 Token 安全复制一份到开发机，供 frpc 使用。

## 5. VPS 配置 frps

创建：

```text
/etc/frp/frps.toml
```

内容：

```toml
bindAddr = "0.0.0.0"
bindPort = 7000

# 关键安全设置：TCP proxy 只绑定 VPS localhost。
# Caddy 可访问 127.0.0.1:19090，公网不能直接访问该端口。
proxyBindAddr = "127.0.0.1"

auth.method = "token"
auth.tokenSource.type = "file"
auth.tokenSource.file.path = "/etc/frp/token"

# frpc 只能申请本文需要的代理端口。
allowPorts = [
  { single = 19090 }
]

# 只接受启用 TLS 的 frpc 控制连接。
transport.tls.force = true

log.to = "/var/log/frps.log"
log.level = "info"
log.maxDays = 7
```

最关键的是：

```toml
proxyBindAddr = "127.0.0.1"
```

最终 VPS 应形成：

```text
0.0.0.0:7000       frps 控制连接
127.0.0.1:19090    MCPX TCP proxy
```

而不是：

```text
0.0.0.0:19090
```

校验配置：

```bash
frps verify -c /etc/frp/frps.toml
```

临时启动验证：

```bash
sudo frps -c /etc/frp/frps.toml
```

在 frpc 尚未连接时，`19090` 可能还没有处于监听状态；这是正常的。

## 6. VPS 使用 systemd 管理 frps

创建：

```text
/etc/systemd/system/frps.service
```

```ini
[Unit]
Description=FRP Server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/frps -c /etc/frp/frps.toml
Restart=always
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

启用并启动：

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now frps
```

检查：

```bash
systemctl status frps
journalctl -u frps -f
```

## 7. 开发机安装 frpc

从与 frps 相同版本的 frp Release 中取得 `frpc`。

例如安装到：

```bash
sudo install -m 0755 frpc /usr/local/bin/frpc
```

确认：

```bash
frpc --version
```

创建配置目录：

```bash
mkdir -p ~/.config/frp
chmod 700 ~/.config/frp
```

把 VPS `/etc/frp/token` 的内容安全复制到：

```text
~/.config/frp/token
```

然后：

```bash
chmod 600 ~/.config/frp/token
```

## 8. 开发机配置 frpc

创建：

```text
~/.config/frp/frpc.toml
```

```toml
serverAddr = "203.0.113.10"
serverPort = 7000

auth.method = "token"
auth.tokenSource.type = "file"
auth.tokenSource.file.path = "/Users/yourname/.config/frp/token"

transport.tls.enable = true

[[proxies]]
name = "mcpx"
type = "tcp"
localIP = "127.0.0.1"
localPort = 9090
remotePort = 19090
```

Linux 用户把 token 路径换成自己的 Home，例如：

```toml
auth.tokenSource.file.path = "/home/yourname/.config/frp/token"
```

因为 frpc 是宿主机原生进程，所以：

```toml
localIP = "127.0.0.1"
```

可以直接访问同一宿主机上的 MCPX，不需要 `host.docker.internal` 或 host network。

校验：

```bash
frpc verify -c ~/.config/frp/frpc.toml
```

临时运行：

```bash
frpc -c ~/.config/frp/frpc.toml
```

此时 VPS 上应该出现：

```text
127.0.0.1:19090
```

可以检查：

```bash
ss -lntp | grep 19090
```

## 9. Linux 开发机使用 systemd 管理 frpc

如果开发机是 Linux，可创建用户级 systemd 服务。

创建：

```text
~/.config/systemd/user/frpc.service
```

```ini
[Unit]
Description=FRP Client for MCPX
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/frpc -c %h/.config/frp/frpc.toml
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
```

启动：

```bash
systemctl --user daemon-reload
systemctl --user enable --now frpc
```

查看日志：

```bash
systemctl --user status frpc
journalctl --user -u frpc -f
```

如果希望用户退出登录后仍运行，可以按系统策略启用 linger：

```bash
loginctl enable-linger "$USER"
```

## 10. macOS 使用 LaunchAgent 管理 frpc

macOS 可以用 LaunchAgent 管理 frpc。

先确认：

```text
/usr/local/bin/frpc
~/.config/frp/frpc.toml
```

存在。

创建：

```text
~/Library/LaunchAgents/com.mcpx.frpc.plist
```

示例：

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.mcpx.frpc</string>

    <key>ProgramArguments</key>
    <array>
        <string>/usr/local/bin/frpc</string>
        <string>-c</string>
        <string>/Users/yourname/.config/frp/frpc.toml</string>
    </array>

    <key>RunAtLoad</key>
    <true/>

    <key>KeepAlive</key>
    <true/>

    <key>StandardOutPath</key>
    <string>/Users/yourname/Library/Logs/frpc.log</string>

    <key>StandardErrorPath</key>
    <string>/Users/yourname/Library/Logs/frpc.err.log</string>
</dict>
</plist>
```

把示例中的 `/Users/yourname` 换成实际 Home 路径。

加载：

```bash
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.mcpx.frpc.plist
```

后续重启：

```bash
launchctl kickstart -k gui/$(id -u)/com.mcpx.frpc
```

卸载：

```bash
launchctl bootout gui/$(id -u) ~/Library/LaunchAgents/com.mcpx.frpc.plist
```

## 11. 在 VPS 验证 FRP 链路

frpc 成功连接后，在 VPS 执行：

```bash
curl -i http://127.0.0.1:19090/.well-known/oauth-protected-resource
```

以及：

```bash
curl -i http://127.0.0.1:19090/.well-known/oauth-authorization-server
```

如果这里成功，说明链路：

```text
VPS localhost:19090
       ↓
      frps
       ↓
      frpc
       ↓
开发机 127.0.0.1:9090
       ↓
      MCPX
```

已经正常。

## 12. VPS 安装 Caddy

在常见 Linux 发行版上，建议使用 Caddy 官方软件源安装，让系统包管理器同时安装 systemd service。

安装完成后确认：

```bash
caddy version
systemctl status caddy
```

Caddy 默认配置通常位于：

```text
/etc/caddy/Caddyfile
```

## 13. 配置 Caddy

编辑：

```text
/etc/caddy/Caddyfile
```

最小配置：

```caddyfile
mcp.example.com {
    reverse_proxy 127.0.0.1:19090
}
```

不要只代理 `/mcp/*`，因为 MCPX OAuth 还需要：

```text
/.well-known/oauth-protected-resource
/.well-known/oauth-authorization-server
/mcp
/mcp/oauth/register
/mcp/oauth/authorize
/mcp/oauth/token
```

因此整个域名都代理给 MCPX。

Caddy 会作为公网 HTTPS 入口，并把 `X-Forwarded-Proto`、`X-Forwarded-Host` 等代理信息传给后端。MCPX 中：

```yaml
server:
  trust_proxy_headers: true
```

用于按公网请求构造正确 URL。

验证 Caddy 配置：

```bash
sudo caddy validate --config /etc/caddy/Caddyfile
```

重载：

```bash
sudo systemctl reload caddy
```

检查日志：

```bash
journalctl -u caddy -f
```

## 14. 公网验证

测试 OAuth protected resource metadata：

```bash
curl -i https://mcp.example.com/.well-known/oauth-protected-resource
```

测试 Authorization Server metadata：

```bash
curl -i https://mcp.example.com/.well-known/oauth-authorization-server
```

最终 Remote MCP Endpoint：

```text
https://mcp.example.com/mcp
```

不要用裸 `GET /mcp` 是否返回 `200` 作为完整 MCP 健康检查。最终验证应由支持 Streamable HTTP 的 MCP Client 发起 `initialize`。

## 15. OpenAI / ChatGPT 配置

OpenAI 产品界面会持续更新。

当前 ChatGPT 自定义 MCP App 的典型流程是：启用 Developer Mode，进入 Apps 创建自定义 App，填写远程 MCP Endpoint 和认证方式，完成 OAuth，然后 Scan Tools / 创建 App。

![img.png](img.png)
![img_1.png](img_1.png)
![img_2.png](img_2.png)

本文 Endpoint：

```text
https://mcp.example.com/mcp
```

认证方式：

```text
OAuth
```

## 16. Bearer 模式

如果客户端支持显式 Bearer Header，也可以使用：

```yaml
server:
  host: 127.0.0.1
  port: 9090
  disable_localhost_protection: true
  trust_proxy_headers: true

auth:
  mode: bearer
  token: "请替换成强随机 token"
```

生成 Token：

```bash
openssl rand -hex 32
```

公网 Endpoint 不变：

```text
https://mcp.example.com/mcp
```

frps、frpc 和 Caddy 均不需要修改。

## 17. 常见问题

### Caddy 返回 502

按层排查。

VPS：

```bash
systemctl status caddy
systemctl status frps
journalctl -u caddy -n 100
journalctl -u frps -n 100
```

开发机：

```bash
curl -i http://127.0.0.1:9090/.well-known/oauth-protected-resource
frpc -c ~/.config/frp/frpc.toml
```

### frpc 已登录，但 Caddy 访问不到 MCPX

先在 VPS 测试：

```bash
curl -i http://127.0.0.1:19090/.well-known/oauth-protected-resource
```

如果失败，再检查 frps/frpc 日志和 MCPX 本地 `9090`。

### VPS 的 `19090` 被公网访问到了

检查：

```toml
proxyBindAddr = "127.0.0.1"
```

再执行：

```bash
ss -lntp | grep 19090
```

期望：

```text
127.0.0.1:19090
```

不是：

```text
0.0.0.0:19090
```

同时不要在云安全组/防火墙开放 `19090/tcp`。

### OAuth URL 变成 HTTP 或内部地址

检查：

```yaml
server:
  trust_proxy_headers: true

auth:
  oauth:
    server_url: "https://mcp.example.com"
```

### `/sse` 返回 404

这是预期行为。MCPX 只提供 Streamable HTTP：

```text
/mcp
```

## 18. 安全检查清单

上线前确认：

- MCPX 只监听开发机 `127.0.0.1:9090`。
- MCPX 公网部署没有使用 `auth.mode: open`。
- OAuth password / Bearer token 使用随机值。
- MCPX 命令策略保持 `confirm` 或更严格默认值。
- frps/frpc 使用相同认证 Token。
- Token 文件权限为 `0600`，不要提交 Git。
- frps 设置 `allowPorts`，只允许 `19090`。
- frps 强制 TLS，frpc 启用 TLS。
- frps 的 `proxyBindAddr` 为 `127.0.0.1`。
- VPS 防火墙不开放 `19090`。
- Caddy 是唯一公网 HTTP/HTTPS 入口。
- OpenAI 配置截图发布前检查是否包含密码、Token、Cookie、组织名或敏感 Workspace 信息。

## 19. 启停命令速查

VPS：

```bash
sudo systemctl restart frps
sudo systemctl status frps
sudo journalctl -u frps -f

sudo systemctl reload caddy
sudo systemctl status caddy
sudo journalctl -u caddy -f
```

Linux 开发机：

```bash
systemctl --user restart frpc
systemctl --user status frpc
journalctl --user -u frpc -f
```

macOS 开发机：

```bash
launchctl kickstart -k gui/$(id -u)/com.mcpx.frpc
tail -f ~/Library/Logs/frpc.log
```

MCPX 继续按项目原生方式启动、升级和管理，与 Caddy/frp 生命周期解耦。

## 20. 部署重启语义

launchd / systemd 在每次部署重启时向 MCPX 发送 SIGTERM，进程按以下顺序收尾：

1. **先关闭 console SSE broker**：所有终端观测页面的事件流立即返回，不再空耗排水窗口。
2. **排水在途工具调用（默认 50s，`toolResponseTimeout + 5s`）**：`http.Server.Shutdown` 等待
   已接受的调用把同步响应（45s 预算）发回客户端，再给 detached worker 一段落库窗口。
3. **释放持久资源**：等待在途重放结果写入 SQLite（短有界等待），然后关闭任务管理器与状态库。
   超过宽限期的 worker 由进程退出兜底（worker 自身另有 90s 硬上限）。

对宿主管护的建议：

- **launchd 建议配置 `ExitTimeOut ≥ 60s`**（默认 20s 会提前 SIGKILL，令排水失效），例如：

  ```xml
  <key>ExitTimeOut</key>
  <integer>60</integer>
  ```

- systemd 用户服务无需额外配置，默认 `TimeoutStopSec` 即可容纳 50s 排水；
  如全局改小过，请为本服务设 `TimeoutStopSec=60`。

**重放缓存跨重启**：已完成的工具调用结果（10 分钟 TTL、512 条上限）持久化在状态库
`tool_replays` 表中。SIGTERM 时没来得及送出的结果（>45s 的调用）在重启后仍可回放：
客户端用相同参数重试会命中回放并标记"服务重启前已完成"，不会重复执行副作用；
需要真实重跑时把参数 `rerun` 设为 `true`。
