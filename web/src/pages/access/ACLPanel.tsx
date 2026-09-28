import * as React from 'react'
import { IrisAlert, IrisButton, IrisCard, IrisCheckbox, IrisInput } from '@iris-ui-kit/react'
import type { ACLEntry, AccessEffect, PrincipalType, ResourceKind } from '../../api/access'
import type { VaultClient } from '../../api/vault'
import { AccessSelect, type SelectOption } from './AccessSelect'

const kinds: Array<SelectOption<ResourceKind>> = [
  { value: 'object', label: '对象' }, { value: 'folder', label: '文件夹' }, { value: 'bucket', label: '存储桶' },
]
const principals: Array<SelectOption<PrincipalType>> = [
  { value: 'user', label: '用户' }, { value: 'department', label: '部门' }, { value: 'group', label: 'Snaplink Group' },
  { value: 'role', label: 'Snaplink Role' }, { value: 'authenticated', label: '所有已认证用户' }, { value: 'everyone', label: '所有人' },
]
const effects: Array<SelectOption<AccessEffect>> = [
  { value: 'allow', label: '允许' }, { value: 'deny', label: '拒绝（优先）' },
]

interface ACLTarget {
  client: VaultClient
  bucket: string
  key: string
  kind: ResourceKind
  scopeKey: string
}

interface ACLResults {
  client: VaultClient
  scopeKey: string
  entries: ACLEntry[]
}

const aclScopeKey = (bucket: string, key: string, kind: ResourceKind): string =>
  JSON.stringify([bucket, key.trim(), kind])

