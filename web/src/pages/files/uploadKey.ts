export function uploadKey(prefix: string, filename: string): string {
  const value = prefix.trim()
  if (!value) return filename
  return `${value.endsWith('/') ? value : `${value}/`}${filename}`
}
