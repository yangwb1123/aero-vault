import * as React from 'react'
import { IrisAlert, IrisButton, IrisInput, IrisSwitch } from '@iris-ui-kit/react'
import type { VaultClient, VaultObject } from '../api/vault'
import { PageError, PageHeader, PageLoading } from '../components/Page'
import { downloadBlob } from '../download'
import { useResource } from '../hooks/useResource'
import { BatchToolbar } from './files/BatchToolbar'
import { FileTable } from './files/FileTable'
import { FolderBrowser } from './files/FolderBrowser'
import { isDirectFile, normalizeFolderPath } from './files/folderPath'
import { UploadPanel } from './files/UploadPanel'

export function FilesPage({ client, onOpenObject }: { client: VaultClient; onOpenObject(key: string, deleted: boolean): void }): React.ReactElement {
  const [prefix, setPrefix] = React.useState('')
  const [activePrefix, setActivePrefix] = React.useState('')
  const [deleted, setDeleted] = React.useState(false)
  const [selected, setSelected] = React.useState<Set<string>>(new Set())
  const [busy, setBusy] = React.useState('')
  const [message, setMessage] = React.useState<{ tone: 'success' | 'danger'; text: string }>()
  const load = React.useCallback(async () => {
    const [files, folders] = await Promise.all([
      client.listFiles(activePrefix, deleted),
      deleted ? Promise.resolve(undefined) : client.listFolder(activePrefix),
    ])
    const objects = files.objects.filter((item) =>
      item.content_type !== 'application/x-directory' && (deleted || isDirectFile(item.key, activePrefix)))
    return {
      files: { ...files, objects },
      folders,
    }
  }, [activePrefix, client, deleted])
  const resource = useResource(load)

  React.useEffect(() => setSelected(new Set()), [activePrefix, deleted])

  const run = async (label: string, action: () => Promise<void>) => {
    setBusy(label); setMessage(undefined)
    try {
      await action()
      setMessage({ tone: 'success', text: '操作已完成。' })
    } catch (reason) {
      setMessage({ tone: 'danger', text: reason instanceof Error ? reason.message : '操作失败' })
    } finally {
      setBusy('')
      resource.reload()
    }
  }

  const navigate = (path: string) => {
    const normalized = normalizeFolderPath(path)
    setPrefix(normalized)
    setActivePrefix(normalized)
  }
  const select = (keys: string[], checked: boolean) => setSelected((current) => {
    const next = new Set(current)
    keys.forEach((key) => checked ? next.add(key) : next.delete(key))
    return next
  })
  const download = (item: VaultObject) => void run(`download:${item.key}`, async () =>
    downloadBlob(await client.download(item.key), item.key.split('/').pop() || 'download'))

  const batchDelete = async () => {
    const keys = [...selected]
    setBusy('batch-delete'); setMessage(undefined)
    try {
      const results = await client.batchDeleteFiles(keys)
      const failed = results.filter((result) => !result.deleted)
      setSelected(new Set(failed.map((result) => result.key)))
      setMessage(batchMessage(keys.length, failed.length, '删除'))
      resource.reload()
    } catch (reason) {
      setMessage({ tone: 'danger', text: reason instanceof Error ? reason.message : '批量删除失败' })
    } finally { setBusy('') }
  }

  const batchTag = async (tagKey: string, tagValue: string) => {
    const keys = [...selected]
    setBusy('batch-tag'); setMessage(undefined)
    try {
      const results = await client.batchTagFiles(keys, { [tagKey]: tagValue })
      const failed = results.filter((result) => result.error)
      setSelected(new Set(failed.map((result) => result.key)))
      setMessage(batchMessage(keys.length, failed.length, '设置标签'))
      resource.reload()
    } catch (reason) {
      setMessage({ tone: 'danger', text: reason instanceof Error ? reason.message : '批量标签失败' })
    } finally { setBusy('') }
  }

  return (
    <section>
      <PageHeader title="文件" description="浏览默认存储桶目录，批量管理对象，并从回收站恢复文件。" />
      {!deleted ? <div className="files-upload"><UploadPanel key={activePrefix} client={client} initialPrefix={activePrefix} onUploaded={resource.reload} /></div> : null}
      <form className="filter-bar" onSubmit={(event) => { event.preventDefault(); navigate(prefix) }}>
        <IrisInput value={prefix} placeholder="目录路径，例如 team/docs/" onChange={(event) => setPrefix(event.target.value)} />
        <label className="deleted-toggle"><IrisSwitch checked={deleted} onChange={setDeleted} /><span>回收站</span></label>
        <IrisButton type="submit" variant="outline">进入</IrisButton>
      </form>
      {message ? <IrisAlert tone={message.tone}>{message.text}</IrisAlert> : null}
      {resource.loading ? <PageLoading /> : null}
      {resource.error ? <PageError error={resource.error} retry={resource.reload} /> : null}
      {resource.data ? <div className="files-stack">
        {resource.data.folders ? <FolderBrowser listing={resource.data.folders} path={activePrefix} busy={Boolean(busy)} navigate={navigate} create={(path) => run(`folder-create:${path}`, () => client.createFolder(path))} remove={(path) => run(`folder-delete:${path}`, () => client.deleteFolder(path))} /> : null}
        {!deleted ? <BatchToolbar keys={[...selected]} busy={Boolean(busy)} clear={() => setSelected(new Set())} applyTags={batchTag} remove={batchDelete} /> : null}
        <FileTable page={resource.data.files} deleted={deleted} selected={selected} busy={Boolean(busy)} select={select} open={(key) => onOpenObject(key, deleted)} download={download} remove={(key) => void run(`delete:${key}`, () => client.deleteFile(key))} restore={(key) => void run(`restore:${key}`, () => client.restoreObject(key))} />
      </div> : null}
    </section>
  )
}

function batchMessage(total: number, failed: number, action: string): { tone: 'success' | 'danger'; text: string } {
  if (failed === 0) return { tone: 'success', text: `已为 ${total} 个对象完成${action}。` }
  return { tone: 'danger', text: `${action}完成，但有 ${failed}/${total} 个对象失败；失败项仍保持选中。` }
}
