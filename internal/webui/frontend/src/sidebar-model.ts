import type { Session, Workspace } from './model';
export interface SidebarTarget { kind: 'workspace' | 'session'; id: string; workspace: string; label: string; pinned: boolean; working: boolean; path?: string }
export interface SidebarMutation { kind: SidebarTarget['kind']; id: string; workspace: string; action: 'pin' | 'move' | 'delete'; pinned?: boolean; target_id?: string; placement?: 'before' | 'after'; confirm?: boolean; revision?: number }
const position = (value?: number) => value && value > 0 ? value : Number.MAX_SAFE_INTEGER;
export const isWorking = (session: Session): boolean => !!session.is_working || session.running_tasks > 0 || (session.running_calls || 0) > 0 || (session.running_operations || 0) > 0;
export function sortSessions(sessions: Session[]): Session[] {
  return [...sessions].sort((a,b) => Number(isWorking(b)) - Number(isWorking(a)) || Number(!!b.pinned)-Number(!!a.pinned) || position(a.position)-position(b.position) || b.last_active_at-a.last_active_at || a.id.localeCompare(b.id));
}
export function sortWorkspaces(workspaces: Workspace[]): Workspace[] {
  return [...workspaces].sort((a,b) => Number(!!b.pinned)-Number(!!a.pinned) || position(a.position)-position(b.position));
}
export function sameSortGroup(a: SidebarTarget,b: SidebarTarget): boolean {
  return a.kind===b.kind && a.pinned===b.pinned && (a.kind==='workspace' || (a.workspace===b.workspace && a.working===b.working));
}
export function canDrop(a: SidebarTarget,b: SidebarTarget): boolean { return a.id!==b.id && sameSortGroup(a,b); }
export function sessionTarget(session: Session): SidebarTarget { return { kind:'session',id:session.id,workspace:session.workspace,label:session.label||session.description||'未命名会话',pinned:!!session.pinned,working:isWorking(session),path:session.workspace_path }; }
export function workspaceTarget(workspace: Workspace): SidebarTarget { return { kind:'workspace',id:workspace.name,workspace:workspace.name,label:workspace.name,pinned:!!workspace.pinned,working:(workspace.working_sessions||0)>0,path:workspace.path }; }
export function workingLabel(session: Session): string {
  if(session.running_tasks>0)return `${session.running_tasks} 个命令执行中`;
  if((session.running_calls||0)>0)return '工具调用中';
  if((session.running_operations||0)>0)return '操作进行中';
  return '正在工作';
}
