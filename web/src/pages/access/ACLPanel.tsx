import * as React from 'react'
import { IrisAlert, IrisButton, IrisCard } from '@iris-ui-kit/react'
import type { ACLEntry, PutACLInput, ResourceKind } from '../../api/access'
import type { VaultClient } from '../../api/vault'
import { ACLRuleForm, initialACLFields } from './ACLRuleForm'
import { useACLRules } from './useACLRules'

export function ACLPanel({ client, bucket, objectKey }: { client: VaultClient; bucket: string; objectKey?: string }): React.ReactElement {
  const [inputKey, setKey] = React.useState('')
  const [inputKind, setKind] = React.useState<ResourceKind>('object')
  const [fields, setFields] = React.useState(initialACLFields)
  const bound = objectKey !== undefined
  const key = objectKey ?? (inputKind === 'bucket' ? '' : inputKey.trim())
  const kind = bound ? 'object' : inputKind
  const rules = useACLRules(client, bucket, key, kind, bound)
  React.useLayoutEffect(() => { setFields(initialACLFields()) }, [client, bucket, key, kind])
  const grant = () => {
    const actions = [...new Set(fields.actions.split(',').map((value) => value.trim()).filter(Boolean))]
    if (actions.length && fields.effect === 'deny' && !window.confirm(`为 ${bucket}/${key} 添加拒绝规则？拒绝优先，可能令自己失去访问或管理能力，需要有权主体协助恢复。`)) return
    const input: PutACLInput = { bucket, key, resource_kind: kind, principal_type: fields.principal,
      principal_id: fields.principalID.trim(), actions, effect: fields.effect, inherit: bound ? false : fields.inherit }
    return rules.grant(input)
  }
  return (
    <div className="access-stack">
      {bound ? <IrisAlert tone="info">仅展示当前对象的直接 ACL，不包含上级继承或完整有效权限。空列表不代表公开、可读或没有访问限制。</IrisAlert> : null}
      <ACLRuleForm targetKey={objectKey ?? inputKey} kind={kind} bound={bound} fields={fields} busy={rules.busy}
        changeKey={setKey} changeKind={(value) => { setKind(value); if (value === 'bucket') setKey('') }}
        change={setFields} load={rules.load} grant={grant} />
      <p className="muted">多 action 按既有接口逐项写入，失败不保证全部未生效；请重新读取规则确认。删除规则也可能扩大访问权限。</p>
      {rules.error ? <IrisAlert tone="danger">{rules.error}</IrisAlert> : null}
      {rules.entries && rules.error ? <p className="muted">当前列表为上次成功读取的结果，可能已过时。</p> : null}
      {rules.entries ? <ACLTable entries={rules.entries} busy={rules.busy} remove={rules.remove} /> : null}
    </div>
  )
}

function ACLTable({ entries, busy, remove }: { entries: ACLEntry[]; busy: string; remove(id: string): Promise<void> }): React.ReactElement {
  return (
    <IrisCard variant="outline" header="当前资源的直接 ACL">
      <div className="table-scroll"><table className="vault-table">
        <thead><tr><th>主体</th><th>Action</th><th>效果</th><th>继承</th><th>操作</th></tr></thead>
        <tbody>{entries.map((entry) => (
          <tr key={entry.id}><td>{entry.principal_type}{entry.principal_id ? `: ${entry.principal_id}` : ''}</td><td>{entry.action}</td>
            <td><strong className={entry.effect === 'deny' ? 'text-danger' : ''}>{entry.effect}</strong></td><td>{entry.inherit ? '是' : '否'}</td>
            <td><IrisButton size="sm" variant="ghost" disabled={Boolean(busy)} onClick={() => {
              if (window.confirm('删除这条 ACL 规则？删除拒绝规则可能扩大访问权限。')) void remove(entry.id)
            }}>删除</IrisButton></td></tr>
        ))}{entries.length === 0 ? <tr><td className="empty-cell" colSpan={5}>暂无 ACL 规则</td></tr> : null}</tbody>
      </table></div>
    </IrisCard>
  )
}
