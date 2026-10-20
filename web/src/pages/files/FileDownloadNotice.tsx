import { IrisAlert, IrisButton } from '@iris-ui-kit/react'
import type { useObjectDownload } from '../../hooks/useObjectDownload'

type DownloadView = Pick<ReturnType<typeof useObjectDownload>, 'pending' | 'stopping' | 'notice' | 'canCancel' | 'cancel'>

export function FileDownloadNotice({ transfer }: { transfer: DownloadView }): React.ReactElement {
  if (!transfer.pending && !transfer.notice) return <></>
  return <div className="access-stack">
    {transfer.pending ? <div className="access-actions">
      <span role="status">{transfer.stopping ? '正在停止下载准备' : transfer.canCancel ? '正在准备下载' : '正在交给浏览器处理'}：{transfer.pending.key}</span>
      {transfer.canCancel || transfer.stopping ? <IrisButton size="sm" variant="outline" disabled={transfer.stopping} onClick={transfer.cancel}>取消下载准备</IrisButton> : null}
    </div> : null}
    {transfer.notice ? <IrisAlert tone={transfer.notice.tone}>{transfer.notice.text}</IrisAlert> : null}
  </div>
}
