export type FileListView = 'all' | 'mine'

export type FolderItem = {
  name: string
  type: 'file' | 'folder'
  size?: number
  etag?: string
  last_modified?: string
}

export interface FolderListing {
  prefix: string
  items: FolderItem[]
  has_more?: boolean
  next_marker?: string
}

export function folderListingPath(path: string, view: FileListView, marker: string): string {
  const query = new URLSearchParams(view === 'mine' ? { view } : {})
  if (path) query.set('path', path)
  if (marker) query.set('marker', marker)
  return `/folders${query.size ? `?${query}` : ''}`
}

export type BatchDeleteResult = {
  key: string
  deleted: boolean
  error?: string
}

export type BatchTagResult = {
  key: string
  error?: string
}
