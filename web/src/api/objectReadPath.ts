export function objectReadPath(key: string, representation: 'content' | 'thumbnail', versionID?: string): string {
  const query = new URLSearchParams({ representation })
  if (versionID !== undefined) query.set('version', versionID)
  return `/files/${key.split('/').map(encodeURIComponent).join('/')}?${query}`
}
