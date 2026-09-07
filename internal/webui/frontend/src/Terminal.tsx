import { useEffect, useRef, useState } from 'react';
import { Check, Copy, CornerDownLeft, Search, Square, TerminalSquare } from 'lucide-react';
import { api, message, query } from './api';
import { duration, plainTerminal, statusText } from './model';
import type { Detail, Session, Task } from './model';

interface LogPage { text: string; offset: number; next_offset: number; truncated: boolean; task: Task }
interface ListTask extends Task { session_id: string; session_label: string }
interface Props { workspace: string; session: string; sessions: Session[]; tasks: Task[]; selected: string; onSelect: (id: string) => void }

// In workspace overview mode there is no single session to read, so the most
// recent sessions are polled and their execution tasks merged into one list.
function useOverviewTasks(workspace: string, session: string, sessions: Session[]) {
  const [aggregate, setAggregate] = useState<ListTask[]>([]);
  const overview = !session;
  useEffect(() => {
    if (!overview || !workspace) { setAggregate([]); return; }
    let stopped = false;
    const load = async () => {
      const recent = sessions.filter(item => item.workspace === workspace).slice(0, 8);
      const pages = await Promise.allSettled(recent.map(item => api<Detail>('detail?' + query(workspace, item.id))));
      const merged: ListTask[] = [];
      pages.forEach((page, index) => {
        if (page.status !== 'fulfilled') return;
        const label = recent[index].label || recent[index].id.slice(0, 8);
        for (const task of page.value.tasks) merged.push({ ...task, session_id: recent[index].id, session_label: label });
      });
      if (!stopped) setAggregate(merged);
    };
    void load();
    const timer = setInterval(load, 5000);
    return () => { stopped = true; clearInterval(timer); };
  }, [overview, workspace, session, sessions]);
  return overview ? aggregate : [];
}

