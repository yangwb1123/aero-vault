import * as React from 'react'
import { IrisAlert, IrisButton, IrisCard, IrisInput } from '@iris-ui-kit/react'
import type { PublicAsset } from '../../api/access'
import type { VaultClient } from '../../api/vault'
import { PageError, PageLoading } from '../../components/Page'
import { useResource } from '../../hooks/useResource'

export function AssetPanel({ client }: { client: VaultClient }): React.ReactElement {
  const [key, setKey] = React.useState('')
  const [slug, setSlug] = React.useState('')
  const [busy, setBusy] = React.useState('')
  const [message, setMessage] = React.useState<{ tone: 'success' | 'danger'; text: string }>()
  const load = React.useCallback(() => client.listAssets(), [client])
  const resource = useResource(load)

  const run = async (label: string, action: () => Promise<void>) => {
    setBusy(label)
    setMessage(undefined)
    try {
      await action()
      setMessage({ tone: 'success', text: '公开资源配置已更新。' })
      resource.reload()
    } catch (reason) {
      setMessage({ tone: 'danger', text: reason instanceof Error ? reason.message : '公开资源操作失败' })
    } finally {
      setBusy('')
    }
  }

  return (
    <div className="access-stack">
      <IrisCard variant="outline" header="发布稳定公开图片">
        <form className="access-form" onSubmit={(event) => {
          event.preventDefault()
          if (key.trim() && slug.trim()) void run('publish', () => client.publishAsset(key.trim(), slug.trim()).then(() => undefined))
        }}>
          <label className="access-field"><span>图片对象 Key</span><IrisInput required value={key} placeholder="images/hero.jpg" onChange={(event) => setKey(event.target.value)} /></label>
          <label className="access-field"><span>公开 Slug</span><IrisInput required value={slug} placeholder="blog/hero.jpg" onChange={(event) => setSlug(event.target.value)} /></label>
          <div className="access-actions access-wide"><IrisButton type="submit" loading={busy === 'publish'}>发布图片</IrisButton></div>
        </form>
      </IrisCard>
      {message ? <IrisAlert tone={message.tone}>{message.text}</IrisAlert> : null}
      {resource.loading ? <PageLoading /> : null}
      {resource.error ? <PageError error={resource.error} retry={resource.reload} /> : null}
      {resource.data ? <AssetTable assets={resource.data} busy={busy} unpublish={(value) => run(`delete:${value}`, () => client.unpublishAsset(value))} /> : null}
    </div>
  )
}

function AssetTable({ assets, busy, unpublish }: { assets: PublicAsset[]; busy: string; unpublish(slug: string): Promise<void> }): React.ReactElement {
  return (
    <IrisCard variant="outline" header="已发布图片">
      <div className="table-scroll"><table className="vault-table">
        <thead><tr><th>Slug</th><th>对象</th><th>发布者</th><th>发布时间</th><th>操作</th></tr></thead>
        <tbody>{assets.map((asset) => (
          <tr key={asset.id}>
            <td><a href={assetURL(asset.slug)} target="_blank" rel="noreferrer">{asset.slug}</a></td>
            <td>{asset.key}</td><td>{asset.published_by}</td>
            <td>{new Date(asset.published_at).toLocaleString('zh-CN')}</td>
            <td><IrisButton size="sm" variant="ghost" disabled={Boolean(busy)} onClick={() => {
              if (window.confirm(`取消公开发布 ${asset.slug}？`)) void unpublish(asset.slug)
            }}>取消发布</IrisButton></td>
          </tr>
        ))}{assets.length === 0 ? <tr><td className="empty-cell" colSpan={5}>暂无公开图片</td></tr> : null}</tbody>
      </table></div>
    </IrisCard>
  )
}

const assetURL = (slug: string): string => `/public/assets/${slug.split('/').map(encodeURIComponent).join('/')}`
