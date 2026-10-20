import * as React from 'react'
import { IrisAlert, IrisButton, IrisInput, IrisSwitch } from '@iris-ui-kit/react'
import { listNextFilePage } from '../api/filePagination'
import type { FileListView, FolderListing } from '../api/files'
import type { FilePage, VaultClient, VaultObject } from '../api/vault'
import { PageError, PageHeader, PageLoading } from '../components/Page'
import { useObjectDownload } from '../hooks/useObjectDownload'
import { useFolderPagination } from '../hooks/useFolderPagination'
import { useResource } from '../hooks/useResource'
import { BatchToolbar } from './files/BatchToolbar'
import { createFileCommands } from './files/fileCommands'
import { FileDownloadNotice } from './files/FileDownloadNotice'
import { FileSharePanel } from './files/FileSharePanel'
import { FileTable } from './files/FileTable'
import { FolderBrowser } from './files/FolderBrowser'
import { isDirectFile, normalizeFolderPath } from './files/folderPath'
import { RecycleToolbar } from './files/RecycleToolbar'
import { restoreSelection, restoreSelectionMessage } from './files/restoreSelection'
import { UploadPanel } from './files/UploadPanel'

interface FileContinuation {
  client: VaultClient
  scopeKey: string
  objects: VaultObject[]
  nextMarker?: string
  hasMore: boolean
}

interface FilesResourceData {
  client: VaultClient
  scopeKey: string
  files: FilePage
  folders?: FolderListing
}

