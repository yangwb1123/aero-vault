import { describe, expect, it } from 'vitest'
import { VaultApiError } from './transport'
import { presentReadError } from './errorPresentation'

const statusCases = [
  { status: 401, title: '需要身份认证', hint: '登录' },
  { status: 403, title: '没有访问权限', hint: '管理员' },
  { status: 404, title: '资源不存在或不可用', hint: '路径' },
  { status: 409, title: '资源状态冲突', hint: '最新' },
  { status: 412, title: '资源状态冲突', hint: '最新' },
  { status: 413, title: '请求大小超限', hint: '限制' },
  { status: 429, title: '请求过于频繁', hint: '稍候' },
  ...[500, 502, 503, 504].map((status) => ({ status, title: '服务暂不可用', hint: '稍后' })),
]

describe('shared read failure guidance', () => {
  it.each(statusCases)('distinguishes HTTP $status using status rather than server prose', ({ status, title, hint }) => {
    const error = new VaultApiError(status, 'UnfamiliarCode', '服务端原始说明', 'request-17')
    const presentation = presentReadError(error)

    expect(presentation?.title).toBe(title)
    expect(presentation?.guidance).toContain(hint)
    expect(presentation?.message).toBe(error.message)
    expect(presentation?.requestId).toBe('request-17')
    expect(error.status).toBe(status)
    expect(error.code).toBe('UnfamiliarCode')
    expect(error.message).toBe('服务端原始说明')
  })

  it('does not turn an explicit denial into authentication guidance based on the message', () => {
    const presentation = presentReadError(new VaultApiError(403, 'AccessDenied', 'token expired'))
    expect(presentation?.title).toBe('没有访问权限')
    expect(presentation?.guidance).not.toContain('登录')
    expect(presentation?.message).toBe('token expired')
  })

  it('does not mislabel a quota failure as temporary service unavailability', () => {
    const presentation = presentReadError(new VaultApiError(507, 'QuotaExceeded', 'quota exceeded'))
    expect(presentation?.title).toBe('存储配额不足')
    expect(presentation?.guidance).toContain('配额')
    expect(presentation?.message).toBe('quota exceeded')
  })
})

describe('unknown failures and cancellation', () => {
  it.each([400, 418, 422])('keeps unknown HTTP %s errors readable', (status) => {
    const error = new VaultApiError(status, 'CustomCode', '自定义服务端说明')
    const presentation = presentReadError(error)
    expect(presentation?.title).toBe('读取失败')
    expect(presentation?.guidance).toContain('手动重试读取')
    expect(presentation?.message).toBe(error.message)
    expect(presentation?.requestId).toBe('')
  })

  it.each([new TypeError('Failed to fetch'), new Error('session read unavailable')])(
    'preserves safe plain error details without inventing status or request ID (%s)',
    (error) => {
      const presentation = presentReadError(error)
      expect(presentation?.title).toBe('读取失败')
      expect(presentation?.guidance).toContain('网络')
      expect(presentation?.message).toBe(error.message)
      expect(presentation?.requestId).toBe('')
    },
  )

  it.each([new DOMException('cancelled', 'AbortError'), Object.assign(new Error('stopped'), { name: 'AbortError' })])(
    'does not present cancellation as a failure (%s)',
    (error) => { expect(presentReadError(error)).toBeUndefined() },
  )
})

const unsafeMessages = [
  '<html><head><title>502</title></head><body>proxy-secret</body></html>',
  '<script>proxy-secret</script>',
  'request failed https://example.invalid/file?X-Amz-Signature=synthetic-secret',
  'request failed //example.invalid/file?token=synthetic-secret',
  'request failed /v1/files/a?signature=synthetic-secret',
  'request failed /share/synthetic-secret',
  'Authorization: Bearer synthetic-secret',
  'Bearer synthetic-secret',
  'Cookie: session=synthetic-secret',
  'X-Api-Key: synthetic-secret',
  'X-Aero-Share-Password: synthetic-secret',
  'access_token=synthetic-secret',
  '{"refresh_token":"synthetic-secret"}',
  'password=synthetic-secret',
  'eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJub3QtcmVhbCJ9.c3ludGhldGlj',
  'internal\u0000details',
]

describe('diagnostic message privacy', () => {
  it.each(unsafeMessages)('withholds unsafe details but leaves the API error intact (%s)', (message) => {
    const error = new VaultApiError(502, 'OriginalCode', message, 'safe-id')
    const presentation = presentReadError(error)

    expect(presentation?.message).toBe('详细信息已隐藏（可能包含页面内容或敏感信息）。')
    expect(presentation?.requestId).toBe('safe-id')
    expect(error.message).toBe(message)
    expect(error.code).toBe('OriginalCode')
  })

  it('applies the same privacy rule to network error details', () => {
    const presentation = presentReadError(new TypeError('fetch failed https://example.invalid?token=synthetic-secret'))
    expect(presentation?.title).toBe('读取失败')
    expect(presentation?.message).toContain('详细信息已隐藏')
    expect(presentation?.message).not.toContain('synthetic-secret')
  })

  it('bounds unusually large messages without mutating the original', () => {
    const message = 'large diagnostic '.repeat(1000)
    const error = new VaultApiError(502, 'ProxyError', message)
    expect(presentReadError(error)?.message).toContain('详细信息已隐藏')
    expect(error.message).toBe(message)
  })

  it('does not manufacture details for an empty message', () => {
    expect(presentReadError(new VaultApiError(403, '', ''))?.message).toBe('')
  })
})

describe('request ID privacy', () => {
  it.each(['req-7', '81846c24-024f-473c-ad22-5f7e837e2ae8', 'edge:node_1.2', '请求/7+%?#'])('displays a safe request ID (%s)', (id) => {
    expect(presentReadError(new VaultApiError(503, 'Unavailable', 'failed', id))?.requestId).toBe(id)
  })

  it.each(['', 'x'.repeat(1000), '<b>proxy-secret</b>', 'Bearer synthetic-secret', 'token=synthetic-secret'])(
    'does not render an unsafe or unbounded request ID (%s)',
    (id) => {
      const error = new VaultApiError(503, 'Unavailable', 'failed', id)
      expect(presentReadError(error)?.requestId).toBe('')
      expect(error.requestId).toBe(id)
    },
  )
})
