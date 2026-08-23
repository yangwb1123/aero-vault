import * as React from 'react'
import { IrisAlert, IrisButton, IrisCard, IrisInput, IrisSwitch } from '@iris-ui-kit/react'
import type { VaultClient, VaultObject } from '../api/vault'
import { PageError, PageHeader, PageLoading } from '../components/Page'
import { downloadBlob } from '../download'
import { useResource } from '../hooks/useResource'
import { UploadPanel } from './files/UploadPanel'

const formatBytes = (value: number): string =>
  new Intl.NumberFormat('zh-CN', { notation: 'compact', style: 'unit', unit: 'byte' }).format(value)

export function FilesPage({ client, onOpenObject }: { client: VaultClient; onOpenObject(key: string, deleted: boolean): void }): React.ReactElement {
  const [prefix, setPrefix] = React.useState('')
  const [activePrefix, setActivePrefix] = React.useState('')
  const [deleted, setDeleted] = React.useState(false)
  const [busy, setBusy] = React.useState('')
  const [message, setMessage] = React.useState<{ tone: 'success' | 'danger'; text: string }>()
  const load = React.useCallback(() => client.listFiles(activePrefix, deleted), [activePrefix, client, deleted])
  const resource = useResource(load)

  const run = async (label: string, action: () => Promise<void>) => {
    setBusy(label)
    setMessage(undefined)
    try {
      await action()
      setMessage({ tone: 'success', text: '操作已完成。' })
      resource.reload()
    } catch (reason) {
      setMessage({
        tone: 'danger',
        text: reason instanceof Error ? reason.message : '操作失败',
      })
    } finally {
      setBusy('')
    }
  }

  const download = (item: VaultObject) =>
    run(`download:${item.key}`, async () => downloadBlob(await client.download(item.key), item.key.split('/').pop() || 'download'))

  return (
    <section>
      <PageHeader
        title="文件"
        description="浏览、上传、下载、管理和恢复默认存储桶中的对象。"
      />
      {!deleted ? <div className="files-upload"><UploadPanel key={activePrefix} client={client} initialPrefix={activePrefix} onUploaded={resource.reload} /></div> : null}
      <form
        className="filter-bar"
        onSubmit={(event) => {
          event.preventDefault()
          setActivePrefix(prefix.trim())
        }}
      >
        <IrisInput value={prefix} placeholder="按 key 前缀筛选" onChange={(event) => setPrefix(event.target.value)} />
        <label className="deleted-toggle"><IrisSwitch checked={deleted} onChange={setDeleted} /><span>回收站</span></label>
        <IrisButton type="submit" variant="outline">筛选</IrisButton>
      </form>
      {message ? <IrisAlert tone={message.tone}>{message.text}</IrisAlert> : null}
      {resource.loading ? <PageLoading /> : null}
      {resource.error ? <PageError error={resource.error} retry={resource.reload} /> : null}
      {resource.data ? (
        <IrisCard variant="outline">
          <div className="table-scroll">
            <table className="vault-table">
              <thead><tr><th>Key</th><th>大小</th><th>类型</th><th>更新时间</th><th>操作</th></tr></thead>
              <tbody>
                {resource.data.objects.map((item) => (
                  <tr key={`${item.bucket}:${item.key}`}>
                    <td><strong>{item.key}</strong><div className="muted">{item.bucket}</div></td>
                    <td>{formatBytes(item.size)}</td>
                    <td>{item.content_type || 'application/octet-stream'}</td>
                    <td>{new Date(item.updated_at).toLocaleString('zh-CN')}</td>
                    <td className="row-actions">
                      <IrisButton size="sm" variant="ghost" disabled={Boolean(busy)} onClick={() => onOpenObject(item.key, deleted)}>详情</IrisButton>
                      {deleted ? (
                        <IrisButton size="sm" variant="ghost" disabled={Boolean(busy)} onClick={() => void run(`restore:${item.key}`, () => client.restoreObject(item.key))}>恢复</IrisButton>
                      ) : (
                        <>
                          <IrisButton size="sm" variant="ghost" disabled={Boolean(busy)} onClick={() => void download(item)}>下载</IrisButton>
                          <IrisButton size="sm" variant="ghost" disabled={Boolean(busy)} onClick={() => {
                            if (window.confirm(`软删除 ${item.key}？`)) void run(`delete:${item.key}`, () => client.deleteFile(item.key))
                          }}>删除</IrisButton>
                        </>
                      )}
                    </td>
                  </tr>
                ))}
                {resource.data.objects.length === 0 ? <tr><td colSpan={5} className="empty-cell">没有匹配文件</td></tr> : null}
              </tbody>
            </table>
          </div>
          {resource.data.has_more ? <div className="table-note">当前仅显示前 200 项，请使用前缀缩小范围。</div> : null}
        </IrisCard>
      ) : null}
    </section>
  )
}
