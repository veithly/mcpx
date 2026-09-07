import { useDeferredValue, useState } from 'react';
import type { DragEvent } from 'react';
import { ArrowDown, ArrowUp, ChevronDown, ChevronRight, Folder, GripVertical, LoaderCircle, LogOut, MoreHorizontal, Moon, Pin, PinOff, Plus, Search, Settings2, Sun, Trash2, X } from 'lucide-react';
import { Brand } from './Login';
import { Modal } from './Dialogs';
import { age } from './model';
import type { Snapshot } from './model';
import { canDrop, isWorking, sameSortGroup, sessionTarget, sortSessions, sortWorkspaces, workingLabel, workspaceTarget } from './sidebar-model';
import type { SidebarMutation, SidebarTarget } from './sidebar-model';
interface Props {
  snapshot: Snapshot; workspace: string; session: string; open: boolean; theme: string; loadingMore: boolean; busy: boolean;
  close: () => void; choose: (workspace: string, session?: string) => void; add: () => void; settings: () => void;
  toggleTheme: () => void; logout: () => void; loadMore: () => void; mutate: (input: SidebarMutation) => Promise<boolean>;
}
const itemKey = (item: SidebarTarget) => item.kind + ':' + item.id;
export default function Sidebar(props: Props) {
  const { snapshot, workspace, session, busy } = props;
  const [search, setSearch] = useState('');
  const needle = useDeferredValue(search.toLowerCase());
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
  const [menu, setMenu] = useState('');
  const [removing, setRemoving] = useState<SidebarTarget>();
  const [removeError, setRemoveError] = useState('');
  const [drag, setDrag] = useState<{item: SidebarTarget; revision: number}>();
  const [drop, setDrop] = useState<{key: string; placement: 'before' | 'after'}>();
  const [announcement, setAnnouncement] = useState('');
  const workspaces = sortWorkspaces(snapshot.workspaces);
  const sessions = sortSessions(snapshot.sessions);
  const toggle = (name: string) => setCollapsed(previous => {
    const next = new Set(previous); if (next.has(name)) next.delete(name); else next.add(name); return next;
  });
  const act = async (item: SidebarTarget, action: SidebarMutation['action'], extra: Partial<SidebarMutation> = {}) => {
    const ok = await props.mutate({kind:item.kind,id:item.id,workspace:item.workspace,action,...extra});
    if(ok){setMenu('');setAnnouncement(action==='pin' ? (extra.pinned ? '已置顶 '+item.label : '已取消置顶 '+item.label) : action==='move' ? '已保存排序' : '已从工作台移除 '+item.label);}
    return ok;
  };
  const dragStart = (event: DragEvent, item: SidebarTarget) => {
    if(busy || needle){event.preventDefault();return;}
    event.stopPropagation();event.dataTransfer.effectAllowed='move';
    event.dataTransfer.setData('application/x-mcpx-sidebar',itemKey(item));
    setDrag({item,revision:snapshot.sidebar_revision});setMenu('');
  };
  const over = (event: DragEvent, item: SidebarTarget) => {
    event.stopPropagation();if(!drag || busy || !canDrop(drag.item,item))return;
    event.preventDefault();event.dataTransfer.dropEffect='move';
    const bounds=event.currentTarget.getBoundingClientRect();
    setDrop({key:itemKey(item),placement:event.clientY<bounds.top+bounds.height/2?'before':'after'});
  };
  const dropped = (event: DragEvent, item: SidebarTarget) => {
    event.stopPropagation();event.preventDefault();
    if(drag && drop?.key===itemKey(item) && canDrop(drag.item,item))void act(drag.item,'move',{target_id:item.id,placement:drop.placement,revision:drag.revision});
    setDrag(undefined);setDrop(undefined);
  };
  const dropClass = (item:SidebarTarget) => drop?.key===itemKey(item) && drag ? ' drop-'+drop.placement : '';
  const grip = (item:SidebarTarget) => <button className="drag-handle" draggable={!busy && !needle} disabled={busy || !!needle} title="拖动排序；也可在更多操作中上移或下移" aria-label={'拖动排序 '+item.label} onDragStart={event=>dragStart(event,item)} onDragEnd={()=>{setDrag(undefined);setDrop(undefined);}}><GripVertical size={13}/></button>;
  const more = (item:SidebarTarget) => <button className="row-more" aria-label={'更多操作 '+item.label} aria-expanded={menu===itemKey(item)} onClick={()=>setMenu(value=>value===itemKey(item)?'':itemKey(item))}><MoreHorizontal size={16}/></button>;
  const actions = (item:SidebarTarget, all:SidebarTarget[]) => {
    if(menu!==itemKey(item))return null;
    const group=all.filter(other=>sameSortGroup(item,other));const index=group.findIndex(other=>other.id===item.id);
    return <div className="row-action-tray" aria-label={item.label+' 的操作'}>
      <button disabled={busy} onClick={()=>void act(item,'pin',{pinned:!item.pinned})}>{item.pinned?<PinOff size={13}/>:<Pin size={13}/>} {item.pinned?'取消置顶':'置顶'}</button>
      <button disabled={busy || !!needle || index<=0} onClick={()=>void act(item,'move',{target_id:group[index-1].id,placement:'before'})}><ArrowUp size={13}/>上移</button>
      <button disabled={busy || !!needle || index<0 || index===group.length-1} onClick={()=>void act(item,'move',{target_id:group[index+1].id,placement:'after'})}><ArrowDown size={13}/>下移</button>
      <button className="delete-action" disabled={busy} onClick={()=>{setRemoving(item);setRemoveError('');setMenu('');}}><Trash2 size={13}/>删除</button>
    </div>;
  };
  const remove = async () => {
    if(!removing)return;setRemoveError('');
    if(await act(removing,'delete',{confirm:true}))setRemoving(undefined);
    else setRemoveError('删除未完成。任务可能仍在运行，或侧栏已更新；请检查工作状态后重试。');
  };
  const currentRemoving = removing?.kind==='session' ? sessions.find(item=>item.id===removing.id) : undefined;
  const removalWorking = removing?.kind==='workspace' ? (workspaces.find(item=>item.name===removing.id)?.working_sessions||0)>0 : currentRemoving ? isWorking(currentRemoving) : false;
  return <>
    {props.open && <button className="sidebar-backdrop" aria-label="关闭侧栏" onClick={props.close}/>}
    <aside className={'sidebar ' + (props.open ? 'open' : '')} aria-label="Workspace 和任务" onKeyDown={event=>{if(event.key==='Escape'){setMenu('');setDrag(undefined);setDrop(undefined);}}}>
      <header className="sidebar-header"><Brand/><button className="icon-button mobile-only" aria-label="关闭侧栏" onClick={props.close}><X size={18}/></button><span className="app-tag">WORKSPACE</span></header>
      <button className="new-workspace" onClick={props.add} disabled={busy}><Plus size={17}/>添加 Workspace</button>
      <label className="sidebar-search"><Search size={15}/><input aria-label="搜索 Workspace 和任务" value={search} onChange={event=>setSearch(event.target.value)} placeholder="搜索项目和任务"/></label>
      <div className="sidebar-section-label">项目与任务 <span>{workspaces.length}</span></div>
      <nav className="workspace-list">
        {workspaces.map(ws=>{
          const target=workspaceTarget(ws);const all=sessions.filter(item=>item.workspace===ws.name);
          const matches=all.filter(item=>!needle || `${item.label} ${item.description} ${ws.name}`.toLowerCase().includes(needle));
          if(needle && !ws.name.toLowerCase().includes(needle) && !matches.length)return null;
          const expanded=!collapsed.has(ws.name) || !!needle;
          const visible=expanded?matches:matches.filter(isWorking);
          let lastTier='';
          return <section className="workspace-group" key={ws.name}>
            <div className={'sidebar-item'+dropClass(target)} onDragOver={event=>over(event,target)} onDrop={event=>dropped(event,target)}>
              <div className={'workspace-row '+(workspace===ws.name&&!session?'selected':'')}>
                {grip(target)}<button className="group-toggle" aria-label={(expanded?'折叠 ':'展开 ')+ws.name} aria-expanded={expanded} onClick={()=>toggle(ws.name)}>{expanded?<ChevronDown size={13}/>:<ChevronRight size={13}/>}</button>
                <button className="workspace-name" onClick={()=>props.choose(ws.name)} title={ws.path}><Folder size={15}/><span>{ws.name}</span></button>
                {ws.pinned && <Pin className="pin-indicator" size={11} aria-label="已置顶"/>}
                {!!ws.working_sessions && <span className="working-count" title={ws.working_sessions+' 个会话正在工作'}>{ws.working_sessions}<span className="sr-only">个会话正在工作</span></span>}{more(target)}
              </div>{actions(target,workspaces.map(workspaceTarget))}
            </div>
            {!!visible.length && <div className="session-list">{visible.map(item=>{
              const target=sessionTarget(item);const tier=target.working?'正在工作':target.pinned?'已置顶':'最近会话';const heading=lastTier!==tier;lastTier=tier;
              return <div key={item.id}>
                {heading && <div className={'session-tier '+(target.working?'working-tier':'')}>{tier}{target.working && <span>自动置顶</span>}</div>}
                <div className={'sidebar-item'+dropClass(target)} onDragOver={event=>over(event,target)} onDrop={event=>dropped(event,target)}>
                  <div className={'session-row '+(session===item.id?'selected ':'')+(target.working?'working-row':'')}>
                    {grip(target)}<button className="session-select" title={target.label+'\n项目：'+(item.workspace_path||ws.path)} aria-current={session===item.id?'page':undefined} onClick={()=>props.choose(ws.name,item.id)}>
                      {target.working?<LoaderCircle size={12} className="spin working-spinner"/>:<span className={'status-dot '+(item.status==='closed'?'closed':'')}/>}
                      <span className="session-copy"><span>{target.label}</span><small>{target.working?workingLabel(item):age(item.last_active_at)}</small></span>
                    </button>{item.pinned && <Pin className="pin-indicator" size={10} aria-label="已置顶"/>}{more(target)}
                  </div>{actions(target,all.map(sessionTarget))}
                </div>
              </div>;
            })}</div>}
            {expanded&&!matches.length && <p className="sidebar-empty">尚未观测到会话</p>}
          </section>;
        })}
        {!workspaces.length && <p className="sidebar-empty">添加一个已有项目，开始观察它的工作。</p>}
        {!!snapshot.next_offset && snapshot.sessions.length<3000 && <button className="load-history" disabled={props.loadingMore||busy} onClick={props.loadMore}>{props.loadingMore?'加载中…':'加载更多会话'}</button>}
        {snapshot.sessions.length>=3000&&!!snapshot.next_offset&&<p className="sidebar-empty">当前显示前 3,000 个会话。</p>}
      </nav>
      <p className="sidebar-order-hint">{busy?'正在保存…':needle?'清除搜索后可调整顺序':'运行优先 · 置顶优先 · 同组拖动排序'}</p>
      <span className="sr-only" role="status">{announcement}</span>
      <footer className="sidebar-footer"><button disabled={!workspace} onClick={props.settings}><Settings2 size={16}/>Workspace 设置</button><div><span className="runtime-version">Runtime {snapshot.version}</span><button className="icon-button" aria-label="切换明暗主题" onClick={props.toggleTheme}>{props.theme==='dark'?<Sun size={16}/>:<Moon size={16}/>}</button><button className="icon-button" aria-label="退出登录" onClick={props.logout}><LogOut size={16}/></button></div></footer>
    </aside>
    {removing && <Modal title={removing.kind==='workspace'?'删除 Workspace？':'删除会话？'} close={()=>{if(!busy)setRemoving(undefined);}}>
      <p className="modal-description">将 <strong>{removing.label}</strong>{removing.kind==='workspace'?'及其所有会话从工作台移除。':'从工作台移除。'}这是工作台软删除，项目文件和审计日志不会被擦除。</p>
      {removing.path && <code className="remove-path">{removing.path}</code>}
      {removing.kind==='workspace' && <p className="field-help">重新添加同一目录可恢复 Workspace 入口，但不会自动恢复已删除会话。</p>}
      {removalWorking && <p className="form-error" role="alert">这个{removing.kind==='workspace'?'项目':'会话'}仍在工作。请先在终端或请求区停止执行，等待工具调用结束，再删除。</p>}
      {removeError && <p className="form-error" role="alert">{removeError}</p>}
      <footer className="modal-actions"><button className="secondary" disabled={busy} onClick={()=>setRemoving(undefined)}>取消</button><button className="primary destructive-button" disabled={busy||removalWorking} onClick={()=>void remove()}>{busy?'正在删除…':'确认删除'}</button></footer>
    </Modal>}
  </>;
}
