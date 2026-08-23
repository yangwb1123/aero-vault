import * as React from 'react'
import { IrisButton, IrisCard, IrisInput } from '@iris-ui-kit/react'

export function BatchToolbar({ keys, busy, clear, applyTags, remove }: {
  keys: string[]
  busy: boolean
  clear(): void
  applyTags(tagKey: string, tagValue: string): Promise<void>
  remove(): Promise<void>
}): React.ReactElement | null {
  const [tagKey, setTagKey] = React.useState('')
  const [tagValue, setTagValue] = React.useState('')
  if (keys.length === 0) return null
  return (
    <IrisCard variant="outline" header={`已选择 ${keys.length} 个对象`}>
      <div className="batch-toolbar">
        <IrisInput value={tagKey} placeholder="标签键" disabled={busy} onChange={(event) => setTagKey(event.target.value)} />
        <IrisInput value={tagValue} placeholder="标签值" disabled={busy} onChange={(event) => setTagValue(event.target.value)} />
        <IrisButton variant="outline" disabled={busy || !tagKey.trim()} onClick={() => void applyTags(tagKey.trim(), tagValue)}>替换标签</IrisButton>
        <IrisButton variant="outline" className="text-danger" disabled={busy} onClick={() => {
          if (window.confirm(`将选中的 ${keys.length} 个对象移入回收站？`)) void remove()
        }}>批量删除</IrisButton>
        <IrisButton variant="ghost" disabled={busy} onClick={clear}>取消选择</IrisButton>
      </div>
      <p className="muted">批量标签会用当前键值替换所选对象的完整标签集合。</p>
    </IrisCard>
  )
}
