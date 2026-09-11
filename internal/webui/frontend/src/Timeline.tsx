import { useMemo } from 'react';
import { Activity as ActivityIcon, Check, ChevronRight, Eye, FileText, Globe, LoaderCircle, MessageSquare, TerminalSquare, TriangleAlert } from 'lucide-react';
import { argsRows, countDiffChanges, describeEdits, describeReadTargets, describeReadTitle, duration, extractDiffBlocks, parseCommandSummary, parseDiff, plainTerminal, statusText, stripContext, summaryOf, timelineEntries, toolTitle } from './model';
import type { Activity } from './model';

const activityLabels: Record<string, string> = { intent: '工作目标', hypothesis: '待验证方向', evidence: '新发现', conclusion: '当前结论', next: '下一步', status: '工作状态' };

function EventIcon({ event }: { event: Activity }) {
  if (event.status === 'failed') return <TriangleAlert size={16}/>;
  if (event.type === 'tool.started') return <LoaderCircle size={16} className="spin"/>;
  if (event.command || event.tool === 'execute') return <TerminalSquare size={16}/>;
  if (event.tool === 'edit' || event.type === 'file.changed' || event.path) return <FileText size={16}/>;
  if (event.tool === 'mcp_tool') return <Globe size={16}/>;
  if (event.tool === 'read') return <Eye size={16}/>;
  if (event.type.startsWith('operator.')) return <MessageSquare size={16}/>;
  return <Check size={15}/>;
}

function StatusPill({ status }: { status?: string }) {
  if (!status) return null;
  return <span className={'status-pill ' + status}>{statusText[status] || status}</span>;
}

function DiffView({ diff }: { diff: string }) {
  return <pre className="diff-view">{parseDiff(diff).map((line, index) => <span key={index} className={'diff-line ' + line.kind}>{line.text || ' '}</span>)}</pre>;
}

function StreamView({ stream, text }: { stream: string; text: string }) {
  if (!text.trim()) return null;
  return <div className={'stream-block ' + stream}><span className="stream-label">{stream === 'stderr' ? '标准错误' : '标准输出'}</span><pre>{plainTerminal(text).replace(/\n$/, '')}</pre></div>;
}

function ResultText({ text }: { text: string }) {
  const body = stripContext(text);
  if (!body.trim()) return null;
  return <pre className="result-text">{body}</pre>;
}

function ToolBody({ event, onTask }: { event: Activity; onTask: (id: string, session?: string) => void }) {
  const summary = summaryOf(event);
  const note: string[] = [];
  const streams: { stream: string; text: string }[] = [];
  const diffs = event.diffs?.length ? event.diffs : extractDiffBlocks(summary);
  let resultNote: string | null = null;

  if (event.tool === 'execute') {
    const parsed = parseCommandSummary(summary);
    if (parsed.note) note.push(parsed.note);
    const chunks = event.outputs?.filter(chunk => chunk.text.trim()) ?? [];
    if (chunks.length) {
      for (const chunk of chunks) streams.push(chunk);
    } else {
      if (parsed.stdout) streams.push({ stream: 'stdout', text: parsed.stdout });
      if (parsed.stderr) streams.push({ stream: 'stderr', text: parsed.stderr });
    }
  } else if (event.tool === 'edit') {
    resultNote = '文件已按变更写入工作区。';
  } else {
    if (event.outputs?.length) for (const chunk of event.outputs) streams.push(chunk);
  }
  const readTargets = event.tool === 'read' ? describeReadTargets(event.input) : [];
  const bodyText = event.tool === 'execute' ? '' : summary;
  const showSummary = bodyText && event.tool !== 'edit' && !(event.tool === 'read' && readTargets.length > 0 && event.status === 'succeeded');
  return <>
    {event.tool === 'execute' && event.command && <div className="cmd-row"><span className="cmd-prompt">$</span><code>{event.command}</code>{event.working_directory && <span className="cwd-chip" title={event.working_directory}>{event.working_directory.split('/').filter(Boolean).slice(-1)[0] || event.working_directory}</span>}{event.exit_code !== undefined && event.exit_code !== null && <span className={'exit-chip ' + (event.exit_code === 0 ? 'ok' : 'bad')}>exit {event.exit_code}</span>}</div>}
    {event.tool === 'read' && readTargets.length > 0 && <div className="edit-paths">{readTargets.map(path => <span className="path-chip" key={path}>{path}</span>)}</div>}
    {event.tool === 'edit' && <div className="edit-paths">{describeEdits(event.input).map((line, index) => <span className="path-chip" key={index}>{line}</span>)}{(event.changed_paths ?? (event.path ? [event.path] : [])).map(path => <span className="path-chip" key={path}>{path}</span>)}</div>}
    {event.tool === 'mcp_tool' && argsRows(event.input).length > 0 && <div className="args-table">{argsRows(event.input).map(([key, value]) => value.trim() && <div className="args-row" key={key}><small>{key}</small><code>{value.length > 220 ? value.slice(0, 220) + '…' : value}</code></div>)}</div>}
    {note.map((line, index) => line && <p className="event-note" key={index}>{line}</p>)}
    {streams.map((chunk, index) => <StreamView key={index} stream={chunk.stream} text={chunk.text}/>)}
    {!!diffs.length && <div className="diff-group">{(() => { const { added, removed } = countDiffChanges(diffs); return <div className="diff-stat"><FileText size={12}/>{event.changed_paths?.[0] || event.path || '变更'}<em>{added ? ` +${added}` : ''}{removed ? ` −${removed}` : ''}</em></div>; })()}{diffs.map((diff, index) => <DiffView key={index} diff={diff}/>)}</div>}
    {showSummary && <ResultText text={bodyText}/>}
    {resultNote && event.status === 'succeeded' && <p className="event-note subtle">{resultNote}</p>}
    {event.status === 'waiting_confirmation' && <p className="event-note warn">等待用户或管理端确认后，GPT 会以相同参数重试。</p>}
    {event.execution_task_id && <button className="text-button" onClick={() => onTask(event.execution_task_id!, event.remote_session_id)}>在终端中打开完整日志 <ChevronRight size={13}/></button>}
  </>;
}