export function ACLPanel({ client, bucket }: { client: VaultClient; bucket: string }): React.ReactElement {
  const [key, setKey] = React.useState('')
  const [kind, setKind] = React.useState<ResourceKind>('object')
  const [principal, setPrincipal] = React.useState<PrincipalType>('user')
  const [principalID, setPrincipalID] = React.useState('')
  const [actions, setActions] = React.useState('object:read,object:download')
  const [effect, setEffect] = React.useState<AccessEffect>('allow')
  const [inherit, setInherit] = React.useState(false)
  const scopeKey = aclScopeKey(bucket, key, kind)
  const [result, setResult] = React.useState<ACLResults>()
  const [busy, setBusy] = React.useState('')
  const [error, setError] = React.useState<string>()
  const currentScopeRef = React.useRef({ client, scopeKey })
  const requestRef = React.useRef(0)
  const entries = result?.client === client && result.scopeKey === scopeKey ? result.entries : undefined

  React.useLayoutEffect(() => {
    if (currentScopeRef.current.client === client && currentScopeRef.current.scopeKey === scopeKey) return
    currentScopeRef.current = { client, scopeKey }
    requestRef.current += 1
    setResult(undefined)
    setBusy('')
    setError(undefined)
  }, [client, scopeKey])

  const captureTarget = (): ACLTarget => ({
    client, bucket, key: key.trim(), kind, scopeKey,
  })
  const isCurrentScope = (target: ACLTarget) =>
    currentScopeRef.current.client === target.client && currentScopeRef.current.scopeKey === target.scopeKey
  const runScoped = async (
    label: string,
    target: ACLTarget,
    action: (isCurrent: () => boolean) => Promise<void>,
  ) => {
    if (!isCurrentScope(target)) return
    const requestID = ++requestRef.current
    const isCurrent = () => requestRef.current === requestID && isCurrentScope(target)
    setBusy(label)
    setError(undefined)
    try { await action(isCurrent) } catch (reason) {
      if (isCurrent()) setError(reason instanceof Error ? reason.message : 'ACL 操作失败')
    } finally {
      if (isCurrent()) setBusy((value) => value === label ? '' : value)
    }
  }
  const load = () => {
    const target = captureTarget()
    return runScoped('list', target, async (isCurrent) => {
      const nextEntries = await target.client.listACL(target.bucket, target.key, target.kind)
      if (isCurrent()) setResult({ client: target.client, scopeKey: target.scopeKey, entries: nextEntries })
    })
  }
  const grant = () => {
    const target = captureTarget()
    const values = actions.split(',').map((value) => value.trim()).filter(Boolean)
    const input = {
      bucket: target.bucket, key: target.key, resource_kind: target.kind,
      principal_type: principal, principal_id: principalID.trim(), actions: values, effect, inherit,
    }
    return runScoped('grant', target, async (isCurrent) => {
      if (values.length === 0) throw new Error('至少填写一个授权 action')
      await target.client.putACL(input)
      if (!isCurrent()) return
      const nextEntries = await target.client.listACL(target.bucket, target.key, target.kind)
      if (isCurrent()) setResult({ client: target.client, scopeKey: target.scopeKey, entries: nextEntries })
    })
  }
  const remove = (id: string, rowScopeKey: string) => {
    const target = captureTarget()
    if (rowScopeKey !== target.scopeKey) return
    return runScoped(`delete:${id}`, target, async (isCurrent) => {
      await target.client.deleteACL(id)
      if (!isCurrent()) return
      const nextEntries = await target.client.listACL(target.bucket, target.key, target.kind)
      if (isCurrent()) setResult({ client: target.client, scopeKey: target.scopeKey, entries: nextEntries })
    })
  }

  return (
    <div className="access-stack">
      <IrisCard variant="outline" header="资源授权规则">
        <form className="access-form" onSubmit={(event) => { event.preventDefault(); void grant() }}>
          <label className="access-field"><span>资源 Key</span><IrisInput required={kind !== 'bucket'} disabled={kind === 'bucket'} value={key} placeholder={kind === 'folder' ? 'team/docs/' : 'team/docs/report.pdf'} onChange={(event) => setKey(event.target.value)} /></label>
          <AccessSelect<ResourceKind> label="资源类型" value={kind} options={kinds} onChange={(value) => {
            setKind(value)
            if (value === 'bucket') setKey('')
          }} />
          <AccessSelect<PrincipalType> label="主体类型" value={principal} options={principals} onChange={(value) => {
            setPrincipal(value)
            if (value === 'authenticated' || value === 'everyone') setPrincipalID('')
          }} />
          <label className="access-field"><span>主体 ID</span><IrisInput required={principal !== 'authenticated' && principal !== 'everyone'} disabled={principal === 'authenticated' || principal === 'everyone'} value={principalID} placeholder="subject / department / group / role" onChange={(event) => setPrincipalID(event.target.value)} /></label>
          <label className="access-field"><span>Actions（逗号分隔）</span><IrisInput required value={actions} onChange={(event) => setActions(event.target.value)} /></label>
          <AccessSelect<AccessEffect> label="授权效果" value={effect} options={effects} onChange={setEffect} />
          <div className="access-options access-wide"><IrisCheckbox checked={inherit} onChange={setInherit}>继承到下级资源</IrisCheckbox></div>
          <div className="access-actions access-wide">
            <IrisButton type="button" variant="outline" loading={busy === 'list'} onClick={() => void load()}>读取规则</IrisButton>
            <IrisButton type="submit" loading={busy === 'grant'}>添加规则</IrisButton>
          </div>
        </form>
      </IrisCard>
      {error ? <IrisAlert tone="danger">{error}</IrisAlert> : null}
      {entries ? <ACLTable entries={entries} busy={busy} targetKey={scopeKey} remove={remove} /> : null}
    </div>
  )
}

function ACLTable({ entries, busy, targetKey, remove }: { entries: ACLEntry[]; busy: string; targetKey: string; remove(id: string, scopeKey: string): Promise<void> | void }): React.ReactElement {
  return (
    <IrisCard variant="outline" header="当前资源 ACL">
      <div className="table-scroll"><table className="vault-table">
        <thead><tr><th>主体</th><th>Action</th><th>效果</th><th>继承</th><th>操作</th></tr></thead>
        <tbody>{entries.map((entry) => (
          <tr key={entry.id}><td>{entry.principal_type}{entry.principal_id ? `: ${entry.principal_id}` : ''}</td><td>{entry.action}</td>
            <td><strong className={entry.effect === 'deny' ? 'text-danger' : ''}>{entry.effect}</strong></td><td>{entry.inherit ? '是' : '否'}</td>
            <td><IrisButton size="sm" variant="ghost" disabled={Boolean(busy)} onClick={() => {
              if (window.confirm('删除这条 ACL 规则？')) void remove(entry.id, targetKey)
            }}>删除</IrisButton></td></tr>
        ))}{entries.length === 0 ? <tr><td className="empty-cell" colSpan={5}>暂无 ACL 规则</td></tr> : null}</tbody>
      </table></div>
    </IrisCard>
  )
}
