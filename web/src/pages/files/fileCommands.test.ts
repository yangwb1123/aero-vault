import { describe, expect, it, vi } from 'vitest'
import { VaultClient } from '../../api/vault'
import { createFileCommands } from './fileCommands'

function client(base: string, token: string): { client: VaultClient; fetcher: ReturnType<typeof vi.fn<typeof fetch>> } {
  const fetcher = vi.fn<typeof fetch>(async () => new Response('{}', { status: 200 }))
  return { client: new VaultClient(base, () => token, fetcher), fetcher }
}

describe('page-scoped file commands', () => {
  it('uses the existing default-bucket folder and object mutation contracts', async () => {
    const target = client('/v1', 'current-token')
    const commands = createFileCommands(target.client)
    await commands.createFolder('docs/')
    await commands.removeFolder('docs/')
    await commands.removeFile('docs/a b.txt')
    await commands.restoreFile('docs/a b.txt')

    expect(target.fetcher.mock.calls.map(([url, init]) => [url, init?.method])).toEqual([
      ['/v1/folders/docs/', 'POST'],
      ['/v1/folders/docs/', 'DELETE'],
      ['/v1/files/docs/a%20b.txt', 'DELETE'],
      ['/v1/files/docs/a%20b.txt/restore', 'POST'],
    ])
    expect(target.fetcher.mock.calls[0][1]?.body).toBeUndefined()
    for (const [, init] of target.fetcher.mock.calls) {
      expect(new Headers(init?.headers).get('Authorization')).toBe('Bearer current-token')
      expect(init?.credentials).toBe('same-origin')
    }
  })

  it('keeps old commands bound to their original client after a new client is created', async () => {
    const old = client('/old/v1', 'old-token')
    const current = client('/new/v1', 'new-token')
    const oldCommands = createFileCommands(old.client)
    const currentCommands = createFileCommands(current.client)
    await oldCommands.removeFile('old.txt')
    await currentCommands.removeFile('new.txt')

    expect(old.fetcher).toHaveBeenCalledTimes(1)
    expect(current.fetcher).toHaveBeenCalledTimes(1)
    expect(old.fetcher.mock.calls[0][0]).toBe('/old/v1/files/old.txt')
    expect(current.fetcher.mock.calls[0][0]).toBe('/new/v1/files/new.txt')
  })
})
