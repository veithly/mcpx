import { useState } from 'react';
import { ArrowUp, Check, Clock3, MessageSquare, ShieldCheck, X } from 'lucide-react';
import { api, message } from './api';
import { age, requestStatus } from './model';
import type { Approval, Detail, UserRequest } from './model';

export function Composer({ workspace, session, onSent }: { workspace: string; session: string; onSent: () => void }) {
  const [body, setBody] = useState(''); const [busy, setBusy] = useState(false); const [error, setError] = useState(''); const [notice, setNotice] = useState('');
  const [retry, setRetry] = useState<{ signature: string; key: string }>();
  const send = async () => {
    const content = body.trim();
    if (!content || busy) return;
    const signature = JSON.stringify([workspace, session, content]);
    const key = retry?.signature === signature ? retry.key : crypto.randomUUID();
    setRetry({ signature, key }); setBusy(true); setError(''); setNotice('');
    try {
      await api<{ request: UserRequest }>('requests', { method: 'POST', body: JSON.stringify({ workspace, session_id: session, kind: 'steer', body: content, client_key: key }) });
      setBody(''); setRetry(undefined);
      setNotice(session ? '已排队。GPT 下一次在这个会话调用工具时，这段话会作为你的最新指示交给它。' : '已排队。这个 Workspace 里 GPT 下一次调用工具时都会先读到这段话。');
      onSent();
    } catch (cause) { setError(message(cause)); } finally { setBusy(false); }
  };
  return <div className="composer-area"><div className="composer">
    <textarea aria-label="给 GPT 发送指令" placeholder={session ? '告诉 GPT 下一步怎么做…' : '给这个 Workspace 的 GPT 留一条指令…'} value={body} onChange={event => setBody(event.target.value)} maxLength={8000} onKeyDown={event => { if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') { event.preventDefault(); void send(); } }}/>
    <footer><span className="composer-target"><MessageSquare size={13}/>{session ? '当前会话 · 下次工具调用时送达' : 'Workspace 级 · 下次工具调用时送达'}</span><button className="send-button" disabled={busy || !body.trim()} onClick={() => void send()} aria-label="发送指令给 GPT">{busy ? '…' : <ArrowUp size={19}/>}</button></footer>
  </div>
  {notice && <p className="success-message" role="status">{notice}</p>}{error && <p className="form-error" role="alert">{error}</p>}</div>;
}

function ApprovalCard({ item, workspace, session, refreshed }: { item: Approval; workspace: string; session: string; refreshed: () => void }) {
  const [busy, setBusy] = useState(false); const [error, setError] = useState('');
  const decide = async (decision: string) => {
    setBusy(true); setError('');
    try { await api('approvals', { method: 'POST', body: JSON.stringify({ workspace, session_id: session, id: item.id, decision, client_key: `approval:${item.id}:${decision}` }) }); refreshed(); }
    catch (cause) { setError(message(cause)); } finally { setBusy(false); }
  };
  return <article className="approval-card"><header><ShieldCheck size={17}/><strong>{item.decision ? '审批决定已保存' : '这个操作在等待确认'}</strong></header><p>{item.summary}</p>{item.command && <pre>{item.command}</pre>}<small>范围：{item.scope} · {item.tool}</small>
    {!item.decision && <p className="subtle-note">确认通常发生在 GPT 对话里：它会把命令拿去问你，你在对话中回复后它带着确认重试。下面这两个按钮用于你不在对话旁时，在这里直接代为决定。</p>}
    {!item.decision && item.can_decide && <footer><button className="secondary" disabled={busy} onClick={() => void decide('denied')}><X size={14}/>拒绝</button><button className="primary" disabled={busy} onClick={() => void decide('approved')}><Check size={14}/>批准并通知 GPT</button></footer>}
    {item.decision && <p className="subtle-note">{item.decision === 'approved' ? '已批准。等待 GPT 在收到通知后重试原操作，并非已经执行。' : '已拒绝此操作，决定将随下一次工具响应告知 GPT。'}</p>}
    {!item.can_decide && !item.decision && <p className="subtle-note">此操作仍需通过原 MCP 确认流程处理。</p>}
    {error && <p className="form-error" role="alert">{error}</p>}
  </article>;
}
export function Requests({ workspace, session, detail, refreshed }: { workspace: string; session: string; detail: Detail; refreshed: () => void }) {
  return <section className="requests-panel"><div className="section-heading"><div><span className="eyebrow">KEEP THE DIRECTION YOURS</span><h2>请求与审批</h2></div><span>{detail.requests.length} 条记录</span></div>
    {detail.approvals.map(item => <ApprovalCard key={item.id} item={item} workspace={workspace} session={session} refreshed={refreshed}/>)}
    {!detail.requests.length && !detail.approvals.length && <div className="empty-state"><MessageSquare size={27}/><h2>有新想法？直接告诉它。</h2><p>在下方输入框发给 GPT 的每一条指令都会保存在这里：<br/>排队 → 已附加到工具响应 → GPT 已回执。</p></div>}
    {detail.requests.map(item => <article className={'request-card ' + item.status} key={item.id}><header><span>{item.kind === 'interrupt' ? '中断通知' : item.kind === 'approval' ? '审批通知' : '你的指令'}</span><time>{age(item.created_at)}</time></header><p>{item.body}</p><footer>{item.status === 'acknowledged' ? <Check size={13}/> : <Clock3 size={13}/>}<span>{requestStatus[item.status] || item.status}</span><small>{item.remote_session_id ? '会话请求' : 'Workspace 请求'}</small></footer></article>)}
  </section>;
}
