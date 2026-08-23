import * as React from 'react'
import {
  IrisAlert,
  IrisButton,
  IrisCard,
  IrisCheckbox,
  IrisCopyButton,
  IrisInput,
  IrisNumberInput,
  IrisPasswordInput,
} from '@iris-ui-kit/react'
import type { Share } from '../../api/access'
import type { VaultClient } from '../../api/vault'

export function SharePanel({ client }: { client: VaultClient }): React.ReactElement {
  const [key, setKey] = React.useState('')
  const [name, setName] = React.useState('')
  const [password, setPassword] = React.useState('')
  const [ttl, setTTL] = React.useState<number | null>(3600)
  const [maxUses, setMaxUses] = React.useState<number | null>(null)
  const [preview, setPreview] = React.useState(true)
  const [download, setDownload] = React.useState(true)
  const [shares, setShares] = React.useState<Share[]>()
  const [createdURL, setCreatedURL] = React.useState('')
  const [busy, setBusy] = React.useState('')
  const [error, setError] = React.useState<string>()

  const load = async () => {
    if (!key.trim()) return
    await run('list', async () => setShares(await client.listShares(key.trim())))
  }

  const create = async () => {
    const objectKey = key.trim()
    if (!objectKey) return
    await run('create', async () => {
      const result = await client.createShare({
        key: objectKey,
        name: name.trim() || undefined,
        password: password || undefined,
        allow_preview: preview,
        allow_download: download,
        ttl_seconds: ttl && ttl > 0 ? ttl : undefined,
        max_uses: maxUses && maxUses > 0 ? maxUses : undefined,
      })
      setCreatedURL(result.url)
      setShares(await client.listShares(objectKey))
    })
  }

  const revoke = async (id: string) => {
    if (!window.confirm('撤销这个分享链接？撤销后访问会立即失效。')) return
    await run(`revoke:${id}`, async () => {
      await client.revokeShare(id)
      setShares(await client.listShares(key.trim()))
    })
  }

  const run = async (label: string, action: () => Promise<void>) => {
    setBusy(label)
    setError(undefined)
    try {
      await action()
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '分享操作失败')
    } finally {
      setBusy('')
    }
  }

  return (
    <div className="access-stack">
      <IrisCard variant="outline" header="创建对象分享链接">
        <form className="access-form" onSubmit={(event) => { event.preventDefault(); void create() }}>
          <label className="access-field access-wide"><span>对象 Key</span><IrisInput required value={key} placeholder="docs/report.pdf" onChange={(event) => {
            setKey(event.target.value)
            setShares(undefined)
            setCreatedURL('')
          }} /></label>
          <label className="access-field"><span>链接名称</span><IrisInput value={name} placeholder="外部评审" onChange={(event) => setName(event.target.value)} /></label>
          <label className="access-field"><span>访问密码（可选）</span><IrisPasswordInput value={password} onChange={(event) => setPassword(event.target.value)} /></label>
          <label className="access-field"><span>有效期（秒，空为永久）</span><IrisNumberInput min={1} value={ttl} onChange={setTTL} /></label>
          <label className="access-field"><span>最多使用次数（空为不限）</span><IrisNumberInput min={1} value={maxUses} onChange={setMaxUses} /></label>
          <div className="access-options access-wide">
            <IrisCheckbox checked={preview} onChange={setPreview}>允许预览</IrisCheckbox>
            <IrisCheckbox checked={download} onChange={setDownload}>允许下载</IrisCheckbox>
          </div>
          <div className="access-actions access-wide">
            <IrisButton type="button" variant="outline" loading={busy === 'list'} onClick={() => void load()}>刷新列表</IrisButton>
            <IrisButton type="submit" loading={busy === 'create'}>创建分享</IrisButton>
          </div>
        </form>
      </IrisCard>
      {error ? <IrisAlert tone="danger">{error}</IrisAlert> : null}
      {createdURL ? (
        <IrisAlert tone="success" title="分享链接已创建">
          <div className="access-link"><a href={createdURL} target="_blank" rel="noreferrer">{createdURL}</a><IrisCopyButton text={createdURL}>复制</IrisCopyButton></div>
        </IrisAlert>
      ) : null}
      {shares ? <ShareTable shares={shares} busy={busy} revoke={revoke} /> : null}
    </div>
  )
}

function ShareTable({ shares, busy, revoke }: { shares: Share[]; busy: string; revoke(id: string): Promise<void> }): React.ReactElement {
  return (
    <IrisCard variant="outline" header="当前对象的分享链接">
      <div className="table-scroll"><table className="vault-table">
        <thead><tr><th>名称 / ID</th><th>权限</th><th>使用量</th><th>过期时间</th><th>操作</th></tr></thead>
        <tbody>{shares.map((share) => (
          <tr key={share.id}>
            <td><strong>{share.name || '未命名'}</strong><div className="muted">{share.id}</div></td>
            <td>{share.allow_preview ? '预览' : ''}{share.allow_preview && share.allow_download ? ' / ' : ''}{share.allow_download ? '下载' : ''}</td>
            <td>{share.use_count} / {share.max_uses || '不限'}</td>
            <td>{formatExpiry(share.expires_at)}</td>
            <td><IrisButton size="sm" variant="ghost" disabled={Boolean(busy)} onClick={() => void revoke(share.id)}>撤销</IrisButton></td>
          </tr>
        ))}{shares.length === 0 ? <tr><td className="empty-cell" colSpan={5}>没有有效分享链接</td></tr> : null}</tbody>
      </table></div>
    </IrisCard>
  )
}

function formatExpiry(value?: string): string {
  if (!value || value.startsWith('0001-')) return '永久'
  return new Date(value).toLocaleString('zh-CN')
}
