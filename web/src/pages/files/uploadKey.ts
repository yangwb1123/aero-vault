export function uploadKey(prefix: string, filename: string): string {
  const value = prefix.trim()
  if (!value) return filename
  return `${value.endsWith('/') ? value : `${value}/`}${filename}`
}

export function createUploadIdempotencyKeyFactory(createID = randomUploadID): (file: File, targetKey: string) => string {
  const keys = new WeakMap<File, Map<string, string>>()
  return (file, targetKey) => {
    const targets = keys.get(file) ?? new Map<string, string>()
    let key = targets.get(targetKey)
    if (!key) {
      key = createID()
      targets.set(targetKey, key)
      keys.set(file, targets)
    }
    return key
  }
}

function randomUploadID(): string {
  if (globalThis.crypto.randomUUID) return globalThis.crypto.randomUUID()
  return Array.from(globalThis.crypto.getRandomValues(new Uint8Array(16)), (value) =>
    value.toString(16).padStart(2, '0'),
  ).join('')
}
