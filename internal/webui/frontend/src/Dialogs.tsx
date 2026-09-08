import { useEffect, useRef, useState } from 'react';
import type { ReactNode } from 'react';
import { FolderOpen, FolderPlus, ShieldCheck, ShieldOff, X } from 'lucide-react';
import { api, message } from './api';
import type { AccessMode, Workspace } from './model';


export function Modal({ title, children, close }: { title: string; children: ReactNode; close: () => void }) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => { const dialog = ref.current; dialog?.showModal(); return () => dialog?.close(); }, []);
  return <dialog ref={ref} className="modal" onCancel={event => { event.preventDefault(); close(); }} aria-label={title}>
    <header><h2>{title}</h2><button className="icon-button" aria-label="关闭弹窗" onClick={close}><X size={19}/></button></header>{children}
  </dialog>;
}
export function AddWorkspace({ close, added }: { close: () => void; added: (workspace: Workspace) => void }) {
  const [path, setPath] = useState('');
  const choosing = useRef<AbortController | undefined>(undefined);
  const mounted = useRef(true);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; choosing.current?.abort(); }; }, []);
  const [browsing, setBrowsing] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const selectFolder = async () => {
    if (choosing.current) return;
    const controller = new AbortController(); choosing.current = controller;
    setBrowsing(true); setError('');
    try {
      const result = await api<{path?:string;cancelled:boolean}>('native/folder', {method:'POST', body:JSON.stringify({confirm:true}), signal:controller.signal});
      if (mounted.current && !result.cancelled && result.path) setPath(result.path);
    } catch (cause) { if (mounted.current && !controller.signal.aborted) setError(message(cause)); }
    finally { choosing.current = undefined; if (mounted.current) setBrowsing(false); }
  };
  const add = async () => {
    if (!path.trim()) return;
    setBusy(true); setError('');
    try { const workspace = await api<Workspace>('workspaces', { method: 'POST', body: JSON.stringify({ path: path.trim() }) }); added(workspace); close(); }
    catch (cause) { setError(message(cause)); } finally { setBusy(false); }
  };
  const name = path.split(/[\\/]/).filter(Boolean).pop() || '';
  return <Modal title="添加项目到 Workspace" close={close}>
    <div className="modal-intro"><FolderPlus size={23}/><p>从运行 MCPX 的机器上选择一个已有项目文件夹。不会复制、移动或上传你的项目文件。</p></div>
    <div className="native-folder-choice">
      <button className="secondary" disabled={busy || browsing} onClick={() => void selectFolder()}><FolderOpen size={18}/>{browsing ? '请在系统窗口中选择…' : path ? '重新选择文件夹' : '选择项目文件夹…'}</button>
      <p className="field-help">打开运行 MCPX 的电脑上的系统文件夹选择器，不上传文件，也不扫描其他目录。</p>
      {path && <code className="remove-path">{path}</code>}
    </div>
    <details className="manual-folder"><summary>无桌面环境？输入服务器上的绝对路径</summary><label>项目目录<input aria-label="项目绝对路径" value={path} disabled={busy || browsing} onChange={event => setPath(event.target.value)} placeholder="/absolute/path/to/project"/></label></details>
    <footer className="modal-actions"><button className="secondary" disabled={busy} onClick={close}>取消</button><button className="primary" disabled={!path.trim() || busy || browsing} onClick={() => void add()}>{busy ? '正在添加…' : name ? `添加项目 ${name}` : '添加项目'}</button></footer>
    <p className="field-help">项目文件夹的名字将成为 Workspace 名称；重复添加同一目录会保留原有 Workspace。</p>
    {error && <p className="form-error" role="alert">{error}</p>}
  </Modal>;
}
export function AccessSettings({ workspace, mode, close, saved }: { workspace: string; mode: AccessMode; close: () => void; saved: () => void }) {
  const [selected, setSelected] = useState(mode); const [consent, setConsent] = useState(false); const [busy, setBusy] = useState(false); const [error, setError] = useState('');
  const save = async () => {
    setBusy(true); setError('');
    try { await api('access', { method: 'PUT', body: JSON.stringify({ workspace, mode: selected, confirm_full_access: selected === 'full_access' && (consent || mode === 'full_access') }) }); saved(); close(); }
    catch (cause) { setError(message(cause)); } finally { setBusy(false); }
  };
  return <Modal title="Workspace 访问权限" close={close}><form onSubmit={event => { event.preventDefault(); void save(); }}>
    <p className="modal-description">仅应用于 <strong>{workspace}</strong>，保存在 Runtime 中，同项目的所有会话复用，不会因切换会话重复请求。其他项目不会受到影响。</p>
    <fieldset className="mode-options"><legend className="sr-only">执行审批方式</legend>
      <label className={selected === 'approval' ? 'selected' : ''}><input type="radio" name="mode" value="approval" checked={selected === 'approval'} onChange={() => setSelected('approval')}/><ShieldCheck size={22}/><span><strong>审批模式 <em>推荐</em></strong><small>需要确认的操作保持暂停。由你查看并批准，也保留现有 GPT 确认流程。</small></span></label>
      <label className={selected === 'full_access' ? 'selected' : ''}><input type="radio" name="mode" value="full_access" checked={selected === 'full_access'} onChange={() => setSelected('full_access')}/><ShieldOff size={22}/><span><strong>完全访问</strong><small>允许范围内的命令静默执行，不再逐次请求 MCPX 审批。适合你信任的项目。</small></span></label>
    </fieldset>
    {selected === 'full_access' && <div className="access-warning"><strong>授权范围不会无限扩大</strong><p>明确禁止规则、角色与路径边界、文件版本校验仍然生效。此设置不能取消 ChatGPT 或其他宿主自己的安全要求。</p>{mode !== 'full_access' && <label><input type="checkbox" checked={consent} onChange={event => setConsent(event.target.checked)}/>我了解允许操作将直接执行，并为此 Workspace 授权。</label>}</div>}
    {error && <p className="form-error" role="alert">{error}</p>}
    <footer className="modal-actions"><button className="secondary" type="button" onClick={close}>取消</button><button className="primary" disabled={busy || (selected === 'full_access' && mode !== 'full_access' && !consent)}>{busy ? '正在保存…' : '保存设置'}</button></footer>
  </form></Modal>;
}
