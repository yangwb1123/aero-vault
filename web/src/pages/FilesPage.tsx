import * as React from 'react'
import { IrisAlert, IrisButton, IrisInput, IrisSwitch } from '@iris-ui-kit/react'
import { listNextFilePage } from '../api/filePagination'
import type { FolderListing } from '../api/files'
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
import { defaultFileLocation, type FileLocation } from './files/fileLocation'
import { normalizeFolderPath } from './files/folderPath'
import { isLiteralDirectFile } from './files/literalFolderPath'
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

type FilesPageProps = {
  client: VaultClient
  onOpenObject(key: string, deleted: boolean): void
  location?: FileLocation
  onLocationChange?(location: FileLocation): void
}
type FileScope = ReturnType<typeof useFileScope>
type FileListing = ReturnType<typeof useFileListing>
type FileOperations = ReturnType<typeof useFileOperations>
type FileNavigation = ReturnType<typeof useFileLocation>

function useFileLocation({ location, onLocationChange }: FilesPageProps) {
  const [local, setLocal] = React.useState<FileLocation>({ ...defaultFileLocation })
  const { path, view, deleted } = location ?? local
  const current = { path, view, deleted }
  const [prefix, setPrefix] = React.useState(current.path)
  React.useLayoutEffect(() => { setPrefix(current.path) }, [current.path])
  const change = (next: FileLocation) => {
    if (!location) setLocal(next)
    onLocationChange?.(next)
  }
  const navigate = (path: string) => {
    setPrefix(path)
    change({ ...current, path })
  }
  return { ...current, prefix, setPrefix, navigate,
    setView: (view: FileLocation['view']) => change({ ...current, view }),
    setDeleted: (deleted: boolean) => change({ ...current, deleted }) }
}

function useFileScope(client: VaultClient, scopeKey: string) {
  const [selected, setSelected] = React.useState<Set<string>>(new Set())
  const [shareTarget, setShareTarget] = React.useState<{ client: VaultClient; scopeKey: string; object: VaultObject }>()
  const [actionBusy, setBusy] = React.useState('')
  const [message, setMessage] = React.useState<{ tone: 'success' | 'danger'; text: string }>()
  const [continuation, setContinuation] = React.useState<FileContinuation>()
  const [loadingMore, setLoadingMore] = React.useState(false)
  const loadingMoreRef = React.useRef(false)
  const fileScopeRef = React.useRef({ client, scopeKey })
  const fileRequestRef = React.useRef(0)
  const actionRequestRef = React.useRef(0)
  const restoreBusyRef = React.useRef(false)
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
  return { client, scopeKey, selected, setSelected, shareTarget, setShareTarget, actionBusy, setBusy,
    message, setMessage, continuation, setContinuation, loadingMore, setLoadingMore, loadingMoreRef,
    fileScopeRef, fileRequestRef, actionRequestRef, restoreBusyRef }
}

function useFileListing(client: VaultClient, location: FileLocation, scope: FileScope) {
  const { path: activePrefix, deleted, view } = location
  const { scopeKey, continuation } = scope
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
  const reloadFiles = () => {
    scope.fileRequestRef.current += 1
    scope.loadingMoreRef.current = false
    scope.setContinuation(undefined)
    scope.setLoadingMore(false)
    scope.setShareTarget(undefined)
    resource.reload()
  }
  return { resource, currentData, folderPaging, displayedPage, reloadFiles }
}

function beginFileAction(scope: FileScope) {
  const requestID = ++scope.actionRequestRef.current
  const target = { client: scope.client, scopeKey: scope.scopeKey }
  return () => scope.actionRequestRef.current === requestID &&
    scope.fileScopeRef.current.client === target.client && scope.fileScopeRef.current.scopeKey === target.scopeKey
}

function fileListBusy(busy: string, loading: boolean): boolean {
  return Boolean(busy) || loading
}

