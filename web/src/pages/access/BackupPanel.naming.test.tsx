import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { VaultClient } from '../../api/vault'
import { downloadBlob } from '../../download'
import { AccessPage } from './AccessPage'
import { BackupPanel } from './BackupPanel'

vi.mock('../../download', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../download')>()), downloadBlob: vi.fn(),
}))
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
let mounted: { container: HTMLDivElement; root: Root } | undefined

function render(element: React.ReactElement): HTMLDivElement {
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  mounted = { container, root }
  act(() => root.render(element))
  return container
}

async function settle(): Promise<void> {
  await act(async () => { await new Promise((resolve) => window.setTimeout(resolve, 0)) })
}

function blobBytes(blob: Blob): Promise<Uint8Array> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onerror = () => reject(reader.error)
    reader.onload = () => resolve(new Uint8Array(reader.result as ArrayBuffer))
    reader.readAsArrayBuffer(blob)
  })
}

afterEach(() => {
  if (mounted) act(() => mounted?.root.unmount())
  mounted?.container.remove()
  mounted = undefined
  vi.clearAllMocks()
})

describe('authorized object archive terminology', () => {
  it('explains the manifest and disaster-recovery limit without changing the export or browser handoff', async () => {
    const bytes = new Uint8Array([31, 139, 8, 42])
    const exportArchive = vi.fn(async () => new Response(bytes, {
      headers: { 'Content-Type': 'application/gzip', 'Content-Disposition': 'attachment; filename="original-name.tar.gz"' },
    }))
    const client = { exportArchive } as unknown as VaultClient
    const container = render(<BackupPanel client={client} bucket="images" />)

    expect(container.textContent).toContain('导出授权对象归档')
    expect(container.textContent).toContain('manifest.json')
    expect(container.textContent).toContain('不是灾难恢复备份')
    expect(container.textContent).not.toContain('可移植备份')
    const control = [...container.querySelectorAll('button')].find((item) => item.textContent === '下载 tar.gz')
    expect(control).toBeDefined()
    await act(async () => { control?.click() })
    await settle()

    expect(exportArchive).toHaveBeenCalledExactlyOnceWith('images', '')
    expect(downloadBlob).toHaveBeenCalledTimes(1)
    const [blob, filename] = vi.mocked(downloadBlob).mock.calls[0]
    expect(filename).toBe('original-name.tar.gz')
    expect(await blobBytes(blob)).toEqual(bytes)
    expect(container.textContent).not.toContain('已保存')
    expect(container.textContent).not.toContain('已完成')
  })

  it('names the access tab as an object archive and keeps its bucket selector local to those panels', async () => {
    const client = { listBuckets: vi.fn(async () => ['default', 'images']) } as unknown as VaultClient
    const container = render(<AccessPage client={client} />)
    await settle()

    expect(container.textContent).toContain('对象归档')
    expect(container.textContent).not.toContain('备份导出')
    expect(container.textContent).not.toContain('可移植备份')
    expect(container.textContent).toContain('不切换文件浏览范围')
    expect(container.textContent).toContain('分享链接')
    expect(container.textContent).toContain('公开图片')
    expect(container.textContent).toContain('资源 ACL')
    expect(client.listBuckets).toHaveBeenCalledTimes(1)
  })
})
