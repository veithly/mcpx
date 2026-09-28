import { useEffect, useMemo, useState } from 'react';
import { FileDiff, RefreshCw } from 'lucide-react';
import { api, message, query } from './api';
import { parseDiff } from './model';

interface ChangedFile { path: string; original_path?: string; status: string }
interface ChangeReport { git_available: boolean; entries: ChangedFile[]; unified_diff?: string; diff_truncated?: boolean }
interface Props { workspace: string; session: string }

const statusLabel: Record<string, string> = {
  untracked: '新建', added: '新增', modified: '修改', deleted: '删除',
  renamed: '改名', copied: '复制', unmerged: '冲突', ignored: '忽略',
};

export default function Changes({ workspace, session }: Props) {
  const [report, setReport] = useState<ChangeReport>();
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);
  const [revision, setRevision] = useState(0);
  useEffect(() => {
    setReport(undefined); setError('');
    if (!workspace) { setLoading(false); return; }
    let stopped = false;
    let running = false;
    const controller = new AbortController();
    const load = async () => {
      if (running) return;
      running = true;
      setLoading(true);
      try {
        const current = await api<ChangeReport>('changes?' + query(workspace, session), { signal: controller.signal });
        if (!stopped) { setReport(current); setError(''); }
      } catch (cause) { if (!stopped) setError(message(cause)); }
      finally { running = false; if (!stopped) setLoading(false); }
    };
    void load();
    const timer = setInterval(load, 10000);
    return () => { stopped = true; controller.abort(); clearInterval(timer); };
  }, [workspace, session, revision]);
  const allLines = useMemo(() => parseDiff(report?.unified_diff || ''), [report?.unified_diff]);
  const files = report?.entries || [];
  return <section className="changes-panel" aria-label="文件变更">
    <header className="changes-header"><div><h2>当前文件变更</h2><p>读取所选项目的 Git 工作区；命令创建的文件也会出现在这里。</p></div><button onClick={() => setRevision(value => value + 1)} disabled={loading || !workspace}><RefreshCw size={14}/>刷新</button></header>
    {!workspace && <p className="subtle-note">选择一个项目，查看当前文件变更。</p>}
    {workspace && loading && !report && <p className="subtle-note">正在读取工作区…</p>}
    {error && <p className="terminal-error" role="alert">{error}</p>}
    {report && !report.git_available && <p className="subtle-note">当前项目不是 Git 仓库。由 apply_patch 完成的变动仍会显示在工作流中。</p>}
    {report?.git_available && !files.length && <p className="subtle-note">当前 Git 工作区没有未提交的文件变更。</p>}
    {!!files.length && <div className="changes-files">{files.slice(0, 200).map(file => <div className="changes-file" key={file.path}>
      <span className={'changes-status changes-status-' + file.status}>{statusLabel[file.status] || file.status}</span>
      <code title={file.path}>{file.path}</code>{file.original_path && <small>原路径：{file.original_path}</small>}
    </div>)}{files.length > 200 && <p className="subtle-note">还有 {files.length - 200} 个文件，缩小项目范围后查看。</p>}</div>}
    {!!allLines.length && <div className="changes-diff"><div className="changes-diff-title"><FileDiff size={14}/>已跟踪文件的差异</div><pre className="diff-view">{allLines.slice(0, 400).map((line, index) => <span key={index} className={'diff-line ' + line.kind}>{line.text || ' '}</span>)}</pre>{(allLines.length > 400 || report?.diff_truncated) && <p className="subtle-note">这里只显示差异预览；完整内容请在项目中查看。</p>}</div>}
    {files.some(file => file.status === 'untracked') && <p className="subtle-note">未跟踪的新文件先显示文件名；apply_patch 的新建内容可在工作流中查看。</p>}
  </section>;
}
