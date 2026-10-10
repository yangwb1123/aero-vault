import * as React from 'react'
import { IrisAlert, IrisButton, IrisCard, IrisEmptyState, IrisInput } from '@iris-ui-kit/react'
import type { FolderItem, FolderListing } from '../../api/files'
import { childFolderPath, folderCrumbs } from './folderPath'

interface FolderPaging {
  loading: boolean
  error?: string
  loadMore(): void
  reload(): void
}

interface FolderBrowserProps {
  listing: FolderListing
  path: string
  busy: boolean
  navigate(path: string): void
  create(path: string): Promise<void>
  remove(path: string): Promise<void>
  allowRecursiveDelete?: boolean
  paging?: FolderPaging
}

export function FolderBrowser(props: FolderBrowserProps): React.ReactElement {
  const { listing, path, busy, navigate, create, paging } = props
  const [name, setName] = React.useState('')
  const folders = listing.items.filter((item) => item.type === 'folder')
  const submit = async () => {
    const next = childFolderPath(path, name)
    if (!next || next === path) return
    await create(next)
    setName('')
  }
  return (
    <IrisCard variant="outline" header="目录">
      <nav className="folder-crumbs" aria-label="目录路径">
        <button type="button" onClick={() => navigate('')}>根目录</button>
        {folderCrumbs(path).map((crumb) => <React.Fragment key={crumb.path}><span>/</span><button type="button" onClick={() => navigate(crumb.path)}>{crumb.label}</button></React.Fragment>)}
      </nav>
      <form className="folder-create" onSubmit={(event) => { event.preventDefault(); void submit() }}>
        <IrisInput required value={name} placeholder="新目录名称" disabled={busy} onChange={(event) => setName(event.target.value)} />
        <IrisButton type="submit" variant="outline" disabled={busy}>创建目录</IrisButton>
      </form>
      {folders.length ? <FolderEntries {...props} folders={folders} /> : <IrisEmptyState
        title={listing.has_more ? '尚未发现子目录' : '没有子目录'}
        description={listing.has_more ? '目录列表尚未加载完整，请继续加载。' : '可在当前路径创建目录，或直接上传文件。'}
      />}
      {paging?.error ? <IrisAlert tone="danger">{paging.error}</IrisAlert> : null}
      {listing.has_more && paging ? <FolderPageControls listing={listing} paging={paging} busy={busy} /> : null}
    </IrisCard>
  )
}

function FolderEntries({ folders, path, busy, navigate, remove, allowRecursiveDelete = true }: FolderBrowserProps & { folders: FolderItem[] }): React.ReactElement {
  return <div className="folder-grid">{folders.map((folder) => {
    const fullPath = childFolderPath(path, folder.name)
    return (
      <div className="folder-item" key={fullPath}>
        <button type="button" className="folder-open" onClick={() => navigate(fullPath)}><strong>{folder.name}</strong><span>打开目录</span></button>
        {allowRecursiveDelete ? <IrisButton size="sm" variant="ghost" className="text-danger" disabled={busy} onClick={() => {
          if (window.confirm(`将目录 ${fullPath} 及其中全部对象移入回收站？`)) void remove(fullPath)
        }}>删除</IrisButton> : null}
      </div>
    )
  })}</div>
}

function FolderPageControls({ listing, paging, busy }: { listing: FolderListing; paging: FolderPaging; busy: boolean }): React.ReactElement {
  return <div className="access-actions table-note">
    <span>目录列表尚未加载完整</span>
    {listing.next_marker ? <IrisButton
      variant="outline" size="sm" disabled={busy || paging.loading} loading={paging.loading} onClick={paging.loadMore}
    >加载更多目录</IrisButton> : <>
      <span>后续目录游标不可用</span>
      <IrisButton variant="outline" size="sm" disabled={busy || paging.loading} onClick={paging.reload}>重新加载目录</IrisButton>
    </>}
  </div>
}
