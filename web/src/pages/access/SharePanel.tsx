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

interface ShareTarget {
  client: VaultClient
  bucket: string
  key: string
  scopeKey: string
}

interface ScopedShares {
  client: VaultClient
  scopeKey: string
  shares: Share[]
}

interface ScopedURL {
  client: VaultClient
  scopeKey: string
  id: string
  url: string
}

const shareScopeKey = (bucket: string, key: string): string => JSON.stringify([bucket, key])

export function SharePanel({ client, bucket, objectKey }: { client: VaultClient; bucket: string; objectKey?: string }): React.ReactElement {
  const [inputKey, setKey] = React.useState('')
  const key = objectKey ?? inputKey.trim()
  const [name, setName] = React.useState('')
  const [password, setPassword] = React.useState('')
  const [ttl, setTTL] = React.useState<number | null>(3600)
  const [maxUses, setMaxUses] = React.useState<number | null>(null)
  const [preview, setPreview] = React.useState(true)
  const [download, setDownload] = React.useState(true)
  const [sharesState, setSharesState] = React.useState<ScopedShares>()
  const [createdShare, setCreatedShare] = React.useState<ScopedURL>()
  const [busy, setBusy] = React.useState('')
  const [error, setError] = React.useState<string>()
  const scopeKey = shareScopeKey(bucket, key)
  const shares = sharesState?.client === client && sharesState.scopeKey === scopeKey
    ? sharesState.shares
    : undefined
  const createdURL = createdShare?.client === client && createdShare.scopeKey === scopeKey
    ? createdShare.url
    : ''
  const scopeRef = React.useRef({ client, scopeKey })
  const requestRef = React.useRef(0)
  const busyRef = React.useRef(false)

  React.useLayoutEffect(() => {
    if (scopeRef.current.client === client && scopeRef.current.scopeKey === scopeKey) return
    scopeRef.current = { client, scopeKey }
    requestRef.current += 1
    busyRef.current = false
    setSharesState(undefined)
    setCreatedShare(undefined)
    setName('')
    setPassword('')
    setTTL(3600)
    setMaxUses(null)
    setPreview(true)
    setDownload(true)
    setBusy('')
    setError(undefined)
  }, [client, scopeKey])

  React.useEffect(() => () => {
    requestRef.current += 1
    busyRef.current = false
  }, [])

  const captureTarget = (): ShareTarget => ({ client, bucket, key, scopeKey })
  const isCurrentScope = (target: ShareTarget) =>
    scopeRef.current.client === target.client && scopeRef.current.scopeKey === target.scopeKey
  const run = async (
    label: string,
    target: ShareTarget,
    action: (isCurrent: () => boolean) => Promise<void>,
  ) => {
    if (!isCurrentScope(target) || busyRef.current) return
    busyRef.current = true
    const requestID = ++requestRef.current
    const isCurrent = () => requestRef.current === requestID && isCurrentScope(target)
    setBusy(label)
    setError(undefined)
    try {
      await action(isCurrent)
    } catch (reason) {
      if (isCurrent()) setError(reason instanceof Error ? reason.message : '分享操作失败')
    } finally {
      if (isCurrent()) {
        busyRef.current = false
        setBusy((value) => value === label ? '' : value)
      }
    }
  }

  const load = () => {
    const target = captureTarget()
    if (!target.key) return
    return run('list', target, async (isCurrent) => {
      const nextShares = await target.client.listShares(target.bucket, target.key)
      if (isCurrent()) setSharesState({ client: target.client, scopeKey: target.scopeKey, shares: nextShares })
    })
  }

  React.useEffect(() => {
    if (objectKey !== undefined) void load()
  }, [client, bucket, scopeKey, objectKey])

  const create = () => {
    const target = captureTarget()
    if (!target.key || busyRef.current) return
    if (!preview && !download) {
      setError('至少允许预览或下载之一。')
      return
    }
    setCreatedShare(undefined)
    return run('create', target, async (isCurrent) => {
      const result = await target.client.createShare({
        bucket: target.bucket,
        key: target.key,
        name: name.trim() || undefined,
        password: password || undefined,
        allow_preview: preview,
        allow_download: download,
        ttl_seconds: ttl && ttl > 0 ? ttl : undefined,
        max_uses: maxUses && maxUses > 0 ? maxUses : undefined,
      })
      if (!isCurrent()) return
      setCreatedShare({ client: target.client, scopeKey: target.scopeKey, id: result.share.id, url: result.url })
      const nextShares = await target.client.listShares(target.bucket, target.key)
      if (isCurrent()) setSharesState({ client: target.client, scopeKey: target.scopeKey, shares: nextShares })
    })
  }

  const revoke = (id: string, rowScopeKey: string) => {
    const target = captureTarget()
    if (target.scopeKey !== rowScopeKey || !window.confirm('撤销这个分享链接？撤销后访问会立即失效。')) return
    return run(`revoke:${id}`, target, async (isCurrent) => {
      await target.client.revokeShare(id)
      if (!isCurrent()) return
      setCreatedShare((current) => current?.id === id ? undefined : current)
      const nextShares = await target.client.listShares(target.bucket, target.key)
      if (isCurrent()) setSharesState({ client: target.client, scopeKey: target.scopeKey, shares: nextShares })
    })
  }

  return (
    <div className="access-stack">
      <IrisCard variant="outline" header="创建对象分享链接">
        <form className="access-form" onSubmit={(event) => { event.preventDefault(); void create() }}>
          <label className="access-field access-wide"><span>对象 Key</span><IrisInput required readOnly={objectKey !== undefined} disabled={busy === 'create'} value={objectKey ?? inputKey} placeholder="docs/report.pdf" onChange={(event) => setKey(event.target.value)} /></label>
          <label className="access-field"><span>链接名称</span><IrisInput value={name} placeholder="外部评审" onChange={(event) => setName(event.target.value)} /></label>
          <label className="access-field"><span>访问密码（可选）</span><IrisPasswordInput value={password} onChange={(event) => setPassword(event.target.value)} /></label>
          <label className="access-field"><span>有效期（秒，空为永久）</span><IrisNumberInput min={1} value={ttl} onChange={setTTL} /></label>
          <label className="access-field"><span>最多使用次数（空为不限）</span><IrisNumberInput min={1} value={maxUses} onChange={setMaxUses} /></label>
          <div className="access-options access-wide">
            <IrisCheckbox checked={preview} onChange={setPreview}>允许预览</IrisCheckbox>
            <IrisCheckbox checked={download} onChange={setDownload}>允许下载</IrisCheckbox>
          </div>
          <div className="access-actions access-wide">
            <IrisButton type="button" variant="outline" loading={busy === 'list'} disabled={Boolean(busy)} onClick={() => void load()}>刷新列表</IrisButton>
            <IrisButton type="submit" loading={busy === 'create'} disabled={Boolean(busy)}>创建分享</IrisButton>
          </div>
        </form>
      </IrisCard>
      {error ? <IrisAlert tone="danger">{error}</IrisAlert> : null}
      {createdURL ? (
        <IrisAlert tone="success" title="分享链接已创建">
          <div className="access-link"><a href={createdURL} target="_blank" rel="noreferrer">{createdURL}</a><IrisCopyButton text={createdURL}>复制</IrisCopyButton></div>
        </IrisAlert>
      ) : null}
      {shares ? <ShareTable shares={shares} busy={busy} targetKey={scopeKey} revoke={revoke} /> : null}
    </div>
  )
}

function ShareTable({ shares, busy, targetKey, revoke }: { shares: Share[]; busy: string; targetKey: string; revoke(id: string, scopeKey: string): Promise<void> | void }): React.ReactElement {
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
            <td><IrisButton size="sm" variant="ghost" disabled={Boolean(busy)} onClick={() => void revoke(share.id, targetKey)}>撤销</IrisButton></td>
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
