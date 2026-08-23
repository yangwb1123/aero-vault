import { IrisButton, IrisCard, IrisCheckbox } from '@iris-ui-kit/react'
import type { FilePage, VaultObject } from '../../api/vault'

const formatBytes = (value: number): string =>
  new Intl.NumberFormat('zh-CN', { notation: 'compact', style: 'unit', unit: 'byte' }).format(value)

export function FileTable({ page, deleted, selected, busy, select, open, download, remove, restore }: {
  page: FilePage
  deleted: boolean
  selected: Set<string>
  busy: boolean
  select(keys: string[], checked: boolean): void
  open(key: string): void
  download(item: VaultObject): void
  remove(key: string): void
  restore(key: string): void
}): React.ReactElement {
  const keys = page.objects.map((item) => item.key)
  const allSelected = keys.length > 0 && keys.every((key) => selected.has(key))
  return (
    <IrisCard variant="outline">
      <div className="table-scroll"><table className="vault-table">
        <thead><tr><th className="selection-cell">{!deleted ? <IrisCheckbox aria-label="选择当前页全部对象" checked={allSelected} onChange={(checked) => select(keys, checked)} /> : null}</th><th>Key</th><th>大小</th><th>类型</th><th>更新时间</th><th>操作</th></tr></thead>
        <tbody>{page.objects.map((item) => (
          <tr key={`${item.bucket}:${item.key}`}>
            <td className="selection-cell">{!deleted ? <IrisCheckbox aria-label={`选择 ${item.key}`} checked={selected.has(item.key)} onChange={(checked) => select([item.key], checked)} /> : null}</td>
            <td><strong>{item.key}</strong><div className="muted">{item.bucket}</div></td>
            <td>{formatBytes(item.size)}</td><td>{item.content_type || 'application/octet-stream'}</td>
            <td>{new Date(item.updated_at).toLocaleString('zh-CN')}</td>
            <td className="row-actions"><IrisButton size="sm" variant="ghost" disabled={busy} onClick={() => open(item.key)}>详情</IrisButton>
              {deleted ? <IrisButton size="sm" variant="ghost" disabled={busy} onClick={() => restore(item.key)}>恢复</IrisButton> : <>
                <IrisButton size="sm" variant="ghost" disabled={busy} onClick={() => download(item)}>下载</IrisButton>
                <IrisButton size="sm" variant="ghost" disabled={busy} onClick={() => { if (window.confirm(`软删除 ${item.key}？`)) remove(item.key) }}>删除</IrisButton>
              </>}
            </td>
          </tr>
        ))}{page.objects.length === 0 ? <tr><td colSpan={6} className="empty-cell">没有匹配文件</td></tr> : null}</tbody>
      </table></div>
      {page.has_more ? <div className="table-note">当前仅显示前 200 项，请进入目录或缩小路径范围。</div> : null}
    </IrisCard>
  )
}
