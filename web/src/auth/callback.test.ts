import { describe, expect, it } from 'vitest'
import { consumeOIDCCallback } from './callback'

describe('consumeOIDCCallback', () => {
  it('extracts the server-side Snaplink callback without persisting credentials', () => {
    const result = consumeOIDCCallback(
      'https://vault.example/ui?mode=files#oidc_access_token=access-1&oidc_id_token=id-1&oidc_expires_in=60',
      1_000,
    )
    expect(result.cleanUrl).toBe('/ui?mode=files')
    expect(result.session).toEqual({
      accessToken: 'access-1',
      idToken: 'id-1',
      expiresAt: 61_000,
    })
  })

  it('ignores unrelated hash routes', () => {
    expect(consumeOIDCCallback('https://vault.example/ui#/files').session).toBeUndefined()
  })
})