export function FilesPage({ client, onOpenObject }: { client: VaultClient; onOpenObject(key: string, deleted: boolean): void }): React.ReactElement {
  const commands = React.useMemo(() => createFileCommands(client), [client])
  const [prefix, setPrefix] = React.useState('')
  const [activePrefix, setActivePrefix] = React.useState('')
  const [deleted, setDeleted] = React.useState(false)
  const [view, setView] = React.useState<FileListView>('all')
  const [selected, setSelected] = React.useState<Set<string>>(new Set())
  const [shareTarget, setShareTarget] = React.useState<{ client: VaultClient; scopeKey: string; object: VaultObject }>()
  const [actionBusy, setBusy] = React.useState('')
  const [message, setMessage] = React.useState<{ tone: 'success' | 'danger'; text: string }>()
  const scopeKey = JSON.stringify([activePrefix, deleted, view])
  const fileDownload = useObjectDownload(client, scopeKey)
  const busy = actionBusy || (fileDownload.pending ? 'download' : '')
  const [continuation, setContinuation] = React.useState<FileContinuation>()
  const [loadingMore, setLoadingMore] = React.useState(false)
  const loadingMoreRef = React.useRef(false)
  const fileScopeRef = React.useRef({ client, scopeKey })
  const fileRequestRef = React.useRef(0)
  const actionRequestRef = React.useRef(0)
  const restoreBusyRef = React.useRef(false)
  const load = React.useCallback(async () => {
    const [files, folders] = await Promise.all([
      view === 'all' ? client.listFiles(activePrefix, deleted) : client.listFiles(activePrefix, deleted, '', view),
      deleted ? Promise.resolve(undefined) : view === 'all' ? client.listFolder(activePrefix) : client.listFolder(activePrefix, view),
    ])
    return { client, scopeKey, files: filterFilePage(files, activePrefix, deleted), folders }
  }, [activePrefix, client, deleted, scopeKey, view])
  const resource = useResource<FilesResourceData>(load)
  const currentData = resource.data?.client === client && resource.data.scopeKey === scopeKey
    ? resource.data
    : undefined
  const folderPaging = useFolderPagination(client, activePrefix, view, currentData?.folders)
  const scopedContinuation = continuation?.client === client && continuation.scopeKey === scopeKey
    ? continuation
    : undefined
  const displayedPage = currentData ? {
    ...currentData.files,
    objects: [...currentData.files.objects, ...(scopedContinuation?.objects ?? [])],
    next_marker: scopedContinuation ? scopedContinuation.nextMarker : currentData.files.next_marker,
    has_more: scopedContinuation?.hasMore ?? currentData.files.has_more,
  } : undefined

  React.useLayoutEffect(() => {
    if (fileScopeRef.current.client === client && fileScopeRef.current.scopeKey === scopeKey) return
    fileScopeRef.current = { client, scopeKey }
    actionRequestRef.current += 1
    fileRequestRef.current += 1
    loadingMoreRef.current = false
    restoreBusyRef.current = false
    setContinuation(undefined)
    setLoadingMore(false)
    setSelected(new Set())
    setShareTarget(undefined)
    setBusy('')
    setMessage(undefined)
  }, [client, scopeKey])
  React.useEffect(() => () => {
    fileRequestRef.current += 1
    actionRequestRef.current += 1
  }, [])

  const beginAction = () => {
    const requestID = ++actionRequestRef.current
    const target = { client, scopeKey }
    return () => actionRequestRef.current === requestID &&
      fileScopeRef.current.client === target.client && fileScopeRef.current.scopeKey === target.scopeKey
  }

  const reloadFiles = () => {
    fileRequestRef.current += 1
    loadingMoreRef.current = false
    setContinuation(undefined)
    setLoadingMore(false)
    setShareTarget(undefined)
    resource.reload()
  }

  const loadMore = async () => {
    const page = displayedPage
    if (!page?.has_more || !page.next_marker || loadingMoreRef.current || busy || resource.loading) return
    const target = { client, scopeKey, activePrefix, deleted, view }
    const requestID = ++fileRequestRef.current
    const isCurrent = () => fileRequestRef.current === requestID &&
      fileScopeRef.current.client === target.client && fileScopeRef.current.scopeKey === target.scopeKey
    loadingMoreRef.current = true
    setLoadingMore(true)
    setMessage(undefined)
    try {
      const nextPage = await listNextFilePage(target.client, target.activePrefix, target.deleted, page.next_marker, target.view)
      if (!isCurrent()) return
      const filteredPage = filterFilePage(nextPage, target.activePrefix, target.deleted)
      setContinuation((current) => {
        const previous = current?.client === target.client && current.scopeKey === target.scopeKey ? current : undefined
        return {
          client: target.client,
          scopeKey: target.scopeKey,
          objects: [...(previous?.objects ?? []), ...filteredPage.objects],
          nextMarker: filteredPage.next_marker,
          hasMore: filteredPage.has_more,
        }
      })
    } catch (reason) {
      if (isCurrent()) setMessage({ tone: 'danger', text: reason instanceof Error ? reason.message : '加载更多文件失败' })
    } finally {
      if (isCurrent()) {
        loadingMoreRef.current = false
        setLoadingMore(false)
      }
    }
  }

  const run = async (label: string, action: () => Promise<void>) => {
    const isCurrent = beginAction()
    setBusy(label); setMessage(undefined)
    try {
      await action()
      if (isCurrent()) setMessage({ tone: 'success', text: '操作已完成。' })
    } catch (reason) {
      if (isCurrent()) setMessage({ tone: 'danger', text: reason instanceof Error ? reason.message : '操作失败' })
    } finally {
      if (isCurrent()) {
        setBusy('')
        reloadFiles()
      }
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
  const download = (item: VaultObject) => {
    if (deleted || actionBusy || resource.loading) return
    setMessage(undefined)
    void fileDownload.download({ key: item.key })
  }

  const batchDelete = async () => {
    const keys = [...selected]
    const isCurrent = beginAction()
    setBusy('batch-delete'); setMessage(undefined)
    try {
      const results = await client.batchDeleteFiles(keys)
      if (!isCurrent()) return
      const failed = results.filter((result) => !result.deleted)
      setSelected(new Set(failed.map((result) => result.key)))
      setMessage(batchMessage(keys.length, failed.length, '删除'))
      reloadFiles()
    } catch (reason) {
      if (isCurrent()) setMessage({ tone: 'danger', text: reason instanceof Error ? reason.message : '批量删除失败' })
    } finally {
      if (isCurrent()) setBusy('')
    }
  }

  const batchRestore = async () => {
    if (!deleted || !selected.size || busy || resource.loading || restoreBusyRef.current) return
    const keys = [...selected]
    const isCurrent = beginAction()
    restoreBusyRef.current = true
    setBusy('batch-restore'); setMessage(undefined)
    try {
      const results = await restoreSelection(client, keys, isCurrent)
      if (!isCurrent()) return
      setSelected(new Set(results.filter((result) => !result.restored).map((result) => result.key)))
      setMessage(restoreSelectionMessage(results))
      reloadFiles()
    } catch (reason) {
      if (isCurrent()) setMessage({ tone: 'danger', text: reason instanceof Error ? reason.message : '批量恢复失败' })
    } finally {
      if (isCurrent()) {
        restoreBusyRef.current = false
        setBusy('')
      }
    }
  }

  const batchTag = async (tagKey: string, tagValue: string) => {
    const keys = [...selected]
    const isCurrent = beginAction()
    setBusy('batch-tag'); setMessage(undefined)
    try {
      const results = await client.batchTagFiles(keys, { [tagKey]: tagValue })
      if (!isCurrent()) return
      const failed = results.filter((result) => result.error)
      setSelected(new Set(failed.map((result) => result.key)))
      setMessage(batchMessage(keys.length, failed.length, '设置标签'))
      reloadFiles()
    } catch (reason) {
      if (isCurrent()) setMessage({ tone: 'danger', text: reason instanceof Error ? reason.message : '批量标签失败' })
    } finally {
      if (isCurrent()) setBusy('')
    }
  }

  return (
    <section>
      <PageHeader title="文件" description={view === 'mine' ? '仅列出当前身份拥有且仍有权读取的文件；这是归属筛选，不会改变文件权限。' : '浏览默认存储桶中有权访问的文件，批量管理对象，并从回收站恢复文件。'} />
      <div className="filter-bar" role="group" aria-label="文件视图">
        <IrisButton variant={view === 'all' ? 'solid' : 'outline'} aria-pressed={view === 'all'} onClick={() => setView('all')}>可访问文件</IrisButton>
        <IrisButton variant={view === 'mine' ? 'solid' : 'outline'} aria-pressed={view === 'mine'} onClick={() => setView('mine')}>我的文件</IrisButton>
      </div>
      {!deleted ? <div className="files-upload"><UploadPanel key={activePrefix} client={client} initialPrefix={activePrefix} onUploaded={reloadFiles} /></div> : null}
      <form className="filter-bar" onSubmit={(event) => { event.preventDefault(); navigate(prefix) }}>
        <IrisInput value={prefix} placeholder="目录路径，例如 team/docs/" onChange={(event) => setPrefix(event.target.value)} />
        <label className="deleted-toggle"><IrisSwitch checked={deleted} onChange={setDeleted} /><span>回收站</span></label>
        <IrisButton type="submit" variant="outline">进入</IrisButton>
      </form>
      {!deleted && shareTarget?.client === client && shareTarget.scopeKey === scopeKey ? <FileSharePanel
        key={`${shareTarget.object.bucket}:${shareTarget.object.key}`} client={client} object={shareTarget.object}
        close={() => setShareTarget(undefined)}
      /> : null}
      {message ? <IrisAlert tone={message.tone}>{message.text}</IrisAlert> : null}
      <FileDownloadNotice transfer={fileDownload} />
      {resource.loading ? <PageLoading /> : null}
      {resource.error ? <PageError error={resource.error} retry={reloadFiles} /> : null}
      {currentData && displayedPage ? <div className="files-stack">
        {folderPaging.listing ? <FolderBrowser
          listing={folderPaging.listing} path={activePrefix} allowRecursiveDelete={view === 'all'}
          paging={{ ...folderPaging, reload: reloadFiles }} busy={Boolean(busy) || resource.loading}
          navigate={navigate} create={(path) => run(`folder-create:${path}`, () => commands.createFolder(path))}
          remove={(path) => run(`folder-delete:${path}`, () => commands.removeFolder(path))}
        /> : null}
        {deleted ? <RecycleToolbar count={selected.size} busy={Boolean(busy) || resource.loading} clear={() => setSelected(new Set())} restore={batchRestore} />
          : <BatchToolbar keys={[...selected]} busy={Boolean(busy)} clear={() => setSelected(new Set())} applyTags={batchTag} remove={batchDelete} />}
        <FileTable page={displayedPage} deleted={deleted} selected={selected} busy={Boolean(busy) || resource.loading} loadingMore={loadingMore} select={select} open={(key) => onOpenObject(key, deleted)} download={download} share={(object) => setShareTarget({ client, scopeKey, object })} remove={(key) => void run(`delete:${key}`, () => commands.removeFile(key))} restore={(key) => void run(`restore:${key}`, () => commands.restoreFile(key))} loadMore={() => void loadMore()} reload={reloadFiles} />
      </div> : null}
    </section>
  )
}

function filterFilePage(page: FilePage, prefix: string, deleted: boolean): FilePage {
  return {
    ...page,
    objects: page.objects.filter((item) =>
      item.content_type !== 'application/x-directory' && (deleted || isDirectFile(item.key, prefix))),
  }
}

function batchMessage(total: number, failed: number, action: string): { tone: 'success' | 'danger'; text: string } {
  if (failed === 0) return { tone: 'success', text: `已为 ${total} 个对象完成${action}。` }
  return { tone: 'danger', text: `${action}完成，但有 ${failed}/${total} 个对象失败；失败项仍保持选中。` }
}
