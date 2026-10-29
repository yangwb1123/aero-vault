import type { FolderCrumb } from './folderPath'

// Server listing names are literal segments, not manual directory input.
export function literalChildFolderPath(parent: string, segment: string): string {
  return `${parent}${segment}/`
}

export function literalFolderCrumbs(path: string): FolderCrumb[] {
  if (!path) return []
  const segments = path.split('/').slice(0, -1)
  return segments.map((label, index) => ({ label, path: `${segments.slice(0, index + 1).join('/')}/` }))
}

export function isLiteralDirectFile(key: string, parent: string): boolean {
  if (!key.startsWith(parent)) return false
  const remainder = key.slice(parent.length)
  return remainder !== '' && !remainder.includes('/')
}