export default function Terminal({ workspace, session, sessions, tasks, selected, onSelect }: Props) {
  const [stream, setStream] = useState('combined');
  const [text, setText] = useState('');
  const [error, setError] = useState('');
  const [follow, setFollow] = useState(true);
  const [search, setSearch] = useState('');
  const [copied, setCopied] = useState(false);
  const [trimmed, setTrimmed] = useState(false);
  const [stopping, setStopping] = useState(false);
  const [stopNote, setStopNote] = useState('');
  const [liveTask, setLiveTask] = useState<Task | undefined>();
  const output = useRef<HTMLPreElement>(null);
  const aggregate = useOverviewTasks(workspace, session, sessions);
  const list: ListTask[] = session ? tasks.map(item => ({ ...item, session_id: session, session_label: '' })) : aggregate;
  const task = list.find(item => item.execution_task_id === selected) || list[0];
  const id = task?.execution_task_id || '';
  const owner = task?.session_id || session;

  useEffect(() => {
    const controller = new AbortController();
    let stopped = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let offset: number | undefined;
    setText(''); setError(''); setTrimmed(false); setLiveTask(undefined); setFollow(true);
    if (!id || !owner) return () => controller.abort();
    const read = async () => {
      try {
        const params = query(workspace, owner) + '&execution_task_id=' + encodeURIComponent(id) + '&stream=' + stream + (offset === undefined ? '' : '&offset=' + offset);
        const page = await api<LogPage>('logs?' + params, { signal: controller.signal });
        if (stopped) return;
        if (offset === undefined && page.offset > 0) setTrimmed(true);
        offset = page.next_offset;
        setLiveTask(page.task); setError('');
        if (page.text) setText(previous => {
          const combined = previous + plainTerminal(page.text);
          return combined.slice(-262144);
        });
        if (page.truncated) setTrimmed(true);
      } catch (cause) { if (!stopped) setError(message(cause)); }
      if (!stopped) timer = setTimeout(read, 750);
    };
    void read();
    return () => { stopped = true; controller.abort(); clearTimeout(timer); };
  }, [id, owner, workspace, stream]);

  useEffect(() => {
    if (follow && output.current) output.current.scrollTop = output.current.scrollHeight;
  }, [text, follow]);

  const copy = async () => {
    try { await navigator.clipboard.writeText(text); setCopied(true); }
    catch { setError('无法访问剪贴板，请选中文本复制。'); }
  };
  useEffect(() => { if (copied) { const timer = setTimeout(() => setCopied(false), 1600); return () => clearTimeout(timer); } }, [copied]);
  const stop = async () => {
    if (!id || stopping) return;
    setStopping(true); setStopNote('');
    try {
      const result = await api<{ request: UserRequestLike; stopped_tasks: string[] }>('requests', { method: 'POST', body: JSON.stringify({ workspace, session_id: owner, execution_task_id: id, kind: 'interrupt', body: '操作员停止了此执行。', client_key: 'stop:' + id }) });
      setStopNote(result.stopped_tasks.length ? '已发送停止信号。' : '此任务此前已停止过，未重复操作。');
    } catch (cause) { setError(message(cause)); } finally { setStopping(false); }
  };
  const current = liveTask || task;
  const visible = search ? text.split('\n').filter(line => line.toLowerCase().includes(search.toLowerCase())).join('\n') : text;

  return <section className="terminal-section" aria-label="终端日志">
    <div className="terminal-controls">
      <label className="task-select"><TerminalSquare size={16} /><select aria-label="选择执行任务" value={id} onChange={event => onSelect(event.target.value)} disabled={!list.length}>
        {!list.length && <option value="">{session ? '暂无执行任务' : '这个 Workspace 最近没有执行任务'}</option>}
        {list.map(item => <option key={item.session_id + ':' + item.execution_task_id} value={item.execution_task_id}>{(item.session_label ? item.session_label + ' · ' : '') + (statusText[item.status] || item.status) + ' · ' + item.command.slice(0, 90)}</option>)}
      </select></label>
      {current && <span className="muted mono">{duration(current.runtime_ms)}</span>}
    </div>
    <div className="terminal-window">
      <header className="terminal-bar">
        <div className="terminal-lights" aria-hidden="true"><i/><i/><i/></div>
        <div className="terminal-streams" aria-label="输出流">
          {['combined', 'stdout', 'stderr'].map(value => <button key={value} aria-pressed={stream === value} onClick={() => setStream(value)}>{value === 'combined' ? '全部输出' : value}</button>)}
        </div>
        <button className="icon-button" title="复制输出" aria-label="复制输出" onClick={() => void copy()}>{copied ? <Check size={15}/> : <Copy size={15}/>}</button>
      </header>
      {current && <div className="terminal-command"><span>$</span><code>{current.command}</code></div>}
      <pre ref={output} className="terminal-output" tabIndex={0} aria-label="实时输出" onScroll={() => {
        const element = output.current;
        if (element && element.scrollHeight - element.scrollTop - element.clientHeight > 60) setFollow(false);
      }}>{visible || (id ? (search ? '没有匹配的日志。' : '等待程序输出…') : (session ? '这个会话还没有执行过命令。' : '选择一个会话，查看它真实执行过的命令和输出。'))}</pre>
      {error && <p className="terminal-error" role="alert">{error}</p>}
      <footer className="terminal-footer">
        <span><i className={'status-dot ' + (current?.status === 'running' ? 'running' : '')}/>{current ? statusText[current.status] || current.status : '无任务'}{current?.exit_code !== undefined && ` · exit ${current.exit_code}`}{stopNote && ` · ${stopNote}`}</span>
        <div className="terminal-footer-actions">
          {current?.status === 'running' && <button className="terminal-stop" disabled={stopping} onClick={() => void stop()} title="停止这个任务的本地执行"><Square size={11}/>{stopping ? '停止中…' : '停止此执行'}</button>}
          <button aria-pressed={follow} onClick={() => setFollow(value => !value)}><CornerDownLeft size={13}/>{follow ? '跟随最新输出' : '恢复跟随'}</button>
        </div>
      </footer>
    </div>
    <div className="terminal-search"><Search size={14}/><input aria-label="搜索日志" placeholder="筛选日志内容" value={search} onChange={event => setSearch(event.target.value)}/><small>{trimmed || text.length >= 262144 ? '仅显示最近输出；完整日志保留在 Runtime。' : '日志经脱敏处理 · 只读，不执行终端输入'}</small></div>
  </section>;
}
interface UserRequestLike { replayed?: boolean }
