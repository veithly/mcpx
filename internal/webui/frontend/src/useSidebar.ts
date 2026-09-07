import { useCallback, useEffect, useRef, useState } from 'react';
import { api, message } from './api';
import type { Snapshot } from './model';
import type { SidebarMutation } from './sidebar-model';
const empty: Snapshot = {workspaces:[],sessions:[],next_offset:0,version:'',sidebar_revision:0,total_sessions:0,removed_session_ids:[]};

export function useSidebar() {
  const [snapshot,setSnapshot]=useState<Snapshot>(empty);
  const [error,setError]=useState('');
  const [mutationError,setMutationError]=useState('');
  const [busy,setBusy]=useState(false);
  const [loadingMore,setLoadingMore]=useState(false);
  const [loaded,setLoaded]=useState(false);
  const current=useRef(snapshot); const limit=useRef(100); const generation=useRef(0);
  const controller=useRef<AbortController | undefined>(undefined);
  const changing=useRef(false); const mounted=useRef(true);
  const refresh=useCallback(async () => {
    controller.current?.abort(); const token=++generation.current;
    const abort=new AbortController();controller.current=abort;
    try {
      const value=await api<Snapshot>('state?limit='+limit.current,{signal:abort.signal});
      if(mounted.current && generation.current===token){current.current=value;setSnapshot(value);setLoaded(true);setError('');}
    }catch(cause){if(mounted.current && generation.current===token && !abort.signal.aborted)setError(message(cause));}
  },[]);
  useEffect(()=>{
    mounted.current=true;let stopped=false;let timer:ReturnType<typeof setTimeout>;
    const poll=async()=>{if(!changing.current)await refresh();if(!stopped)timer=setTimeout(poll,2000);};void poll();
    return()=>{stopped=true;mounted.current=false;clearTimeout(timer);controller.current?.abort();++generation.current;};
  },[refresh]);
  const mutate=useCallback(async(input:SidebarMutation):Promise<boolean>=>{
    if(changing.current)return false;
    changing.current=true;setBusy(true);setMutationError('');controller.current?.abort();++generation.current;
    let succeeded=false;
    try {
      await api('sidebar',{method:'POST',body:JSON.stringify({...input,revision:input.revision ?? current.current.sidebar_revision})});succeeded=true;
    }catch(cause){if(mounted.current)setMutationError(message(cause));}
    finally {await refresh();changing.current=false;if(mounted.current)setBusy(false);}
    return succeeded;
  },[refresh]);
  const loadMore=async()=>{if(changing.current||loadingMore||limit.current>=3000)return;limit.current=Math.min(3000,limit.current+100);setLoadingMore(true);await refresh();if(mounted.current)setLoadingMore(false);};
  return {snapshot,loaded,error:mutationError||error,busy,refresh,mutate,loadMore,loadingMore};
}
