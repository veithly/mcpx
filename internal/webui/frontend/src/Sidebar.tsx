import { useCallback, useDeferredValue, useState } from 'react';
import type { DragEvent, MouseEvent, KeyboardEvent } from 'react';
import { ArrowDown, ArrowUp, ChevronDown, ChevronRight, ExternalLink, Folder, GripVertical, LayoutGrid, LoaderCircle, LogOut, MoreHorizontal, Moon, Pin, PinOff, Plus, Search, Settings2, ShieldCheck, Sun, Trash2, X } from 'lucide-react';
import { Brand } from './Login';
import { Modal } from './Dialogs';
import ContextMenu from './ContextMenu';
import type { MenuAction, MenuAnchor } from './ContextMenu';
import { age } from './model';
import type { Snapshot } from './model';
import { canDrop, isWorking, sameSortGroup, sessionTarget, sessionURL, sortSessions, sortWorkspaces, workingLabel, workspaceTarget } from './sidebar-model';
import type { SidebarMutation, SidebarTarget } from './sidebar-model';
interface Props {
  snapshot: Snapshot; workspace: string; session: string; open: boolean; theme: string; loadingMore: boolean; busy: boolean;
  close: () => void; choose: (workspace: string, session?: string) => void; add: () => void; settings: () => void; permissions: () => void;
  toggleTheme: () => void; logout: () => void; loadMore: () => void; mutate: (input: SidebarMutation) => Promise<boolean>;
}
const itemKey = (item: SidebarTarget) => item.kind + ':' + item.id;
export default function Sidebar(props: Props) {
  const { snapshot, workspace, session, busy } = props;
  const [search,setSearch]=useState('');
  const needle=useDeferredValue(search.trim().toLowerCase());
  // Polls never unfold projects or change the selected conversation.
  const [expanded,setExpanded]=useState<Set<string>>(new Set());
  const [showAll,setShowAll]=useState<Set<string>>(new Set());
  const [menu,setMenu]=useState<{item:SidebarTarget;anchor:MenuAnchor}>();
  const closeMenu=useCallback(()=>setMenu(undefined),[]);
  const [removing,setRemoving]=useState<SidebarTarget>();
  const [removeError,setRemoveError]=useState('');
  const [drag,setDrag]=useState<{item:SidebarTarget;revision:number}>();
  const [drop,setDrop]=useState<{key:string;placement:'before'|'after'}>();
  const [announcement,setAnnouncement]=useState('');
  const workspaces=sortWorkspaces(snapshot.workspaces);
  const sessions=sortSessions(snapshot.sessions);
  const toggle=(name:string)=>setExpanded(previous=>{const next=new Set(previous);if(next.has(name))next.delete(name);else next.add(name);return next;});
  const act=async(item:SidebarTarget,action:SidebarMutation['action'],extra:Partial<SidebarMutation>={})=>{
    const ok=await props.mutate({kind:item.kind,id:item.id,workspace:item.workspace,action,...extra});
    if(ok){closeMenu();setAnnouncement(action==='pin'?(extra.pinned?'已置顶 ':'已取消置顶 ')+item.label:action==='move'?'已保存排序':'已从工作台移除 '+item.label);}return ok;
  };
  const showMenu=(event:MouseEvent<HTMLElement>|KeyboardEvent<HTMLElement>,item:SidebarTarget)=>{
    event.preventDefault();event.stopPropagation();
    const element=event.currentTarget;
    const trigger=element.matches('button')?element:element.querySelector<HTMLButtonElement>('.workspace-name,.session-select')||element;
    const rect=trigger.getBoundingClientRect();
    const pointer='clientX' in event && (event.clientX>0||event.clientY>0);
    setMenu({item,anchor:{x:pointer?event.clientX:rect.right,y:pointer?event.clientY:rect.bottom,trigger}});
  };
  const menuKeyboard=(event:KeyboardEvent<HTMLElement>,item:SidebarTarget)=>{if(event.key==='ContextMenu'||(event.shiftKey&&event.key==='F10'))showMenu(event,item);};
  const dragStart=(event:DragEvent,item:SidebarTarget)=>{
    if(busy||needle){event.preventDefault();return;}event.stopPropagation();event.dataTransfer.effectAllowed='move';event.dataTransfer.setData('application/x-mcpx-sidebar',itemKey(item));setDrag({item,revision:snapshot.sidebar_revision});closeMenu();
  };
  const over=(event:DragEvent,item:SidebarTarget)=>{
    event.stopPropagation();if(!drag||busy||!canDrop(drag.item,item))return;event.preventDefault();event.dataTransfer.dropEffect='move';const bounds=event.currentTarget.getBoundingClientRect();setDrop({key:itemKey(item),placement:event.clientY<bounds.top+bounds.height/2?'before':'after'});
  };
  const dropped=(event:DragEvent,item:SidebarTarget)=>{
    event.stopPropagation();event.preventDefault();if(drag&&drop?.key===itemKey(item)&&canDrop(drag.item,item))void act(drag.item,'move',{target_id:item.id,placement:drop.placement,revision:drag.revision});setDrag(undefined);setDrop(undefined);
  };
  const dropClass=(item:SidebarTarget)=>drop?.key===itemKey(item)&&drag?' drop-'+drop.placement:'';
  const grip=(item:SidebarTarget)=><button className="drag-handle" draggable={!busy&&!needle} disabled={busy||!!needle} title="拖动调整同组顺序" aria-label={'拖动排序 '+item.label} onDragStart={event=>dragStart(event,item)} onDragEnd={()=>{setDrag(undefined);setDrop(undefined);}}><GripVertical size={13}/></button>;
  const more=(item:SidebarTarget)=><button className="row-more" aria-label={'更多操作 '+item.label} aria-haspopup="menu" aria-expanded={menu?.item.id===item.id&&menu.item.kind===item.kind} onClick={event=>showMenu(event,item)}><MoreHorizontal size={16}/></button>;
  const menuActions=():MenuAction[]=>{
    if(!menu)return [];
    const item=menu.item;
    const all=item.kind==='workspace'?workspaces.map(workspaceTarget):sessions.filter(s=>s.workspace===item.workspace).map(sessionTarget);
    const group=all.filter(other=>sameSortGroup(item,other));const index=group.findIndex(other=>other.id===item.id);
    return [
      {id:'open',label:item.kind==='workspace'?'进入活跃会话':'进入会话',icon:<Folder size={15}/>,run:()=>props.choose(item.workspace,item.kind==='session'?item.id:undefined)},
      {id:'parallel',label:'在新标签页中打开',icon:<ExternalLink size={15}/>,run:()=>{const id=item.kind==='session'?item.id:workspaces.find(ws=>ws.name===item.workspace)?.preferred_session_id||'';window.open(sessionURL(item.workspace,id),'_blank','noopener,noreferrer');}},
      ...(item.kind==='workspace'?[{id:'overview',label:'项目总览',icon:<LayoutGrid size={15}/>,run:()=>props.choose(item.workspace,'')}]:[]),
      {id:'pin',label:item.pinned?'取消置顶':'置顶',icon:item.pinned?<PinOff size={15}/>:<Pin size={15}/>,disabled:busy,run:()=>void act(item,'pin',{pinned:!item.pinned})},
      {id:'up',label:'上移',icon:<ArrowUp size={15}/>,disabled:busy||!!needle||index<=0,run:()=>void act(item,'move',{target_id:group[index-1].id,placement:'before'})},
      {id:'down',label:'下移',icon:<ArrowDown size={15}/>,disabled:busy||!!needle||index<0||index===group.length-1,run:()=>void act(item,'move',{target_id:group[index+1].id,placement:'after'})},
      {id:'remove',label:'从工作台移除…',icon:<Trash2 size={15}/>,disabled:busy,danger:true,run:()=>{setRemoving(item);setRemoveError('');}},
    ];
  };
  const remove=async()=>{if(!removing)return;setRemoveError('');if(await act(removing,'delete',{confirm:true}))setRemoving(undefined);else setRemoveError('移除未完成。请检查会话是否仍在工作，再重试。');};
  const currentRemoving=removing?.kind==='session'?sessions.find(item=>item.id===removing.id):undefined;
  const removalWorking=removing?.kind==='workspace'?(workspaces.find(item=>item.name===removing.id)?.working_sessions||0)>0:currentRemoving?isWorking(currentRemoving):false;
  let lastGroup='';let matchedProjects=0;
  return <>
    {props.open&&<button className="sidebar-backdrop" aria-label="关闭侧栏" onClick={props.close}/>}
    <aside className={'sidebar '+(props.open?'open':'')} aria-label="项目与会话" onKeyDown={event=>{if(event.key==='Escape'){closeMenu();setDrag(undefined);setDrop(undefined);}}}>
      <header className="sidebar-header"><Brand/><button className="icon-button mobile-only" aria-label="关闭侧栏" onClick={props.close}><X size={18}/></button></header>
      <button className="new-workspace" onClick={props.add} disabled={busy}><Plus size={17}/>添加项目</button>
      <label className="sidebar-search"><Search size={15}/><input aria-label="搜索项目和会话" value={search} onChange={event=>setSearch(event.target.value)} placeholder="搜索项目或会话"/>{search&&<button aria-label="清除搜索" onClick={()=>setSearch('')}><X size={13}/></button>}</label>
      <nav className="workspace-list" aria-label="工作区导航">
        {workspaces.map(ws=>{
          const target=workspaceTarget(ws);const all=sessions.filter(item=>item.workspace===ws.name);
          const matches=all.filter(item=>!needle||`${item.label} ${item.description} ${item.id} ${ws.name}`.toLowerCase().includes(needle));
          if(needle&&!ws.name.toLowerCase().includes(needle)&&!matches.length)return null;
          matchedProjects++;
          const group=needle?'搜索结果':ws.is_active?'活跃项目':'项目';const heading=lastGroup!==group;lastGroup=group;
          const unfolded=expanded.has(ws.name)||!!needle;
          const visible=unfolded?(showAll.has(ws.name)||needle?matches:matches.slice(0,6)):[];
          return <section className="workspace-group" key={ws.name}>
            {heading&&<div className="sidebar-section-label"><span>{group}</span>{group==='活跃项目'&&<small title="命令正在执行，或最近三分钟执行过命令">近 3 分钟</small>}</div>}
            <div className={'sidebar-item'+dropClass(target)} onContextMenu={event=>showMenu(event,target)} onKeyDown={event=>menuKeyboard(event,target)} onDragOver={event=>over(event,target)} onDrop={event=>dropped(event,target)}>
              <div className={'workspace-row '+(workspace===ws.name?'selected':'')}>
                {grip(target)}<button className="group-toggle" aria-label={(unfolded?'折叠 ':'展开 ')+ws.name} aria-expanded={unfolded} onClick={()=>toggle(ws.name)}>{unfolded?<ChevronDown size={14}/>:<ChevronRight size={14}/>}</button>
                <button className="workspace-name" onClick={()=>props.choose(ws.name)} title={ws.path} aria-current={workspace===ws.name?'page':undefined}><Folder size={15}/><span>{ws.name}</span></button>
                {ws.pinned&&<Pin className="pin-indicator" size={11} aria-label="已置顶"/>}
                {ws.is_active&&<span className="status-dot running" title="近三分钟有命令执行"/>}
                <span className="project-session-count" title={`${ws.session_count??all.length} 个会话，${ws.working_sessions||0} 个正在工作`}>{ws.working_sessions?`${ws.working_sessions} 运行`:ws.session_count??all.length}</span>{more(target)}
              </div>
            </div>
            {!!visible.length&&<div className="session-list">{visible.map(item=>{
              const target=sessionTarget(item);
              return <div className={'sidebar-item'+dropClass(target)} key={item.id} onContextMenu={event=>showMenu(event,target)} onKeyDown={event=>menuKeyboard(event,target)} onDragOver={event=>over(event,target)} onDrop={event=>dropped(event,target)}>
                <div className={'session-row '+(session===item.id?'selected ':'')+(target.working?'working-row':'')}>
                  {grip(target)}<button className="session-select" title={target.label+'\n'+item.id} aria-current={session===item.id?'page':undefined} onClick={()=>props.choose(ws.name,item.id)}>
                    {target.working?<LoaderCircle size={12} className="spin working-spinner"/>:<span className={'status-dot '+(item.status==='closed'?'closed':'')}/>}
                    <span className="session-copy"><span>{target.label}</span><small>{target.working?workingLabel(item):age(item.last_active_at)}<code>{item.id.slice(-6)}</code></small></span>
                  </button>{item.pinned&&<Pin className="pin-indicator" size={10} aria-label="已置顶"/>}{more(target)}
                </div>
              </div>;
            })}</div>}
            {unfolded&&!matches.length&&<p className="sidebar-empty">暂无会话。在 GPT 中为此项目开启会话。</p>}
            {unfolded&&visible.length<matches.length&&<button className="session-more" onClick={()=>setShowAll(previous=>new Set([...previous,ws.name]))}>显示其余 {matches.length-visible.length} 个会话</button>}
          </section>;
        })}
        {!matchedProjects&&<p className="sidebar-empty">{needle?'没有匹配的项目或会话':'添加一个已有项目，开始工作。'}</p>}
        {!!snapshot.next_offset&&snapshot.sessions.length<3000&&<button className="load-history" disabled={props.loadingMore||busy} onClick={props.loadMore}>{props.loadingMore?'加载中…':'加载更多会话'}</button>}
      </nav>
      <span className="sr-only" role="status">{announcement}</span>
      <footer className="sidebar-footer"><button disabled={!workspace} onClick={props.settings}><Settings2 size={16}/>项目设置</button><button onClick={props.permissions}><ShieldCheck size={16}/>系统权限</button><div><span className="runtime-version">Runtime {snapshot.version}{busy?' · 保存中':''}</span><button className="icon-button" aria-label="切换明暗主题" onClick={props.toggleTheme}>{props.theme==='dark'?<Sun size={16}/>:<Moon size={16}/>}</button><button className="icon-button" aria-label="退出登录" onClick={props.logout}><LogOut size={16}/></button></div></footer>
    </aside>
    {menu&&<ContextMenu anchor={menu.anchor} label={menu.item.label} actions={menuActions()} close={closeMenu}/>}
    {removing&&<Modal title={removing.kind==='workspace'?'移除项目？':'移除会话？'} close={()=>{if(!busy)setRemoving(undefined);}}>
      <p className="modal-description">将 <strong>{removing.label}</strong>{removing.kind==='workspace'?'及其会话从工作台移除。':'从工作台移除。'}项目文件和审计日志不会删除。</p>
      {removing.path&&<code className="remove-path">{removing.path}</code>}
      {removalWorking&&<p className="form-error" role="alert">仍有任务正在工作，请先停止执行再移除。</p>}
      {removeError&&<p className="form-error" role="alert">{removeError}</p>}
      <footer className="modal-actions"><button className="secondary" disabled={busy} onClick={()=>setRemoving(undefined)}>取消</button><button className="primary destructive-button" disabled={busy||removalWorking} onClick={()=>void remove()}>{busy?'正在移除…':'确认移除'}</button></footer>
    </Modal>}
  </>;
}
