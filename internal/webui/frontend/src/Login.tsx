import { useState } from 'react';
import { Activity, ArrowRight, Check, Eye, EyeOff, FolderOpen, LockKeyhole, TerminalSquare } from 'lucide-react';
import { api, message } from './api';
import type { Auth } from './model';

export function Brand() { return <span className="brand"><span className="brand-mark" aria-hidden="true"><TerminalSquare size={22}/></span><strong>MCPX</strong></span>; }
export default function Login({ auth, onLogin }: { auth: Auth; onLogin: (auth: Auth) => void }) {
  const [credential, setCredential] = useState('');
  const [visible, setVisible] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const local = auth.auth_mode === 'open' && auth.local;
  const submit = async () => {
    setBusy(true); setError('');
    try { const result = await api<Auth>('session', { method: 'POST', body: JSON.stringify({ credential }) }); setCredential(''); onLogin(result); }
    catch (cause) { setError(message(cause)); }
    finally { setBusy(false); }
  };
  return <div className="login-layout">
    <section className="login-story">
      <Brand/>
      <div className="login-editorial"><span className="eyebrow">YOUR WORK, IN SIGHT.</span><h1>让每一步，<br/>清晰可见。</h1><p>一个安静的工作台。<br/>看见 Agent 的工作，随时调整方向。</p>
        <div className="login-capabilities">
          <div><span><FolderOpen size={17}/></span><section><strong>项目各就各位</strong><small>Workspace 与会话，清楚地组织在一起。</small></section></div>
          <div><span><Activity size={17}/></span><section><strong>工作实时发生</strong><small>从工具调用到终端输出，不错过关键变化。</small></section></div>
          <div><span><Check size={17}/></span><section><strong>方向始终在你手中</strong><small>追加请求、中断执行，或明确授予访问权限。</small></section></div>
        </div>
      </div>
      <footer><span className="mono">MCP RUNTIME / OPERATOR CONSOLE</span><span>Observe. Adjust. Continue.</span></footer>
    </section>
    <main className="login-form-panel"><form className="login-form" onSubmit={event => { event.preventDefault(); void submit(); }}>
      <span className="login-lock"><LockKeyhole size={23}/></span><h2>回到你的工作空间</h2><p>使用此 Runtime 的管理口令登录。<br/>任务记录留在你的机器上。</p>
      {!local && <label className="field">管理口令或 Bearer Token<div className="password-field"><input autoFocus type={visible ? 'text' : 'password'} autoComplete="current-password" name="password" value={credential} onChange={event => setCredential(event.target.value)} required placeholder="输入已配置的管理凭据"/><button type="button" aria-label={visible ? '隐藏口令' : '显示口令'} onClick={() => setVisible(value => !value)}>{visible ? <EyeOff size={17}/> : <Eye size={17}/>}</button></div></label>}
      {local && <div className="info-box"><LockKeyhole size={17}/><span>当前为本机 Open 模式，无需口令。<br/>此入口不允许远程免密访问。</span></div>}
      {error && <p className="form-error" role="alert">{error}</p>}
      <button className="primary login-submit" disabled={busy || (!local && !credential)} type="submit">{busy ? '正在连接…' : local ? '进入本机工作台' : '进入工作台'}<ArrowRight size={17}/></button>
      <p className="login-security"><LockKeyhole size={12}/>HttpOnly 会话保护 · 凭据不写入浏览器存储</p>
    </form><span className="login-bottom">MCPX · A clearer way to work with agents.</span></main>
  </div>;
}
