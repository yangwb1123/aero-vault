import {
  IrisAlert,
  IrisButton,
  IrisCard,
  IrisFileUpload,
  IrisInput,
} from '@iris-ui-kit/react'
import type { VaultClient } from '../../api/vault'
import { useFileUpload } from './useFileUpload'

export function UploadPanel({ client, initialPrefix, onUploaded }: {
  client: VaultClient
  initialPrefix: string
  onUploaded(): void
}): React.ReactElement {
  const upload = useFileUpload(client, initialPrefix, onUploaded)
  return (
    <IrisCard variant="outline" header="拖拽或选择文件上传">
      <div className="upload-panel">
        <label className="access-field"><span>目标 Key 前缀（可选）</span><IrisInput value={upload.prefix} placeholder="team/project/" disabled={upload.busy} onChange={(event) => upload.change({ prefix: event.target.value })} /></label>
        <IrisFileUpload multiple maxFiles={20} value={upload.files} disabled={upload.busy}
          label="点击选择或将文件拖到这里（最多 20 个）" onValueChange={(files) => upload.change({ files })}
          onReject={() => upload.change({ message: { tone: 'danger', text: '部分文件超过数量限制或不符合上传条件。' } })} />
        {upload.busy ? <p className="muted" role="status">{upload.stopping ? '正在停止上传' : '正在上传'}；已确认完成 {upload.completed}/{upload.total} 个文件。</p> : null}
        {upload.message ? <IrisAlert tone={upload.message.tone}>{upload.message.text}</IrisAlert> : null}
        <div className="access-actions">
          <IrisButton disabled={upload.files.length === 0 || upload.busy} loading={upload.busy} onClick={() => void upload.upload()}>上传 {upload.files.length || ''} 个文件</IrisButton>
          {upload.busy ? <IrisButton variant="outline" disabled={upload.stopping} onClick={upload.cancel}>取消上传</IrisButton> : null}
        </div>
      </div>
    </IrisCard>
  )
}
