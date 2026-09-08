import { useEffect, useLayoutEffect, useRef } from 'react';
import type { ReactNode } from 'react';
import { createPortal } from 'react-dom';

export interface MenuAction { id: string; label: string; icon?: ReactNode; disabled?: boolean; danger?: boolean; run: () => void }
export interface MenuAnchor { x: number; y: number; trigger: HTMLElement }
export default function ContextMenu({ anchor, label, actions, close }: { anchor: MenuAnchor; label: string; actions: MenuAction[]; close: () => void }) {
  const ref = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const menu=ref.current;if(!menu)return;
    const bounds=menu.getBoundingClientRect();
    menu.style.left=Math.max(8,Math.min(anchor.x,window.innerWidth-bounds.width-8))+'px';
    menu.style.top=Math.max(8,Math.min(anchor.y,window.innerHeight-bounds.height-8))+'px';
    menu.querySelector<HTMLButtonElement>('button:not(:disabled)')?.focus();
  },[anchor]);
  useEffect(()=>{
    const outside=(event:PointerEvent)=>{if(!ref.current?.contains(event.target as Node))close();};
    const dismiss=()=>close();
    window.addEventListener('pointerdown',outside,true);
    window.addEventListener('resize',dismiss);
    // A fixed menu must not become detached from a scrolling project row.
    const scroll=(event:Event)=>{if(!ref.current?.contains(event.target as Node))close();};
    window.addEventListener('scroll',scroll,true);
    return()=>{window.removeEventListener('pointerdown',outside,true);window.removeEventListener('resize',dismiss);window.removeEventListener('scroll',scroll,true);};
  },[close]);
  return createPortal(<div ref={ref} className="context-menu" role="menu" aria-label={label} onContextMenu={event=>event.preventDefault()} onKeyDown={event=>{
    const buttons=Array.from(ref.current?.querySelectorAll<HTMLButtonElement>('button:not(:disabled)')||[]);
    const index=buttons.indexOf(document.activeElement as HTMLButtonElement);
    if(event.key==='Escape'){event.preventDefault();close();anchor.trigger.focus();}
    else if(event.key==='Tab'){close();anchor.trigger.focus();}
    else if(['ArrowDown','ArrowUp','Home','End'].includes(event.key)){
      event.preventDefault();const next=event.key==='Home'?0:event.key==='End'?buttons.length-1:(index+(event.key==='ArrowDown'?1:-1)+buttons.length)%buttons.length;buttons[next]?.focus();
    }
  }}><div className="context-menu-label">{label}</div>{actions.map(action=><button key={action.id} role="menuitem" disabled={action.disabled} className={action.danger?'danger':''} onClick={()=>{close();anchor.trigger.focus();action.run();}}>{action.icon}<span>{action.label}</span></button>)}</div>,document.body);
}
