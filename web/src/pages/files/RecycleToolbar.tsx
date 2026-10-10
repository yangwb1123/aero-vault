import { IrisButton, IrisCard } from '@iris-ui-kit/react'

export function RecycleToolbar({ count, busy, clear, restore }: {
  count: number
  busy: boolean
  clear(): void
  restore(): Promise<void>
}): React.ReactElement | null {
  if (!count) return null
  return <IrisCard variant="outline" header={`已选择 ${count} 个回收站文件`}>
    <div className="batch-toolbar">
      <IrisButton variant="outline" disabled={busy} onClick={() => {
        if (window.confirm(`恢复选中的 ${count} 个文件？不会覆盖已有同名文件，失败项将保持选中。`)) void restore()
      }}>恢复所选</IrisButton>
      <IrisButton variant="ghost" disabled={busy} onClick={clear}>取消选择</IrisButton>
    </div>
    <p className="muted">逐项恢复最近删除的对象，不恢复任意历史版本；权限、配额和同名文件冲突仍由后端检查。</p>
  </IrisCard>
}
