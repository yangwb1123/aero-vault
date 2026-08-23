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

export function ACLPanel({ client }: { client: VaultClient }): React.ReactElement {
  const [key, setKey] = React.useState('')
  const [kind, setKind] = React.useState<ResourceKind>('object')
  const [principal, setPrincipal] = React.useState<PrincipalType>('user')
  const [principalID, setPrincipalID] = React.useState('')
  const [actions, setActions] = React.useState('object:read,object:download')
  const [effect, setEffect] = React.useState<AccessEffect>('allow')
  const [inherit, setInherit] = React.useState(false)
  const [entries, setEntries] = React.useState<ACLEntry[]>()
  const [busy, setBusy] = React.useState('')
  const [error, setError] = React.useState<string>()

  const load = async () => run('list', async () => setEntries(await client.listACL(key.trim(), kind)))
  const grant = async () => run('grant', async () => {
    const values = actions.split(',').map((value) => value.trim()).filter(Boolean)
    if (values.length === 0) throw new Error('至少填写一个授权 action')
    await client.putACL({
      key: key.trim(), resource_kind: kind, principal_type: principal,
      principal_id: principalID.trim(), actions: values, effect, inherit,
    })
    setEntries(await client.listACL(key.trim(), kind))
  })
  const remove = async (id: string) => run(`delete:${id}`, async () => {
    await client.deleteACL(id)
    setEntries(await client.listACL(key.trim(), kind))
  })
  const run = async (label: string, action: () => Promise<void>) => {
    setBusy(label)
    setError(undefined)
    try { await action() } catch (reason) {
      setError(reason instanceof Error ? reason.message : 'ACL 操作失败')
    } finally { setBusy('') }
  }

  return (
    <div className="access-stack">
      <IrisCard variant="outline" header="资源授权规则">
        <form className="access-form" onSubmit={(event) => { event.preventDefault(); void grant() }}>
          <label className="access-field"><span>资源 Key</span><IrisInput required={kind !== 'bucket'} disabled={kind === 'bucket'} value={key} placeholder={kind === 'folder' ? 'team/docs/' : 'team/docs/report.pdf'} onChange={(event) => { setKey(event.target.value); setEntries(undefined) }} /></label>
          <AccessSelect<ResourceKind> label="资源类型" value={kind} options={kinds} onChange={(value) => {
            setKind(value)
            setEntries(undefined)
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
      {entries ? <ACLTable entries={entries} busy={busy} remove={remove} /> : null}
    </div>
  )
}

function ACLTable({ entries, busy, remove }: { entries: ACLEntry[]; busy: string; remove(id: string): Promise<void> }): React.ReactElement {
  return (
    <IrisCard variant="outline" header="当前资源 ACL">
      <div className="table-scroll"><table className="vault-table">
        <thead><tr><th>主体</th><th>Action</th><th>效果</th><th>继承</th><th>操作</th></tr></thead>
        <tbody>{entries.map((entry) => (
          <tr key={entry.id}><td>{entry.principal_type}{entry.principal_id ? `: ${entry.principal_id}` : ''}</td><td>{entry.action}</td>
            <td><strong className={entry.effect === 'deny' ? 'text-danger' : ''}>{entry.effect}</strong></td><td>{entry.inherit ? '是' : '否'}</td>
            <td><IrisButton size="sm" variant="ghost" disabled={Boolean(busy)} onClick={() => {
              if (window.confirm('删除这条 ACL 规则？')) void remove(entry.id)
            }}>删除</IrisButton></td></tr>
        ))}{entries.length === 0 ? <tr><td className="empty-cell" colSpan={5}>暂无 ACL 规则</td></tr> : null}</tbody>
      </table></div>
    </IrisCard>
  )
}
