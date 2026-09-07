import { useEffect, useRef, useState } from 'react';
import { Activity, ArrowDown, ChevronRight, Folder, FolderPlus, Menu, MessageSquare, RefreshCw, ShieldCheck, ShieldOff, TerminalSquare } from 'lucide-react';
import { api, message, setCSRF } from './api';
import type { Auth, Session, Snapshot } from './model';
import Login, { Brand } from './Login';
import Sidebar from './Sidebar';
import { AccessSettings, AddWorkspace } from './Dialogs';
import { Composer, Requests } from './Requests';
import Terminal from './Terminal';
import Timeline from './Timeline';
import { useWorkspace } from './useWorkspace';

export default function App() {
  const [auth, setAuth] = useState<Auth>();
  const [error, setError] = useState('');
  const [retry, setRetry] = useState(0);
  const [theme, setTheme] = useState(() => { try { return localStorage.getItem('mcpx:theme') || 'light'; } catch { return 'light'; } });
  useEffect(() => { document.documentElement.dataset.theme = theme; try { localStorage.setItem('mcpx:theme', theme); } catch { /* Storage is optional. */ } }, [theme]);
  useEffect(() => {
    let stopped = false;
    setError('');
    const load = async () => {
      try { const result = await api<Auth>('session'); if (!stopped) { setCSRF(result.csrf || ''); setAuth(result); } }
      catch (cause) { if (!stopped) setError(message(cause)); }
    };
    const expired = () => { setCSRF(''); setAuth(previous => ({ ...previous, authenticated: false })); };
    window.addEventListener('mcpx:unauthorized', expired); void load();
    return () => { stopped = true; window.removeEventListener('mcpx:unauthorized', expired); };
  }, [retry]);
  const loggedIn = (result: Auth) => { setCSRF(result.csrf || ''); setAuth(previous => ({ ...previous, ...result })); };
  if (!auth) return <div className="boot-screen"><Brand/><p>{error || '正在连接你的 Runtime…'}</p>{error && <button className="secondary" onClick={() => setRetry(value => value + 1)}>重新连接</button>}</div>;
  if (!auth.authenticated) return <Login auth={auth} onLogin={loggedIn}/>;
  return <Workbench theme={theme} toggleTheme={() => setTheme(value => value === 'dark' ? 'light' : 'dark')} logout={async () => { await api('session', { method: 'DELETE' }); setCSRF(''); setAuth(previous => ({ ...previous, authenticated: false })); }}/>;
}

