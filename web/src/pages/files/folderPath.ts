export interface FolderCrumb {
  label: string
  path: string
}

export function normalizeFolderPath(value: string): string {
  const segments = value.split('/').map((segment) => segment.trim()).filter(Boolean)
  return segments.length ? `${segments.join('/')}/` : ''
}

export function childFolderPath(parent: string, name: string): string {
  return normalizeFolderPath(`${normalizeFolderPath(parent)}${name}`)
}

export function folderCrumbs(path: string): FolderCrumb[] {
  const segments = normalizeFolderPath(path).split('/').filter(Boolean)
  return segments.map((label, index) => ({
    label,
    path: `${segments.slice(0, index + 1).join('/')}/`,
  }))
}

export function isDirectFile(key: string, parent: string): boolean {
  const prefix = normalizeFolderPath(parent)
  if (!key.startsWith(prefix)) return false
  const remainder = key.slice(prefix.length)
  return remainder !== '' && !remainder.includes('/')
}
