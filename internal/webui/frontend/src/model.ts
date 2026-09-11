export type AccessMode = 'approval' | 'full_access';
export interface Workspace { name: string; path: string; description: string; access_mode: AccessMode; pinned?: boolean; position?: number; working_sessions?: number; session_count?: number; active_sessions?: number; last_command_at?: number; is_active?: boolean; preferred_session_id?: string }
export interface Session { id: string; workspace: string; workspace_path?: string; label: string; description: string; status: string; last_active_at: number; running_tasks: number; running_calls?: number; running_operations?: number; last_command_at?: number; recent_command?: boolean; is_working?: boolean; pinned?: boolean; position?: number }
export interface Snapshot { workspaces: Workspace[]; sessions: Session[]; next_offset: number; version: string; sidebar_revision: number; total_sessions: number; removed_session_ids: string[]; server_time?: number }
export interface Auth { authenticated: boolean; csrf?: string; auth_mode?: string; local?: boolean }
export interface Task { execution_task_id: string; command: string; status: string; runtime_ms: number; exit_code?: number; log_truncated: boolean }
export interface UserRequest { id: string; body: string; kind: string; status: string; created_at: number; delivered_at?: number; acknowledged_at?: number; remote_session_id?: string; replayed?: boolean }
export interface Approval { id: string; tool: string; summary: string; command: string; scope: string; decision: string; can_decide: boolean }
export interface Detail { tasks: Task[]; requests: UserRequest[]; approvals: Approval[]; access_mode: AccessMode; session_info?: Session }
export interface ActivityOutput { stream: string; text: string }
export interface Activity {
  sequence: number; type: string; workspace: string;
  remote_session_id?: string; call_id?: string; request_id?: string;
  tool?: string; status?: string; phase?: string;
  summary?: string; intent?: string; purpose?: string; goal?: string;
  progress_summary?: string; next_step?: string;
  activity_kind?: string; command?: string; working_directory?: string;
  exit_code?: number | null; duration_ms?: number; truncated?: boolean;
  input?: unknown; output?: unknown;
  execution_task_id?: string; path?: string; stream?: string;
  mcp_server?: string; mcp_tool?: string; skill_name?: string;
  created_at: string;
  // Attached by timelineEntries from same-call child events:
  outputs?: ActivityOutput[];
  diffs?: string[];
  changed_paths?: string[];
}
export interface HistoryPage { events: Activity[]; last_sequence: number; next_cursor: string }
export interface DiffLine { kind: 'add' | 'del' | 'hunk' | 'meta' | 'ctx'; text: string }
export const emptyDetail: Detail = { tasks: [], requests: [], approvals: [], access_mode: 'approval' };
export const requestStatus: Record<string, string> = { cancelled: '项目或会话已删除，请求已取消', queued: '等待 GPT 调用', delivered: '已附加到工具响应', acknowledged: 'GPT 已回执' };
export const statusText: Record<string, string> = { started: '进行中', running: '执行中', exited: '已退出', killed: '已停止', succeeded: '已完成', failed: '失败', waiting_confirmation: '等待审批', active: '会话就绪', closed: '已关闭', interrupted: '已中断', accepted: '已接受', cancelled: '已取消' };
export const toolTitles: Record<string, string> = {
  execute: '执行命令', edit: '编辑文件', read: '读取文件', session: '会话', workspace: '选择项目',
  observe: '查看进度', plan: '计划', progress: '进度', artifact: '产物', move_out: '迁移产物',
  skill_tool: '技能', operation_batch: '批量操作', operation_manage: '操作管理', secret_provide: '提供密钥',
  screenshot_capture: '截屏', environment: '环境配置', environment_read: '读取环境', runtime_read: '读取运行时', mcp_tool: '上游工具',
};
export function toolTitle(event: Pick<Activity, 'tool' | 'mcp_server' | 'mcp_tool'>): string {
  if (event.tool === 'mcp_tool' && (event.mcp_server || event.mcp_tool)) {
    return event.mcp_tool ? `${event.mcp_server || 'mcp'} · ${event.mcp_tool}` : event.mcp_server || 'mcp';
  }
  return toolTitles[event.tool ?? ''] || event.tool || 'Runtime';
}
export function mergeEvents(before: Activity[], incoming: Activity[], limit = 1200): Activity[] {
  return [...new Map([...before, ...incoming].map(event => [event.sequence, event])).values()].sort((a,b) => a.sequence-b.sequence).slice(-limit);
}
function appendOutput(map: Map<string, ActivityOutput[]>, callId: string, event: Activity) {
  const list = map.get(callId) ?? [];
  const text = typeof (event.output as { text?: unknown } | null)?.text === 'string' ? (event.output as { text: string }).text : '';
  const last = list[list.length - 1];
  if (last && last.stream === (event.stream || 'stdout')) last.text += text;
  else list.push({ stream: event.stream || 'stdout', text });
  map.set(callId, list);
}
function diffTextsOf(event: Activity): { diffs: string[]; paths: string[] } {
  const out = event.output as { diff_summary?: unknown; paths?: unknown; results?: unknown } | null;
  const diffs: string[] = []; const paths: string[] = [];
  if (typeof out?.diff_summary === 'string' && out.diff_summary) diffs.push(out.diff_summary);
  if (Array.isArray(out?.results)) for (const item of out.results as { diff?: unknown }[]) {
    if (typeof item?.diff === 'string' && item.diff && !diffs.includes(item.diff)) diffs.push(item.diff);
  }
  if (Array.isArray(out?.paths)) for (const p of out.paths as unknown[]) if (typeof p === 'string') paths.push(p);
  if (event.path && !paths.includes(event.path)) paths.unshift(event.path);
  return { diffs, paths };
}
export function timelineEntries(events: Activity[]): Activity[] {
  const outputs = new Map<string, ActivityOutput[]>();
  const changes = new Map<string, { diffs: string[]; paths: string[] }>();
  const cardCalls = new Set<string>();
  for (const event of events) {
    if (event.call_id && (event.type === 'tool.started' || event.type === 'tool.completed')) cardCalls.add(event.call_id);
    else if (event.type === 'command.output' && event.call_id && cardCalls.has(event.call_id)) appendOutput(outputs, event.call_id, event);
    else if (event.type === 'file.changed' && event.call_id) {
      const { diffs, paths } = diffTextsOf(event);
      const existing = changes.get(event.call_id) ?? { diffs: [], paths: [] };
      for (const diff of diffs) if (!existing.diffs.includes(diff)) existing.diffs.push(diff);
      for (const p of paths) if (!existing.paths.includes(p)) existing.paths.push(p);
      changes.set(event.call_id, existing);
    }
  }
  const byCall = new Map<string, number>(); const out: Activity[] = [];
  let orphanTask = ''; let orphanRow = -1;
  for (const event of events) {
    if (event.type === 'command.output') {
      if (event.call_id && cardCalls.has(event.call_id)) continue;
      // Chunks whose parent card is outside the loaded window group into one
      // row per contiguous run so a chatty long task does not flood the
      // timeline with one row per chunk.
      const key = event.execution_task_id || event.call_id || '';
      const text = typeof (event.output as { text?: unknown } | null)?.text === 'string' ? (event.output as { text: string }).text : '';
      if (key && key === orphanTask && orphanRow >= 0) {
        const row = out[orphanRow];
        const rowOutputs = [...(row.outputs ?? [])];
        const last = rowOutputs[rowOutputs.length - 1];
        if (last && last.stream === (event.stream || 'stdout')) rowOutputs[rowOutputs.length - 1] = { ...last, text: last.text + text };
        else rowOutputs.push({ stream: event.stream || 'stdout', text });
        out[orphanRow] = { ...row, outputs: rowOutputs };
        continue;
      }
      orphanTask = key;
      orphanRow = out.length;
      out.push({ ...event, outputs: [{ stream: event.stream || 'stdout', text }] });
      continue;
    }
    orphanTask = ''; orphanRow = -1;
    if (event.type === 'file.changed') {
      if (!event.call_id || !cardCalls.has(event.call_id)) out.push({ ...event, ...changes.get(event.call_id ?? '') });
      continue;
    }
    const key = event.call_id || (event.request_id ? `${event.request_id}:${event.tool}` : '');
    if (key && (event.type === 'tool.started' || event.type === 'tool.completed')) {
      const index = byCall.get(key);
      if (index !== undefined) { out[index] = { ...out[index], ...event }; continue; }
      byCall.set(key, out.length);
    }
    out.push(event);
  }
  for (let index = 0; index < out.length; index++) {
    const key = out[index].call_id;
    if (!key) continue;
    const merged = outputs.get(key);
    const changed = changes.get(key);
    if (merged?.length) out[index] = { ...out[index], outputs: merged };
    if (changed?.diffs.length) out[index] = { ...out[index], diffs: changed.diffs, changed_paths: changed.paths };
  }
  return out;
}
export function plainTerminal(value: string): string {
  return value.replace(/\x1b\][^\x07]*(?:\x07|\x1b\\)/g, '').replace(/\x1b\[[0-?]*[ -/]*[@-~]/g, '').replace(/\r(?!\n)/g, '\n').replace(/[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]/g, '');
}
export function summaryOf(event: Activity): string {
  const nested = (event.output as { summary?: unknown } | null)?.summary;
  if (typeof nested === 'string' && nested) return nested;
  return event.summary || '';
}
const contextPreamble = /^Context:\n(?:- [^\n]*\n)+\n?/;
export function stripContext(text: string): string { return text.replace(contextPreamble, ''); }
export interface ParsedStreams { note: string; stdout?: string; stderr?: string }
export function parseCommandSummary(text: string): ParsedStreams {
  const note: string[] = []; const streams: ParsedStreams = { note: '' };
  let current: 'stdout' | 'stderr' | null = null; const stdout: string[] = []; const stderr: string[] = [];
  for (const line of stripContext(text).split('\n')) {
    if (line === 'stdout:' || line === 'stderr:') { current = line === 'stdout:' ? 'stdout' : 'stderr'; continue; }
    if (current === 'stdout') stdout.push(line);
    else if (current === 'stderr') stderr.push(line);
    else note.push(line);
  }
  streams.note = note.join('\n').trim();
  if (current !== null) {
    if (stdout.length) streams.stdout = stdout.join('\n');
    if (stderr.length) streams.stderr = stderr.join('\n');
  } else streams.note = note.join('\n').trim();
  return streams;
}
export function extractDiffBlocks(text: string): string[] {
  const blocks: string[] = [];
  for (const match of text.matchAll(/```diff\n([\s\S]*?)```/g)) if (match[1].trim()) blocks.push(match[1].replace(/\n$/, ''));
  return blocks;
}
export function parseDiff(diff: string): DiffLine[] {
  return diff.split('\n').filter((line, index, all) => line !== '' || index < all.length - 1).map(line => {
    if (line.startsWith('@@')) return { kind: 'hunk', text: line };
    if (line.startsWith('+++') || line.startsWith('---') || line.startsWith('diff ') || line.startsWith('index ')) return { kind: 'meta', text: line };
    if (line.startsWith('+')) return { kind: 'add', text: line };
    if (line.startsWith('-')) return { kind: 'del', text: line };
    return { kind: 'ctx', text: line };
  });
}
export function countDiffChanges(diffs: string[]): { added: number; removed: number } {
  let added = 0; let removed = 0;
  for (const diff of diffs) for (const line of parseDiff(diff)) {
    if (line.kind === 'add') added++;
    else if (line.kind === 'del') removed++;
  }
  return { added, removed };
}
export function describeEdits(input: unknown): string[] {
  const edits = (input as { edits?: unknown } | null)?.edits;
  if (!Array.isArray(edits)) return [];
  return edits.map((item: { path?: unknown; operation?: unknown; replacements?: unknown; content?: unknown; new_path?: unknown }) => {
    const path = typeof item?.path === 'string' ? item.path : '?';
    const operation = typeof item?.operation === 'string' ? item.operation : 'update';
    const opLabel = ({ create: '新建', update: '修改', delete: '删除', rename: `重命名为 ${String(item?.new_path ?? '')}` } as Record<string, string>)[operation] ?? operation;
    const extra = Array.isArray(item?.replacements) ? ` · ${item.replacements.length} 处替换` : (typeof item?.content === 'string' ? ' · 写入内容' : '');
    return `${path} · ${opLabel}${extra}`;
  });
}
export function argsRows(input: unknown): [string, string][] {
  const args = (input as { arguments?: unknown } | null)?.arguments;
  if (args === null || args === undefined || typeof args !== 'object') return [];
  return Object.entries(args as Record<string, unknown>).map(([key, value]) => [key, typeof value === 'string' ? value : JSON.stringify(value)]);
}
export function duration(ms: number): string { if(ms<1000)return `${Math.max(0,ms)}ms`; if(ms<60000)return `${(ms/1000).toFixed(1)}s`; return `${Math.floor(ms/60000)}m ${Math.floor(ms%60000/1000)}s`; }
export function age(timestamp: number): string { const seconds=Math.max(0,(Date.now()-timestamp)/1000);if(seconds<60)return '刚刚';if(seconds<3600)return `${Math.floor(seconds/60)} 分钟前`;if(seconds<86400)return `${Math.floor(seconds/3600)} 小时前`;return new Date(timestamp).toLocaleDateString('zh-CN',{month:'short',day:'numeric'}); }
