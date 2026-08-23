export function parseStringRecord(raw: string, label: string): Record<string, string> {
  let parsed: unknown
  try {
    parsed = JSON.parse(raw)
  } catch {
    throw new Error(`${label}必须是有效 JSON`)
  }
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    throw new Error(`${label}必须是 JSON 对象`)
  }
  for (const value of Object.values(parsed)) {
    if (typeof value !== 'string') throw new Error(`${label}的值必须全部是字符串`)
  }
  return parsed as Record<string, string>
}
