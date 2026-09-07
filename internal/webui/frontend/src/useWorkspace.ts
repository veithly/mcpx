import { useCallback, useEffect, useRef, useState } from 'react';
import { API, api, message, query } from './api';
import { emptyDetail, mergeEvents } from './model';
import type { Activity, Detail, HistoryPage } from './model';

export function useWorkspace(workspace: string, session: string) {
  const [events, setEvents] = useState<Activity[]>([]);
  const [detail, setDetail] = useState<Detail>(emptyDetail);
  const [connection, setConnection] = useState('connecting');
  const [error, setError] = useState('');
  const [cursor, setCursor] = useState('');
  const [loadingOlder, setLoadingOlder] = useState(false);
  const [revision, setRevision] = useState(0);
  const generation = useRef(0);
  const refresh = useCallback(() => setRevision(value => value + 1), []);

  useEffect(() => {
    const current = ++generation.current;
    const controller = new AbortController();
    let source: EventSource | undefined;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let batchTimer: ReturnType<typeof setTimeout> | undefined;
    let buffer: Activity[] = [];
    let stopped = false;
    setEvents([]); setDetail(emptyDetail); setCursor(''); setError('');
    setConnection(workspace ? 'connecting' : 'idle');
    if (!workspace) return () => controller.abort();
    const params = query(workspace, session);
    const flush = () => {
      if (stopped) return;
      const next = buffer; buffer = [];
      setEvents(previous => mergeEvents(previous, next));
      batchTimer = undefined;
    };
    const poll = async () => {
      try {
        const value = await api<Detail>('detail?' + params, { signal: controller.signal });
        if (!stopped) { setDetail(value); setError(''); }
      } catch (cause) { if (!stopped) setError(message(cause)); }
      if (!stopped) timer = setTimeout(poll, 2000);
    };
    void poll();
    void (async () => {
      try {
        const page = await api<HistoryPage>('events?' + params, { signal: controller.signal });
        if (stopped) return;
        setEvents(mergeEvents([], page.events)); setCursor(page.next_cursor);
        source = new EventSource(API + 'stream?' + params + '&after=' + page.last_sequence);
        source.addEventListener('ready', () => { if (!stopped) setConnection('live'); });
        source.addEventListener('activity', event => {
          if (stopped) return;
          try {
            const value = JSON.parse((event as MessageEvent<string>).data) as Activity;
            buffer.push(value);
            if (!batchTimer) batchTimer = setTimeout(flush, 120);
          } catch { setError('收到无法识别的事件；请重新连接。'); }
        });
        source.addEventListener('expired', () => window.dispatchEvent(new Event('mcpx:unauthorized')));
        source.onerror = () => { if (!stopped) setConnection('reconnecting'); };
        source.onopen = () => { if (!stopped) setConnection('live'); };
      } catch (cause) {
        if (!stopped && generation.current === current) { setError(message(cause)); setConnection('offline'); }
      }
    })();
    return () => {
      stopped = true; controller.abort(); source?.close();
      clearTimeout(timer); clearTimeout(batchTimer);
    };
  }, [workspace, session, revision]);

  const loadOlder = async () => {
    if (!cursor || loadingOlder || events.length >= 1200) return;
    const current = generation.current;
    setLoadingOlder(true);
    try {
      const page = await api<HistoryPage>('events?' + query(workspace, session) + '&cursor=' + encodeURIComponent(cursor));
      if (current === generation.current) {
        setEvents(previous => mergeEvents(previous, page.events)); setCursor(page.next_cursor);
      }
    } catch (cause) { if (current === generation.current) setError(message(cause)); }
    finally { setLoadingOlder(false); }
  };
  return { events, detail, connection, error, refresh, loadOlder, loadingOlder, hasOlder: !!cursor && events.length < 1200 };
}
