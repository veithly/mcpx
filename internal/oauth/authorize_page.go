package oauth

// This form stays server-rendered so connector authorization works without JS.
// All substitutions are escaped by authorizeGet; percent signs are fmt escapes.
const authorizeHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1"/>
<meta name="color-scheme" content="light dark"/>
<title>连接到 MCPX · 安全授权</title>
<style>
:root{color-scheme:light;--bg:#fdfdfb;--panel:#fff;--ink:#242722;--muted:#72796a;--line:#e3e7dc;--wash:#f1f3ec;--green:#4e6b3e}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--ink);font:14px/1.7 -apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC",sans-serif}
.layout{min-height:100dvh;display:grid;grid-template-columns:1fr 1fr}.story{background:var(--wash);border-right:1px solid var(--line);padding:44px 8vw 34px 7vw;display:flex;flex-direction:column}.brand{font-weight:650;font-size:24px;letter-spacing:-1px}.mark{display:inline-grid;place-items:center;background:var(--ink);color:var(--bg);border-radius:9px;width:34px;height:34px;font:18px monospace;margin-right:10px;vertical-align:middle}
.editorial{margin:auto 0;padding:50px 0}.eyebrow{font-size:10px;font-weight:600;letter-spacing:.15em;color:var(--muted)}h1{font-size:clamp(38px,4.4vw,64px);line-height:1.23;letter-spacing:-2px;font-weight:550;margin:23px 0}.lead{font-size:15px;color:var(--muted);line-height:1.9}.promise{border-top:1px solid var(--line);margin-top:34px;padding-top:24px;font-size:12px;color:var(--muted)}.story footer{font-size:10px;letter-spacing:.07em;color:var(--muted)}
main{display:flex;align-items:center;justify-content:center;padding:60px 32px}.card{width:min(370px,100%%)}.badge{display:inline-block;font-size:10px;letter-spacing:.08em;background:var(--wash);color:var(--green);padding:6px 10px;border:1px solid var(--line);border-radius:6px}h2{font-size:24px;letter-spacing:-.6px;font-weight:550;margin:20px 0 12px}.intro{color:var(--muted);font-size:13px;margin-bottom:23px;overflow-wrap:anywhere}.intro strong{color:var(--ink);font-weight:600}.meta{background:var(--wash);border:1px solid var(--line);border-radius:9px;padding:13px 15px;margin-bottom:24px}.meta dt{font-size:10px;color:var(--muted)}.meta dd{margin:3px 0 12px;font-size:12px;overflow-wrap:anywhere}.meta dd:last-child{margin-bottom:0}
label{display:block;font-size:12px;font-weight:500;margin-bottom:8px}input[type=password]{width:100%%;padding:12px;border:1px solid var(--line);border-radius:8px;background:var(--panel);color:var(--ink);font:inherit}input:focus{outline:2px solid var(--green);outline-offset:3px}button{margin-top:17px;padding:12px 15px;width:100%%;border:0;border-radius:8px;background:var(--ink);color:var(--bg);font-family:inherit;font-size:13px;font-weight:500;cursor:pointer;text-align:left;display:flex;justify-content:space-between;align-items:center}button:hover{opacity:.88}button:focus-visible{outline:2px solid var(--green);outline-offset:3px}.note{font-size:11px;color:var(--muted);line-height:1.8;margin-top:17px}.notice{font-size:10px;color:var(--muted);text-align:center;margin-top:30px}
@media(prefers-color-scheme:dark){:root{color-scheme:dark;--bg:#1c1e1b;--panel:#232620;--ink:#eceee7;--muted:#a0a69a;--line:#343930;--wash:#242a20;--green:#b0cd93}}
@media(max-width:720px){.layout{grid-template-columns:1fr}.story{padding:25px 28px;border-right:0;border-bottom:1px solid var(--line)}.editorial{padding:24px 0 8px}h1{font-size:38px;margin:15px 0}.lead{font-size:13px}.promise,.story footer{display:none}main{padding:35px 28px 48px}.card{width:100%%;max-width:430px}}
</style>
</head>
<body>
<div class="layout"><section class="story"><div class="brand"><span class="mark" aria-hidden="true">&gt;_</span>MCPX</div><div class="editorial"><span class="eyebrow">CONNECTED. WITH YOUR PERMISSION.</span><h1>连接你的工具，<br/>保留你的掌控。</h1><p class="lead">让 Agent 进入你的工作空间。<br/>每一次连接，都由你明确授权。</p><p class="promise">工作过程可观测。<br/>项目权限可设置。<br/>新的方向，随时可以提出。</p></div><footer>MCP RUNTIME / SECURE CONNECTION</footer></section>
<main><section class="card"><span class="badge">OAUTH · PKCE 保护</span><h2>允许这次连接？</h2><p class="intro">客户端 <strong>%s</strong> 请求访问此 Runtime。确认以下信息后，输入你的运维口令。</p>
<dl class="meta"><dt>授权回调</dt><dd>%s</dd><dt>请求权限</dt><dd>%s</dd></dl>
<form method="POST" action="%s">
<label for="password">运维口令</label>
<input id="password" name="password" type="password" required autocomplete="current-password" placeholder="输入此 Runtime 的运维口令"/>
<input type="hidden" name="client_id" value="%s"/>
<input type="hidden" name="redirect_uri" value="%s"/>
<input type="hidden" name="code_challenge" value="%s"/>
<input type="hidden" name="code_challenge_method" value="%s"/>
<input type="hidden" name="state" value="%s"/>
<input type="hidden" name="resource" value="%s"/>
<input type="hidden" name="scope" value="%s"/>
<button type="submit"><span>授权并连接</span><span aria-hidden="true">→</span></button>
</form><p class="note">仅在你主动发起连接且信任该客户端时授权。此操作不会自动开启 Workspace 完全访问模式。</p><p class="notice">你的口令不会通过回调地址发送给客户端。</p></section></main></div>
</body>
</html>
`
