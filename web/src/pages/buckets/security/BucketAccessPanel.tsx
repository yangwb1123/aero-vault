import * as React from 'react'
import { IrisAlert, IrisButton, IrisCard, IrisTextarea } from '@iris-ui-kit/react'
import type { BucketACL, BucketSecurityClient } from '../../../api/bucketSecurity'

export function BucketAccessPanel({ api, bucket, acl: initialACL, policy: initialPolicy, onSaved }: {
  api: BucketSecurityClient; bucket: string; acl: BucketACL; policy: string; onSaved(): void
}): React.ReactElement {
  const [acl, setACL] = React.useState(initialACL)
  const [policy, setPolicy] = React.useState(prettyPolicy(initialPolicy))
  const [busy, setBusy] = React.useState('')
  const [message, setMessage] = React.useState<{ tone: 'success' | 'danger'; text: string }>()

  const run = async (kind: string, operation: () => Promise<void>, success: string) => {
    setBusy(kind); setMessage(undefined)
    try {
      await operation(); setMessage({ tone: 'success', text: success }); onSaved(); return true
    } catch (reason) {
      setMessage({ tone: 'danger', text: reason instanceof Error ? reason.message : '存储桶访问设置更新失败' }); return false
    } finally { setBusy('') }
  }

  const savePolicy = () => {
    const value = policy.trim()
    if (!value) { setMessage({ tone: 'danger', text: '空策略请使用“清除策略”，避免误操作。' }); return }
    try { JSON.parse(value) } catch { setMessage({ tone: 'danger', text: '策略必须是有效 JSON。' }); return }
    void run('policy', () => api.putPolicy(bucket, value), 'Bucket Policy 已更新。')
  }
  const clearPolicy = async () => {
    if (!window.confirm('清除当前 Bucket Policy？清除后将回到 ACL 和主体权限边界。')) return
    if (await run('clear', () => api.deletePolicy(bucket), 'Bucket Policy 已清除。')) setPolicy('')
  }

  return (
    <div className="bucket-security-stack">
      {message ? <IrisAlert tone={message.tone}>{message.text}</IrisAlert> : null}
      {acl === 'public-read' || acl === 'public-read-write' ? <IrisAlert tone="warning" title="公开访问已启用">匿名调用方可能读取此存储桶；public-read-write 还允许写入，请谨慎使用。</IrisAlert> : null}
      <IrisCard variant="outline" header="Canned ACL">
        <form className="bucket-security-form" onSubmit={(event) => { event.preventDefault(); void run('acl', () => api.putACL(bucket, acl), '存储桶 ACL 已更新。') }}>
          <label><span>访问级别</span><select value={acl} onChange={(event) => setACL(event.target.value as BucketACL)}><option value="private">private</option><option value="authenticated-read">authenticated-read</option><option value="public-read">public-read</option><option value="public-read-write">public-read-write</option></select></label>
          <p>ACL 是粗粒度默认边界；资源 ACL 的显式 deny 与 Bucket Policy 仍可进一步限制访问。</p>
          <IrisButton type="submit" variant="outline" loading={busy === 'acl'}>保存 ACL</IrisButton>
        </form>
      </IrisCard>
      <IrisCard variant="outline" header="IAM 风格 Bucket Policy">
        <p className="muted">服务端按显式 deny、allow、资源 ARN 与来源 IP 执行策略；无匹配 allow 时隐式拒绝。</p>
        <IrisTextarea rows={12} value={policy} spellCheck={false} placeholder='{"Version":"2012-10-17","Statement":[]}' onChange={(event) => setPolicy(event.target.value)} />
        <div className="bucket-security-actions"><IrisButton variant="ghost" loading={busy === 'clear'} onClick={() => void clearPolicy()}>清除策略</IrisButton><IrisButton loading={busy === 'policy'} onClick={savePolicy}>保存策略</IrisButton></div>
      </IrisCard>
    </div>
  )
}

function prettyPolicy(policy: string): string {
  if (!policy.trim()) return ''
  try { return JSON.stringify(JSON.parse(policy), null, 2) } catch { return policy }
}