function ActivityBody({ event }: { event: Activity }) {
  const parts = [event.goal && { label: '目标', text: event.goal }, event.progress_summary && { label: '进展', text: event.progress_summary }, event.next_step && { label: '下一步', text: event.next_step }].filter(Boolean) as { label: string; text: string }[];
  if (!parts.length) return null;
  return <div className="activity-notes">{parts.map((part, index) => <p key={index}><small>{part.label}</small>{part.text}</p>)}</div>;
}

export default function Timeline({ events, hasOlder, loadingOlder, loadOlder, onTask }: { events: Activity[]; hasOlder: boolean; loadingOlder: boolean; loadOlder: () => Promise<void>; onTask: (id: string, session?: string) => void }) {
  const entries = useMemo(() => timelineEntries(events), [events]);
  const isMinor = (event: Activity) => !event.activity_kind && (event.type === 'observer.notice' || event.type === 'session.lifecycle' || event.type === 'command.output');
  return <section className="timeline" aria-label="工作过程">
    {hasOlder && <button className="load-history" disabled={loadingOlder} onClick={() => void loadOlder()}>{loadingOlder ? '正在加载…' : '加载较早记录'}</button>}
    {events.length >= 1200 && <p className="subtle-note">当前保留最近 1,200 个事件；完整历史仍在 Runtime 中。</p>}
    {!entries.length && <div className="empty-state"><ActivityIcon size={28}/><h2>工作发生时，这里会亮起来。</h2><p>连接的 GPT 在这个 Workspace 调用工具后，<br/>公开工作摘要、文件变更与执行结果会实时显示在这里。</p><span className="empty-footnote">真实任务 · 没有模拟进度</span></div>}
    {entries.map(event => {
      const isActivity = !!event.activity_kind;
      const minor = isMinor(event);
      const isToolCard = event.type === 'tool.started' || event.type === 'tool.completed';
      const title = isActivity ? activityLabels[event.activity_kind!] || event.activity_kind : event.type === 'file.changed' ? '文件变更' : event.tool === 'read' ? describeReadTitle(event.input) : toolTitle(event);
      const purpose = event.purpose || event.intent || '';
      const hasRaw = isToolCard && (!!event.input || !!event.output);
      const body = <ToolBody event={event} onTask={onTask}/>;
      return <article className={'timeline-entry ' + (isActivity ? 'agent-entry ' : '') + (minor ? 'minor-entry ' : '') + (event.status === 'failed' ? ' error-entry' : '')} key={event.sequence}>
        <div className="event-symbol">{isActivity ? <ActivityIcon size={16}/> : <EventIcon event={event}/>}</div>
        <div className="event-body">
          <header><span className="event-title">{title}</span><StatusPill status={event.status}/>{event.duration_ms !== undefined && !minor && <span className="event-duration">{duration(event.duration_ms)}</span>}<span className="event-time">{new Date(event.created_at).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' })}</span></header>
          {purpose && !minor && <p className="event-purpose" title={purpose}>{purpose}</p>}
          {!isActivity && !minor && body}
          {isActivity && <ActivityBody event={event}/>}
          {minor && <p className="event-summary">{event.summary}</p>}
          {hasRaw && <details className="event-details"><summary>原始事件数据</summary>{event.input !== undefined && <><small>INPUT</small><pre>{JSON.stringify(event.input, null, 2)}</pre></>}{event.output !== undefined && <><small>OUTPUT</small><pre>{JSON.stringify(event.output, null, 2)}</pre></>}{event.truncated && <p>大段数据已截断，完整结果可通过 observe 获取。</p>}</details>}
        </div>
      </article>;
    })}
  </section>;
}
