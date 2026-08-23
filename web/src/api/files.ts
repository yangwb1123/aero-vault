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