function Workbench({ theme, toggleTheme, logout }: { theme: string; toggleTheme: () => void; logout: () => Promise<void> }) {
  const [snapshot, setSnapshot] = useState<Snapshot>({ workspaces: [], sessions: [], next_offset: 0, version: '' });
  const [workspace, setWorkspace] = useState(() => new URLSearchParams(location.hash.slice(1)).get('workspace') || '');
  const [session, setSession] = useState(() => new URLSearchParams(location.hash.slice(1)).get('session') || '');
  const [tab, setTab] = useState('activity'); const [selectedTask, setSelectedTask] = useState('');
  const [modal, setModal] = useState<'workspace' | 'access' | ''>('');
  const [sidebarOpen, setSidebarOpen] = useState(false);
  const [error, setError] = useState(''); const [loadingMore, setLoadingMore] = useState(false);
  const [follow, setFollow] = useState(true);
  const scroller = useRef<HTMLDivElement>(null);
  const initial = useRef(true);
  const live = useWorkspace(workspace, session);
  const activeSession = snapshot.sessions.find(item => item.id === session);
  const activeWorkspace = snapshot.workspaces.find(item => item.name === workspace);
  const pending = live.detail.approvals.filter(item => !item.decision).length;
  const unacknowledged = live.detail.requests.filter(item => item.status !== 'acknowledged').length;

  useEffect(() => {
    let stopped = false; let timer: ReturnType<typeof setTimeout> | undefined;
    const controller = new AbortController();
    const poll = async () => {
      try {
        const result = await api<Snapshot>('state', { signal: controller.signal });
        if (stopped) return;
        const first = initial.current; initial.current = false;
        setSnapshot(previous => ({ ...result, sessions: combineSessions(previous.sessions, result.sessions), next_offset: first ? result.next_offset : previous.next_offset }));
        setWorkspace(previous => previous || result.workspaces[0]?.name || ''); setError('');
      } catch (cause) { if (!stopped) setError(message(cause)); }
      if (!stopped) timer = setTimeout(poll, 3000);
    };
    void poll(); return () => { stopped = true; controller.abort(); clearTimeout(timer); };
  }, []);

  useEffect(() => { history.replaceState(null, '', '#' + new URLSearchParams({ workspace, session })); setFollow(true); }, [workspace, session]);
  useEffect(() => { if (follow && tab === 'activity' && scroller.current) scroller.current.scrollTop = scroller.current.scrollHeight; }, [live.events, follow, tab]);
  const choose = (name: string, id = '') => { setWorkspace(name); setSession(id); setSelectedTask(''); setSidebarOpen(false); setTab('activity'); };
  const loadMore = async () => {
    if (loadingMore || !snapshot.next_offset) return;
    setLoadingMore(true);
    try { const result = await api<Snapshot>('state?offset=' + snapshot.next_offset); setSnapshot(previous => ({ ...previous, sessions: combineSessions(previous.sessions, result.sessions), next_offset: result.next_offset })); }
    catch (cause) { setError(message(cause)); } finally { setLoadingMore(false); }
  };
  const viewTask = (id: string, ownerSession?: string) => {
    if (ownerSession) setSession(ownerSession);
    setSelectedTask(id); setTab('terminal');
  };
  const errorText = error || live.error;
  return <div className="app-shell">
    <Sidebar snapshot={snapshot} workspace={workspace} session={session} open={sidebarOpen} close={() => setSidebarOpen(false)} choose={choose} add={() => setModal('workspace')} settings={() => setModal('access')} theme={theme} toggleTheme={toggleTheme} logout={() => void logout().catch(cause => setError(message(cause)))} loadMore={() => void loadMore()} loadingMore={loadingMore}/>
    <main className="workspace-main">
      <header className="main-header"><div className="breadcrumb"><button className="icon-button mobile-only" aria-label="打开任务侧栏" onClick={() => setSidebarOpen(true)}><Menu size={20}/></button><Folder size={15}/><span>{workspace || 'Workspace'}</span><ChevronRight size={13}/><strong>{session ? activeSession?.label || '任务会话' : '项目总览'}</strong></div><div className="header-status"><span className={'connection ' + live.connection}><i/>{({ live: '实时连接', connecting: '连接中', reconnecting: '正在重连', offline: '连接失败', idle: '尚未选择' } as Record<string,string>)[live.connection]}</span><button className={'access-badge ' + live.detail.access_mode} disabled={!workspace} onClick={() => setModal('access')}>{live.detail.access_mode === 'full_access' ? <ShieldOff size={14}/> : <ShieldCheck size={14}/>}<span>{live.detail.access_mode === 'full_access' ? '完全访问' : '审批模式'}</span></button></div></header>
      {errorText && <div className="global-error" role="alert"><span>{errorText}</span><button onClick={live.refresh}><RefreshCw size={14}/>重新连接</button></div>}
      <div className="workspace-content">
        <section className="workspace-heading"><div><span className="eyebrow">{session ? 'AGENT SESSION' : 'WORKSPACE OVERVIEW'}</span><h1>{session ? activeSession?.label || '任务会话' : workspace || '你的工作，从这里开始。'}</h1><p>{session ? activeSession?.description || session : activeWorkspace?.path || '添加一个项目，让工作有迹可循。'}</p></div>{live.detail.tasks.some(item => item.status === 'running') && <span className="running-pill"><i className="status-dot running"/>正在执行</span>}</section>
        <nav className="tabs" aria-label="任务视图">{[{ id: 'activity', label: '工作流', icon: <Activity size={15}/> }, { id: 'terminal', label: '终端', icon: <TerminalSquare size={15}/> }, { id: 'requests', label: '请求', icon: <MessageSquare size={15}/> }].map(item => <button key={item.id} aria-current={tab === item.id ? 'page' : undefined} className={tab === item.id ? 'active' : ''} onClick={() => setTab(item.id)}>{item.icon}{item.label}{item.id === 'requests' && !!(pending + unacknowledged) && <span className="tab-count">{pending + unacknowledged}</span>}</button>)}</nav>
        {!!pending && tab !== 'requests' && <button className="approval-banner" onClick={() => setTab('requests')}><ShieldCheck size={16}/>{pending} 个操作正在等待审批（也可以直接在 GPT 对话中确认）<ChevronRight size={16}/></button>}
        <div ref={scroller} className="content-scroll" onScroll={() => { const el = scroller.current; if (el && el.scrollHeight - el.scrollTop - el.clientHeight > 80) setFollow(false); }}>
          {tab === 'activity' && <Timeline events={live.events} hasOlder={live.hasOlder} loadingOlder={live.loadingOlder} loadOlder={async () => { setFollow(false); await live.loadOlder(); }} onTask={viewTask}/>}
          {tab === 'terminal' && <Terminal workspace={workspace} session={session} sessions={snapshot.sessions} tasks={live.detail.tasks} selected={selectedTask} onSelect={setSelectedTask}/>}
          {tab === 'requests' && <Requests workspace={workspace} session={session} detail={live.detail} refreshed={live.refresh}/>}
          {!workspace && <button className="primary welcome-add" onClick={() => setModal('workspace')}><FolderPlus size={17}/>添加第一个 Workspace</button>}
        </div>
        {tab === 'activity' && !follow && <button className="jump-latest" onClick={() => setFollow(true)}><ArrowDown size={13}/>回到最新</button>}
        {workspace && <Composer key={workspace + ':' + session} workspace={workspace} session={session} onSent={live.refresh}/>}
      </div>
    </main>
    {modal === 'workspace' && <AddWorkspace close={() => setModal('')} added={ws => { setSnapshot(previous => ({ ...previous, workspaces: [...previous.workspaces.filter(item => item.name !== ws.name), ws] })); choose(ws.name); }}/>}
    {modal === 'access' && workspace && <AccessSettings workspace={workspace} mode={live.detail.access_mode} close={() => setModal('')} saved={live.refresh}/>}
  </div>;
}

function combineSessions(previous: Session[], incoming: Session[]) {
  return [...new Map([...previous, ...incoming].map(item => [item.id, item])).values()].sort((a,b) => b.last_active_at - a.last_active_at).slice(0, 3000);
}
