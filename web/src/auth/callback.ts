export interface BrowserSession {
  accessToken: string
  idToken?: string
  expiresAt?: number
}

export interface CallbackResult {
  session?: BrowserSession
  cleanUrl: string
}

const maxTokenBytes = 64 * 1024

export function consumeOIDCCallback(href: string, now = Date.now()): CallbackResult {
  const url = new URL(href)
  const fragment = new URLSearchParams(url.hash.replace(/^#/, ''))
  const accessToken = fragment.get('oidc_access_token') ?? ''
  const cleanUrl = `${url.pathname}${url.search}`
  if (!accessToken) return { cleanUrl }
  if (accessToken.length > maxTokenBytes) throw new Error('Snaplink access token 响应过大')

  const expiresIn = Number(fragment.get('oidc_expires_in') ?? 0)
  return {
    cleanUrl,
    session: {
      accessToken,
      idToken: fragment.get('oidc_id_token') || undefined,
      expiresAt: Number.isFinite(expiresIn) && expiresIn > 0 ? now + expiresIn * 1000 : undefined,
    },
  }
}
