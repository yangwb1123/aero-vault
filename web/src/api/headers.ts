export function requestHeaders(source: HeadersInit | undefined, token: string): Record<string, string> {
  const headers: Record<string, string> = {}
  if (source instanceof Headers) {
    source.forEach((value, key) => { headers[key] = value })
  } else if (Array.isArray(source)) {
    for (const [key, value] of source) headers[key] = value
  } else if (source) {
    Object.assign(headers, source)
  }
  const names = new Set(Object.keys(headers).map((name) => name.toLowerCase()))
  if (!names.has('accept')) headers.Accept = 'application/json'
  if (token) headers.Authorization = `Bearer ${token}`
  return headers
}
