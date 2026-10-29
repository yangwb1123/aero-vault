import type { FileListView } from '../../api/files'

/** Navigation only: always the default bucket; never an authorization grant. */
export type FileLocation = {
  path: string
  view: FileListView
  deleted: boolean
}

export const defaultFileLocation: Readonly<FileLocation> = Object.freeze({ path: '', view: 'all', deleted: false })
const routePrefix = 'files/v1/'
const maxPathLength = 4096
const maxRouteLength = 32768

function isDirectoryPath(value: unknown): value is string {
  return typeof value === 'string' && value.length <= maxPathLength && (value === '' || value.endsWith('/'))
}

function isFileLocation(value: unknown): value is FileLocation {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false
  const location = value as Record<string, unknown>
  return Object.keys(location).sort().join(',') === 'deleted,path,view' &&
    isDirectoryPath(location.path) && (location.view === 'all' || location.view === 'mine') &&
    typeof location.deleted === 'boolean'
}

/** The hash router encodes this entire route once. Do not URI-decode paths here. */
export function fileLocationRoute(location: FileLocation): string {
  const value = { path: location.path, view: location.view, deleted: location.deleted }
  if (!isFileLocation(value)) throw new Error('文件位置不可用：目录或视图超出支持范围。')
  if (value.path === '' && value.view === 'all' && !value.deleted) return 'files'
  return `${routePrefix}${JSON.stringify(value)}`
}

export function parseFileLocationRoute(route: string): FileLocation | undefined {
  if (route === 'files') return { ...defaultFileLocation }
  if (!route.startsWith(routePrefix) || route.length > maxRouteLength) return undefined
  try {
    const value: unknown = JSON.parse(route.slice(routePrefix.length))
    return isFileLocation(value) ? value : undefined
  } catch {
    return undefined
  }
}
