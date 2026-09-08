import { useEffect, useState } from 'react';
import { ExternalLink, ShieldCheck } from 'lucide-react';
import { Modal } from './Dialogs';
import { api, message } from './api';
interface NativeInfo { platform:string; executable:string; picker_available:boolean; signing:string; permission_status:string }
export default function NativePermissions({close}:{close:()=>void}) {
  const [info,setInfo]=useState<NativeInfo>();
  const [error,setError]=useState('');const [busy,setBusy]=useState(false);const [notice,setNotice]=useState('');
  useEffect(()=>{const controller=new AbortController();void api<NativeInfo>('native',{signal:controller.signal}).then(setInfo).catch(cause=>{if(!controller.signal.aborted)setError(message(cause));});return()=>controller.abort();},[]);
  const open=async(pane:string)=>{setBusy(true);setError('');try{await api('native/privacy',{method:'POST',body:JSON.stringify({pane,confirm:true})});setNotice('已打开运行 MCPX 的电脑上的系统设置。授权状态由 macOS 管理，请在那里确认。');}catch(cause){setError(message(cause));}finally{setBusy(false);}};
  return <Modal title="系统权限" close={close}>
    <div className="modal-intro"><ShieldCheck size={24}/><p>系统隐私权限与项目的 MCPX 执行审批分开管理。此面板不会定时扫描受保护目录，也不会自动重复申请权限。</p></div>
    {info&&<><span className="field-help">当前运行程序</span><code className="remove-path">{info.executable}</code>
      {info.platform==='darwin'?<>
        {info.signing==='adhoc'&&<div className="access-warning"><strong>当前程序使用临时签名</strong><p>重建后程序身份可能改变，macOS 可能再次询问。更新时应使用同一签名证书及固定安装路径；不能把页面上的“已设置”当成系统已授权。</p></div>}
        <div className="permission-options">{[{pane:'disk',title:'完全磁盘访问',hint:'需要跨项目访问受保护文件时，在系统设置中添加上面的程序。'},{pane:'files',title:'文件与文件夹',hint:'检查文稿、桌面、下载等目录的访问许可。'},{pane:'accessibility',title:'辅助功能',hint:'仅在使用桌面交互自动化时需要。'},{pane:'screen',title:'屏幕录制',hint:'仅在使用截屏或屏幕读取时需要。'}].map(item=><button className="permission-row" key={item.pane} disabled={busy} onClick={()=>void open(item.pane)}><span><strong>{item.title}</strong><small>{item.hint}</small></span><ExternalLink size={16}/></button>)}</div>
        <p className="field-help">授权由 macOS 保存。首次授权或系统要求重启时，需要在系统界面完成；MCPX 不修改隐私数据库，也不自动点击系统批准。</p>
      </>:<p className="modal-description">当前平台：{info.platform}。系统权限请在主机操作系统设置中管理；项目执行授权在“项目设置”中保存。</p>}
    </>}
    {!info&&!error&&<p className="field-help">正在读取程序身份…</p>}
    {notice&&<p className="success-message" role="status">{notice}</p>}{error&&<p className="form-error" role="alert">{error}</p>}
    <footer className="modal-actions"><button className="primary" onClick={close}>关闭</button></footer>
  </Modal>;
}
