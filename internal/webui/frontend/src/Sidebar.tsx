import { useDeferredValue, useState } from 'react';
import { ChevronDown, ChevronRight, Folder, LogOut, Moon, Plus, Search, Settings2, Sun, X } from 'lucide-react';
import { Brand } from './Login';
import { age } from './model';
import type { Snapshot } from './model';
interface Props {
  snapshot: Snapshot; workspace: string; session: string; open: boolean; theme: string; loadingMore: boolean;
  close: () => void; choose: (workspace: string, session?: string) => void; add: () => void; settings: () => void;
  toggleTheme: () => void; logout: () => void; loadMore: () => void;
}
export default function Sidebar(props: Props) {
  const { snapshot, workspace, session } = props;
  const [search, setSearch] = useState('');
  const needle = useDeferredValue(search.toLowerCase());
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
  const toggle = (name: string) => setCollapsed(previous => {
    const next = new Set(previous); if (next.has(name)) next.delete(name); else next.add(name); return next;
  });
  return <>
    {props.open && <button className="sidebar-backdrop" aria-label="关闭侧栏" onClick={props.close}/>}
    <aside className={'sidebar ' + (props.open ? 'open' : '')} aria-label="Workspace 和任务">
      <header className="sidebar-header"><Brand/><button className="icon-button mobile-only" aria-label="关闭侧栏" onClick={props.close}><X size={18}/></button><span className="app-tag">WORKSPACE</span></header>
      <button className="new-workspace" onClick={props.add}><Plus size={17}/>添加 Workspace</button>
      <label className="sidebar-search"><Search size={15}/><input aria-label="搜索 Workspace 和任务" value={search} onChange={event => setSearch(event.target.value)} placeholder="搜索项目和任务"/></label>
      <div className="sidebar-section-label">项目与任务 <span>{snapshot.workspaces.length}</span></div>
      <nav className="workspace-list">
        {snapshot.workspaces.map(ws => {
          const sessions = snapshot.sessions.filter(item => item.workspace === ws.name);
          const matches = sessions.filter(item => !needle || `${item.label} ${item.description} ${ws.name}`.toLowerCase().includes(needle));
          if (needle && !ws.name.toLowerCase().includes(needle) && !matches.length) return null;
          const expanded = !collapsed.has(ws.name) || !!needle;
          return <section className="workspace-group" key={ws.name}>
            <div className={'workspace-row ' + (workspace === ws.name && !session ? 'selected' : '')}>
              <button className="group-toggle" aria-label={(expanded ? '折叠 ' : '展开 ') + ws.name} aria-expanded={expanded} onClick={() => toggle(ws.name)}>{expanded ? <ChevronDown size={13}/> : <ChevronRight size={13}/>}</button>
              <button className="workspace-name" onClick={() => props.choose(ws.name)} title={ws.path}><Folder size={15}/><span>{ws.name}</span></button>
              <span className="group-count">{sessions.filter(item => item.running_tasks > 0).length || ''}</span>
            </div>
            {expanded && <div className="session-list">{matches.map(item => <button key={item.id} title={item.description || item.label || item.id} className={'session-row ' + (session === item.id ? 'selected' : '')} onClick={() => props.choose(ws.name, item.id)}>
              <span className={'status-dot ' + (item.running_tasks > 0 ? 'running' : item.status === 'closed' ? 'closed' : '')}/>
              <span className="session-copy"><span>{item.label || item.description || '未命名会话'}</span><small>{item.running_tasks > 0 ? `${item.running_tasks} 个任务执行中` : age(item.last_active_at)}</small></span>
            </button>)}{!matches.length && <p className="sidebar-empty">尚未观测到会话</p>}</div>}
          </section>;
        })}
        {!snapshot.workspaces.length && <p className="sidebar-empty">添加一个已有项目，开始观察它的工作。</p>}
        {!!snapshot.next_offset && <button className="load-history" disabled={props.loadingMore} onClick={props.loadMore}>{props.loadingMore ? '加载中…' : '加载更多会话'}</button>}
      </nav>
      <footer className="sidebar-footer"><button disabled={!workspace} onClick={props.settings}><Settings2 size={16}/>Workspace 设置</button><div><span className="runtime-version">Runtime {snapshot.version}</span><button className="icon-button" aria-label="切换明暗主题" onClick={props.toggleTheme}>{props.theme === 'dark' ? <Sun size={16}/> : <Moon size={16}/>}</button><button className="icon-button" aria-label="退出登录" onClick={props.logout}><LogOut size={16}/></button></div></footer>
    </aside>
  </>;
}
