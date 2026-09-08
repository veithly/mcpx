import { ExternalLink, LayoutGrid, LoaderCircle } from 'lucide-react';
import type { Session, Workspace } from './model';
import { isWorking, sessionURL, sortSessions, workingLabel } from './sidebar-model';
export default function SessionStrip({workspace, sessions, selected, current, choose}:{workspace:Workspace;sessions:Session[];selected:string;current?:Session;choose:(workspace:string,session?:string)=>void}) {
  const members=sortSessions([...new Map([...sessions.filter(item=>item.workspace===workspace.name),...(current&&current.workspace===workspace.name?[current]:[])].map(item=>[item.id,item])).values()]);
  return <nav className="session-strip" aria-label={workspace.name+' 的会话'}>
    <button className={'session-overview '+(!selected?'active':'')} aria-current={!selected?'page':undefined} onClick={()=>choose(workspace.name,'')} title="查看项目全部会话的工作流"><LayoutGrid size={14}/>总览</button>
    <div className="session-strip-items">{members.map(item=><div className={'session-chip '+(selected===item.id?'active':'')} key={item.id}>
      <button className="session-chip-select" aria-current={selected===item.id?'page':undefined} title={(item.label||'未命名会话')+'\n'+item.id+'\n'+(isWorking(item)?workingLabel(item):'会话空闲')} onClick={()=>choose(workspace.name,item.id)}>
        {isWorking(item)?<LoaderCircle className="spin" size={12}/>:<span className={'status-dot '+(item.status==='closed'?'closed':'')}/>}
        <span>{item.label||'未命名会话'}</span><code>{item.id.slice(-6)}</code>
      </button>
      <a href={sessionURL(workspace.name,item.id)} target="_blank" rel="noopener noreferrer" title="在新标签页中并行查看" aria-label={'新标签页打开 '+(item.label||'会话')+' '+item.id.slice(-6)}><ExternalLink size={12}/></a>
    </div>)}{!members.length&&<span className="session-strip-empty">会话将在 GPT 开始使用此项目后出现</span>}</div>
  </nav>;
}
