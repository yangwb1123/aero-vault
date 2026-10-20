import { IrisButton, IrisCard, IrisCheckbox, IrisInput } from '@iris-ui-kit/react'
import type { AccessEffect, PrincipalType, ResourceKind } from '../../api/access'
import { AccessSelect, type SelectOption } from './AccessSelect'

export interface ACLFields {
  principal: PrincipalType
  principalID: string
  actions: string
  effect: AccessEffect
  inherit: boolean
}
export const initialACLFields = (): ACLFields => ({
  principal: 'user', principalID: '', actions: 'object:read,object:download', effect: 'allow', inherit: false,
})
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

interface ACLFormProps {
  targetKey: string
  kind: ResourceKind
  bound: boolean
  fields: ACLFields
  busy: string
  changeKey(key: string): void
  changeKind(kind: ResourceKind): void
  change(fields: ACLFields): void
  load(): Promise<void>
  grant(): Promise<void> | void
}

export function ACLRuleForm({ targetKey, kind, bound, fields, busy, changeKey, changeKind, change, load, grant }: ACLFormProps): React.ReactElement {
  const anonymous = fields.principal === 'authenticated' || fields.principal === 'everyone'
  return (
    <IrisCard variant="outline" header="资源授权规则">
      <form className="access-form" onSubmit={(event) => { event.preventDefault(); void grant() }}>
        <label className="access-field"><span>资源 Key</span><IrisInput required={kind !== 'bucket'} readOnly={bound} disabled={kind === 'bucket'} value={targetKey} placeholder={kind === 'folder' ? 'team/docs/' : 'team/docs/report.pdf'} onChange={(event) => changeKey(event.target.value)} /></label>
        {bound ? <label className="access-field"><span>资源类型</span><IrisInput readOnly value="对象" /></label>
          : <AccessSelect<ResourceKind> label="资源类型" value={kind} options={kinds} onChange={changeKind} />}
        <AccessSelect<PrincipalType> label="主体类型" value={fields.principal} options={principals} onChange={(principal) => change({ ...fields, principal, principalID: principal === 'authenticated' || principal === 'everyone' ? '' : fields.principalID })} />
        <label className="access-field"><span>主体 ID</span><IrisInput required={!anonymous} disabled={anonymous} value={fields.principalID} placeholder="subject / department / group / role" onChange={(event) => change({ ...fields, principalID: event.target.value })} /></label>
        <label className="access-field"><span>Actions（逗号分隔）</span><IrisInput required value={fields.actions} onChange={(event) => change({ ...fields, actions: event.target.value })} /></label>
        <AccessSelect<AccessEffect> label="授权效果" value={fields.effect} options={effects} onChange={(effect) => change({ ...fields, effect })} />
        {!bound ? <div className="access-options access-wide"><IrisCheckbox checked={fields.inherit} onChange={(inherit) => change({ ...fields, inherit })}>继承到下级资源</IrisCheckbox></div> : null}
        <div className="access-actions access-wide">
          <IrisButton type="button" variant="outline" disabled={Boolean(busy)} loading={busy === 'list'} onClick={() => void load()}>读取规则</IrisButton>
          <IrisButton type="submit" disabled={Boolean(busy)} loading={busy === 'grant'}>添加规则</IrisButton>
        </div>
      </form>
    </IrisCard>
  )
}
