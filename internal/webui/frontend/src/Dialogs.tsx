import { useEffect, useRef, useState } from 'react';
import type { ReactNode } from 'react';
import { ArrowUp, ChevronRight, Folder, FolderOpen, FolderPlus, Home, ShieldCheck, ShieldOff, X } from 'lucide-react';
import { api, message } from './api';
import type { AccessMode, Workspace } from './model';

interface FsEntry { name: string; path: string }
interface FsPage { path: string; parent: string; home: string; segments: { label: string; path: string }[]; entries: FsEntry[] }

export function Modal({ title, children, close }: { title: string; children: ReactNode; close: () => void }) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => { const dialog = ref.current; dialog?.showModal(); return () => dialog?.close(); }, []);
  return <dialog ref={ref} className="modal" onCancel={close} aria-label={title}>
    <header><h2>{title}</h2><button className="icon-button" aria-label="关闭弹窗" onClick={close}><X size={19}/></button></header>{children}
  </dialog>;
}
export function AddWorkspace({ close, added }: { close: () => void; added: (workspace: Workspace) => void }) {
  const [page, setPage] = useState<FsPage>();
  const [browsing, setBrowsing] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const load = async (target: string) => {
    setBrowsing(true); setError('');
    try { setPage(await api<FsPage>('fs?path=' + encodeURIComponent(target))); }
    catch (cause) { setError(message(cause)); } finally { setBrowsing(false); }
  };
  useEffect(() => { void load(''); }, []);
  const add = async () => {
    if (!page) return;
    setBusy(true); setError('');
    try { const workspace = await api<Workspace>('workspaces', { method: 'POST', body: JSON.stringify({ path: page.path }) }); added(workspace); close(); }
    catch (cause) { setError(message(cause)); } finally { setBusy(false); }
  };
  const name = page ? page.path.split(/[\\/]/).filter(Boolean).pop() : '';
  return <Modal title="添加项目到 Workspace" close={close}>
    <div className="modal-intro"><FolderPlus size={23}/><p>从运行 MCPX 的机器上选择一个已有项目文件夹。不会复制、移动或上传你的项目文件。</p></div>
    <div className="fs-crumb" aria-label="目录路径">
      <button className="icon-button" title="主目录" aria-label="回到主目录" disabled={!page?.home || browsing} onClick={() => void load(page?.home || '')}><Home size={15}/></button>
      <button className="icon-button" title="上一级" aria-label="上一级目录" disabled={!page?.parent || browsing} onClick={() => void load(page?.parent || '')}><ArrowUp size={15}/></button>
      <div className="fs-crumb-path">{page?.segments.map(segment => <button key={segment.path} disabled={browsing} onClick={() => void load(segment.path)}>{segment.label}<ChevronRight size={11} className="fs-crumb-sep" aria-hidden="true"/></button>)}</div>
    </div>
    <div className="fs-browser" role="listbox" aria-label="选择文件夹">
      {browsing && !page && <p className="fs-note">正在读取目录…</p>}
      {page?.entries.map(entry => <button className="fs-row" key={entry.path} disabled={browsing} onClick={() => void load(entry.path)} role="option" aria-label={'打开文件夹 ' + entry.name}><FolderOpen size={15}/><span>{entry.name}</span></button>)}
      {page && !page.entries.length && <p className="fs-note">这个目录下没有子文件夹了，可以直接添加当前目录。</p>}
    </div>
    <div className="fs-selected">
      <Folder size={16}/>
      <div className="fs-selected-path"><small>已选文件夹</small><code>{page?.path || '…'}</code></div>
      <button className="primary" disabled={!page || busy || browsing} onClick={() => void add()}>{busy ? '正在添加…' : name ? `添加为 Workspace（${name}）` : '添加 Workspace'}</button>
    </div>
    <p className="field-help">项目文件夹的名字将成为 Workspace 名称；重复添加同一目录会保留原有 Workspace。</p>
    {error && <p className="form-error" role="alert">{error}</p>}
    <footer className="modal-actions"><button className="secondary" type="button" onClick={close}>取消</button></footer>
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
    <p className="modal-description">仅应用于 <strong>{workspace}</strong>，保存在 Runtime 中。其他项目不会受到影响。</p>
    <fieldset className="mode-options"><legend className="sr-only">执行审批方式</legend>
      <label className={selected === 'approval' ? 'selected' : ''}><input type="radio" name="mode" value="approval" checked={selected === 'approval'} onChange={() => setSelected('approval')}/><ShieldCheck size={22}/><span><strong>审批模式 <em>推荐</em></strong><small>需要确认的操作保持暂停。由你查看并批准，也保留现有 GPT 确认流程。</small></span></label>
      <label className={selected === 'full_access' ? 'selected' : ''}><input type="radio" name="mode" value="full_access" checked={selected === 'full_access'} onChange={() => setSelected('full_access')}/><ShieldOff size={22}/><span><strong>完全访问</strong><small>允许范围内的命令静默执行，不再逐次请求 MCPX 审批。适合你信任的项目。</small></span></label>
    </fieldset>
    {selected === 'full_access' && <div className="access-warning"><strong>授权范围不会无限扩大</strong><p>明确禁止规则、角色与路径边界、文件版本校验仍然生效。此设置不能取消 ChatGPT 或其他宿主自己的安全要求。</p>{mode !== 'full_access' && <label><input type="checkbox" checked={consent} onChange={event => setConsent(event.target.checked)}/>我了解允许操作将直接执行，并为此 Workspace 授权。</label>}</div>}
    {error && <p className="form-error" role="alert">{error}</p>}
    <footer className="modal-actions"><button className="secondary" type="button" onClick={close}>取消</button><button className="primary" disabled={busy || (selected === 'full_access' && mode !== 'full_access' && !consent)}>{busy ? '正在保存…' : '保存设置'}</button></footer>
  </form></Modal>;
}
