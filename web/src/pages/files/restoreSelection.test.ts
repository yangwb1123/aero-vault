import { describe, expect, it, vi } from 'vitest'
import { VaultApiError, VaultClient } from '../../api/vault'
import { restoreSelection, restoreSelectionMessage } from './restoreSelection'

function deferred<T>(): { promise: Promise<T>; resolve(value: T): void } {
  let resolve: (value: T) => void = () => undefined
  const promise = new Promise<T>((accept) => { resolve = accept })
  return { promise, resolve }
}

describe('selected recycle-file restoration', () => {
  it('uses the existing restore route, encodes keys and preserves credentials', async () => {
    const fetcher = vi.fn<typeof fetch>(async () => new Response('{}', { status: 200 }))
    const client = new VaultClient('/v1', () => 'original-token', fetcher)
    await expect(restoreSelection(client, ['docs/a b.txt'], () => true)).resolves.toEqual([
      { key: 'docs/a b.txt', restored: true },
    ])
    expect(fetcher.mock.calls[0][0]).toBe('/v1/files/docs/a%20b.txt/restore')
    expect(fetcher.mock.calls[0][1]?.method).toBe('POST')
    expect(new Headers(fetcher.mock.calls[0][1]?.headers).get('Authorization')).toBe('Bearer original-token')
  })

  it('deduplicates keys and sends only one restore at a time', async () => {
    const first = deferred<void>()
    const restoreObject = vi.fn((key: string) => key === 'a' ? first.promise : Promise.resolve())
    const operation = restoreSelection({ restoreObject }, ['a', 'a', 'b'], () => true)
    expect(restoreObject.mock.calls).toEqual([['a']])
    first.resolve()
    await expect(operation).resolves.toEqual([{ key: 'a', restored: true }, { key: 'b', restored: true }])
    expect(restoreObject.mock.calls).toEqual([['a'], ['b']])
  })

  it('records forbidden/conflict/quota failures and continues to other keys', async () => {
    const restoreObject = vi.fn<(key: string) => Promise<void>>()
      .mockRejectedValueOnce(new VaultApiError(403, 'Forbidden', 'denied'))
      .mockRejectedValueOnce(new VaultApiError(412, 'PreconditionFailed', 'already exists'))
      .mockRejectedValueOnce(new VaultApiError(507, 'QuotaExceeded', 'quota exceeded'))
      .mockResolvedValueOnce(undefined)
    const results = await restoreSelection({ restoreObject }, ['denied', 'conflict', 'quota', 'ok'], () => true)
    expect(results).toEqual([
      { key: 'denied', restored: false, error: 'denied' },
      { key: 'conflict', restored: false, error: 'already exists' },
      { key: 'quota', restored: false, error: 'quota exceeded' },
      { key: 'ok', restored: true },
    ])
    expect(restoreSelectionMessage(results)).toMatchObject({ tone: 'danger' })
  })

  it('does not start a cancelled selection', async () => {
    const restoreObject = vi.fn(async () => undefined)
    await expect(restoreSelection({ restoreObject }, ['a'], () => false)).resolves.toEqual([])
    expect(restoreObject).not.toHaveBeenCalled()
  })

  it('stops unsent keys when the scope changes while a request is in flight', async () => {
    let current = true
    const first = deferred<void>()
    const restoreObject = vi.fn(() => first.promise)
    const operation = restoreSelection({ restoreObject }, ['a', 'b'], () => current)
    current = false
    first.resolve()
    await operation
    expect(restoreObject.mock.calls).toEqual([['a']])
  })

  it('summarizes successes and bounds failure details', () => {
    expect(restoreSelectionMessage([{ key: 'ok', restored: true }])).toEqual({ tone: 'success', text: '已恢复 1 个文件。' })
    const message = restoreSelectionMessage(Array.from({ length: 4 }, (_, i) => ({
      key: `failure-${i}`, restored: false, error: `reason-${i}`,
    })))
    expect(message.text).toContain('4 个失败')
    expect(message.text).toContain('reason-2')
    expect(message.text).not.toContain('reason-3')
  })
})
