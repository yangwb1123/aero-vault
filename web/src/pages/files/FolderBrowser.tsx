import * as React from 'react'
import { IrisButton, IrisCard, IrisEmptyState, IrisInput } from '@iris-ui-kit/react'
import type { FolderListing } from '../../api/files'
import { childFolderPath, folderCrumbs } from './folderPath'

export function FolderBrowser({ listing, path, busy, navigate, create, remove }: {
  listing: FolderListing
  path: string
  busy: boolean
  navigate(path: string): void
  create(path: string): Promise<void>
  remove(path: string): Promise<void>
}): React.ReactElement {
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
      {folders.length ? <div className="folder-grid">{folders.map((folder) => {
        const fullPath = childFolderPath(path, folder.name)
        return (
          <div className="folder-item" key={fullPath}>
            <button type="button" className="folder-open" onClick={() => navigate(fullPath)}><strong>{folder.name}</strong><span>打开目录</span></button>
            <IrisButton size="sm" variant="ghost" className="text-danger" disabled={busy} onClick={() => {
              if (window.confirm(`将目录 ${fullPath} 及其中全部对象移入回收站？`)) void remove(fullPath)
            }}>删除</IrisButton>
          </div>
        )
      })}</div> : <IrisEmptyState title="没有子目录" description="可在当前路径创建目录，或直接上传文件。" />}
    </IrisCard>
  )
}