async function loadMoreFiles(scope: FileScope, location: FileLocation, listing: FileListing, busy: string) {
  const { displayedPage: page, resource } = listing
  const { client, scopeKey } = scope
  const { path: activePrefix, deleted, view } = location
  if (!page?.has_more || !page.next_marker || scope.loadingMoreRef.current || fileListBusy(busy, resource.loading)) return
  const target = { client, scopeKey, activePrefix, deleted, view }
  const requestID = ++scope.fileRequestRef.current
  const isCurrent = () => scope.fileRequestRef.current === requestID &&
    scope.fileScopeRef.current.client === target.client && scope.fileScopeRef.current.scopeKey === target.scopeKey
  scope.loadingMoreRef.current = true
  scope.setLoadingMore(true)
  scope.setMessage(undefined)
  try {
    const nextPage = await listNextFilePage(target.client, target.activePrefix, target.deleted, page.next_marker, target.view)
    if (!isCurrent()) return
    const filteredPage = filterFilePage(nextPage, target.activePrefix, target.deleted)
    scope.setContinuation((current) => {
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
    if (isCurrent()) scope.setMessage({ tone: 'danger', text: reason instanceof Error ? reason.message : '加载更多文件失败' })
  } finally {
    if (isCurrent()) {
      scope.loadingMoreRef.current = false
      scope.setLoadingMore(false)
    }
  }
}

async function runFileAction(scope: FileScope, reloadFiles: () => void, label: string, action: () => Promise<void>) {
  const isCurrent = beginFileAction(scope)
  scope.setBusy(label); scope.setMessage(undefined)
  try {
    await action()
    if (isCurrent()) scope.setMessage({ tone: 'success', text: '操作已完成。' })
  } catch (reason) {
    if (isCurrent()) scope.setMessage({ tone: 'danger', text: reason instanceof Error ? reason.message : '操作失败' })
  } finally {
    if (isCurrent()) {
      scope.setBusy('')
      reloadFiles()
    }
  }
}

async function batchDeleteFiles(scope: FileScope, reloadFiles: () => void) {
  const keys = [...scope.selected]
  const isCurrent = beginFileAction(scope)
  scope.setBusy('batch-delete'); scope.setMessage(undefined)
  try {
    const results = await scope.client.batchDeleteFiles(keys)
    if (!isCurrent()) return
    const failed = results.filter((result) => !result.deleted)
    scope.setSelected(new Set(failed.map((result) => result.key)))
    scope.setMessage(batchMessage(keys.length, failed.length, '删除'))
    reloadFiles()
  } catch (reason) {
    if (isCurrent()) scope.setMessage({ tone: 'danger', text: reason instanceof Error ? reason.message : '批量删除失败' })
  } finally {
    if (isCurrent()) scope.setBusy('')
  }
}

async function batchRestoreFiles(scope: FileScope, listing: FileListing, deleted: boolean, busy: string) {
  if (!deleted || !scope.selected.size || fileListBusy(busy, listing.resource.loading) || scope.restoreBusyRef.current) return
  const keys = [...scope.selected]
  const isCurrent = beginFileAction(scope)
  scope.restoreBusyRef.current = true
  scope.setBusy('batch-restore'); scope.setMessage(undefined)
  try {
    const results = await restoreSelection(scope.client, keys, isCurrent)
    if (!isCurrent()) return
    scope.setSelected(new Set(results.filter((result) => !result.restored).map((result) => result.key)))
    scope.setMessage(restoreSelectionMessage(results))
    listing.reloadFiles()
  } catch (reason) {
    if (isCurrent()) scope.setMessage({ tone: 'danger', text: reason instanceof Error ? reason.message : '批量恢复失败' })
  } finally {
    if (isCurrent()) {
      scope.restoreBusyRef.current = false
      scope.setBusy('')
    }
  }
}

async function batchTagFiles(scope: FileScope, reloadFiles: () => void, tagKey: string, tagValue: string) {
  const keys = [...scope.selected]
  const isCurrent = beginFileAction(scope)
  scope.setBusy('batch-tag'); scope.setMessage(undefined)
  try {
    const results = await scope.client.batchTagFiles(keys, { [tagKey]: tagValue })
    if (!isCurrent()) return
    const failed = results.filter((result) => result.error)
    scope.setSelected(new Set(failed.map((result) => result.key)))
    scope.setMessage(batchMessage(keys.length, failed.length, '设置标签'))
    reloadFiles()
  } catch (reason) {
    if (isCurrent()) scope.setMessage({ tone: 'danger', text: reason instanceof Error ? reason.message : '批量标签失败' })
  } finally {
    if (isCurrent()) scope.setBusy('')
  }
}

function useFileOperations(scope: FileScope, listing: FileListing, location: FileLocation) {
  const { client, scopeKey } = scope
  const commands = React.useMemo(() => createFileCommands(client), [client])
  const fileDownload = useObjectDownload(client, scopeKey)
  const busy = scope.actionBusy || (fileDownload.pending ? 'download' : '')
  const select = (keys: string[], checked: boolean) => scope.setSelected((current) => {
    const next = new Set(current)
    keys.forEach((key) => checked ? next.add(key) : next.delete(key))
    return next
  })
  const download = (item: VaultObject) => {
    if (location.deleted || scope.actionBusy || listing.resource.loading) return
    scope.setMessage(undefined)
    void fileDownload.download({ key: item.key })
  }
  return { commands, fileDownload, busy, select, download,
    run: (label: string, action: () => Promise<void>) => runFileAction(scope, listing.reloadFiles, label, action),
    batchDelete: () => batchDeleteFiles(scope, listing.reloadFiles),
    batchRestore: () => batchRestoreFiles(scope, listing, location.deleted, busy),
    batchTag: (key: string, value: string) => batchTagFiles(scope, listing.reloadFiles, key, value),
    loadMore: () => void loadMoreFiles(scope, location, listing, busy) }
}

export function FilesPage(props: FilesPageProps): React.ReactElement {
  const { client, onOpenObject } = props
  const navigation = useFileLocation(props)
  const scope = useFileScope(client, JSON.stringify([navigation.path, navigation.deleted, navigation.view]))
  const listing = useFileListing(client, navigation, scope)
  const operations = useFileOperations(scope, listing, navigation)
  const { shareTarget, scopeKey, message } = scope
  return (
    <section>
      <FileLocationControls client={client} navigation={navigation} reload={listing.reloadFiles} />
      {!navigation.deleted && shareTarget?.client === client && shareTarget.scopeKey === scopeKey ? <FileSharePanel
        key={`${shareTarget.object.bucket}:${shareTarget.object.key}`} client={client} object={shareTarget.object}
        close={() => scope.setShareTarget(undefined)}
      /> : null}
      {message ? <IrisAlert tone={message.tone}>{message.text}</IrisAlert> : null}
      <FileDownloadNotice transfer={operations.fileDownload} />
      {listing.resource.loading ? <PageLoading /> : null}
      {listing.resource.error ? <PageError error={listing.resource.error} retry={listing.reloadFiles} /> : null}
      <FileRows scope={scope} listing={listing} operations={operations} navigation={navigation} open={onOpenObject} />
    </section>
  )
}

function FileLocationControls({ client, navigation, reload }: {
  client: VaultClient; navigation: FileNavigation; reload(): void
}): React.ReactElement {
  const { path, view, deleted, prefix, setPrefix, setView, setDeleted, navigate } = navigation
  return <>
    <PageHeader title="文件" description={view === 'mine' ? '仅列出当前身份拥有且仍有权读取的文件；这是归属筛选，不会改变文件权限。' : '浏览默认存储桶中有权访问的文件，批量管理对象，并从回收站恢复文件。'} />
    <div className="filter-bar" role="group" aria-label="文件视图">
      <IrisButton variant={view === 'all' ? 'solid' : 'outline'} aria-pressed={view === 'all'} onClick={() => setView('all')}>可访问文件</IrisButton>
      <IrisButton variant={view === 'mine' ? 'solid' : 'outline'} aria-pressed={view === 'mine'} onClick={() => setView('mine')}>我的文件</IrisButton>
    </div>
    {!deleted ? <div className="files-upload">
      {path !== path.trim() ? <IrisAlert tone="warning">
        当前目录不支持上传：上传前缀会去除空白，可能写入其他 Key。请返回根目录或选择其他目录后上传；当前目录仍可浏览和管理。
      </IrisAlert> : <UploadPanel key={path} client={client} initialPrefix={path} onUploaded={reload} />}
    </div> : null}
    <form className="filter-bar" onSubmit={(event) => { event.preventDefault(); navigate(normalizeFolderPath(prefix)) }}>
      <IrisInput value={prefix} placeholder="目录路径，例如 team/docs/" onChange={(event) => setPrefix(event.target.value)} />
      <label className="deleted-toggle"><IrisSwitch checked={deleted} onChange={setDeleted} /><span>回收站</span></label>
      <IrisButton type="submit" variant="outline">进入</IrisButton>
    </form>
  </>
}

function FileRows({ scope, listing, operations, navigation, open }: {
  scope: FileScope; listing: FileListing; operations: FileOperations; navigation: FileNavigation
  open(key: string, deleted: boolean): void
}): React.ReactElement | null {
  const { currentData, displayedPage, folderPaging, resource, reloadFiles } = listing
  if (!currentData || !displayedPage) return null
  const { busy, commands, run, select, download, batchRestore, batchDelete, batchTag, loadMore } = operations
  const { selected, setSelected, loadingMore, setShareTarget, client, scopeKey } = scope
  const { deleted, view, path, navigate } = navigation
  return <div className="files-stack">
    {folderPaging.listing ? <FolderBrowser
      listing={folderPaging.listing} path={path} allowRecursiveDelete={view === 'all'}
      paging={{ ...folderPaging, reload: reloadFiles }} busy={Boolean(busy) || resource.loading}
      navigate={navigate} create={(path) => run(`folder-create:${path}`, () => commands.createFolder(path))}
      remove={(path) => run(`folder-delete:${path}`, () => commands.removeFolder(path))}
    /> : null}
    {deleted ? <RecycleToolbar count={selected.size} busy={Boolean(busy) || resource.loading} clear={() => setSelected(new Set())} restore={batchRestore} />
      : <BatchToolbar keys={[...selected]} busy={Boolean(busy)} clear={() => setSelected(new Set())} applyTags={batchTag} remove={batchDelete} />}
    <FileTable page={displayedPage} deleted={deleted} selected={selected} busy={Boolean(busy) || resource.loading} loadingMore={loadingMore} select={select} open={(key) => open(key, deleted)} download={download} share={(object) => setShareTarget({ client, scopeKey, object })} remove={(key) => void run(`delete:${key}`, () => commands.removeFile(key))} restore={(key) => void run(`restore:${key}`, () => commands.restoreFile(key))} loadMore={loadMore} reload={reloadFiles} />
  </div>
}

function filterFilePage(page: FilePage, prefix: string, deleted: boolean): FilePage {
  return {
    ...page,
    objects: page.objects.filter((item) =>
      item.content_type !== 'application/x-directory' && (deleted || isLiteralDirectFile(item.key, prefix))),
  }
}

function batchMessage(total: number, failed: number, action: string): { tone: 'success' | 'danger'; text: string } {
  if (failed === 0) return { tone: 'success', text: `已为 ${total} 个对象完成${action}。` }
  return { tone: 'danger', text: `${action}完成，但有 ${failed}/${total} 个对象失败；失败项仍保持选中。` }
}
