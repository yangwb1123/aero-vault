import * as React from 'react'
import { IrisAlert, IrisBadge, IrisButton, IrisCard, IrisInput } from '@iris-ui-kit/react'
import type { DepartmentRole } from '../../api/access'
import type { VaultClient } from '../../api/vault'
import { PageError, PageLoading } from '../../components/Page'
import { useResource } from '../../hooks/useResource'

export function DepartmentDetailPanel({
  client,
  departmentID,
  onDeleted,
}: {
  client: VaultClient
  departmentID: string
  onDeleted(): void
}): React.ReactElement {
  const [subject, setSubject] = React.useState('')
  const [role, setRole] = React.useState<DepartmentRole>('member')
  const [busy, setBusy] = React.useState('')
  const [error, setError] = React.useState<string>()
  const load = React.useCallback(() => client.getDepartment(departmentID), [client, departmentID])
  const resource = useResource(load)

  const addMember = async () => run('member', async () => {
    await client.putDepartmentMember(departmentID, subject.trim(), role)
    setSubject('')
    resource.reload()
  })
  const removeMember = async (memberSubject: string) => run(`remove:${memberSubject}`, async () => {
    await client.deleteDepartmentMember(departmentID, memberSubject)
    resource.reload()
  })
  const deleteDepartment = async () => run('delete', async () => {
    await client.deleteDepartment(departmentID)
    onDeleted()
  })
  const run = async (label: string, action: () => Promise<void>) => {
    setBusy(label)
    setError(undefined)
    try { await action() } catch (reason) {
      setError(reason instanceof Error ? reason.message : '部门操作失败')
    } finally { setBusy('') }
  }

  if (resource.loading) return <PageLoading />
  if (resource.error) return <PageError error={resource.error} retry={resource.reload} />
  if (!resource.data) return <></>
  const { department, members } = resource.data
  return (
    <div className="access-stack">
      <IrisCard
        variant="outline"
        header={<div className="department-heading"><div><strong>{department.name}</strong><div className="muted">{department.id}</div></div>
          <IrisButton size="sm" variant="ghost" loading={busy === 'delete'} onClick={() => {
            if (window.confirm(`删除 ${department.name} 及其所有下级部门和关联 ACL？`)) void deleteDepartment()
          }}>删除部门</IrisButton></div>}
      >
        <form className="member-form" onSubmit={(event) => { event.preventDefault(); void addMember() }}>
          <label className="access-field"><span>aero-id Subject ID</span><IrisInput required value={subject} placeholder="用户的 canonical subject" onChange={(event) => setSubject(event.target.value)} /></label>
          <label className="access-field"><span>部门角色</span><select value={role} onChange={(event) => setRole(event.target.value as DepartmentRole)}><option value="member">Member</option><option value="manager">Manager</option></select></label>
          <IrisButton type="submit" loading={busy === 'member'}>添加成员</IrisButton>
        </form>
      </IrisCard>
      {error ? <IrisAlert tone="danger">{error}</IrisAlert> : null}
      <IrisCard variant="outline" header={`成员（${members.length}）`}>
        <div className="table-scroll"><table className="vault-table">
          <thead><tr><th>aero-id Subject ID</th><th>角色</th><th>加入时间</th><th>操作</th></tr></thead>
          <tbody>{members.map((member) => (
            <tr key={member.subject_id}><td><strong>{member.subject_id}</strong></td><td><IrisBadge>{member.role}</IrisBadge></td>
              <td>{new Date(member.created_at).toLocaleString('zh-CN')}</td><td><IrisButton size="sm" variant="ghost" disabled={Boolean(busy)} onClick={() => {
                if (window.confirm(`移除成员 ${member.subject_id}？`)) void removeMember(member.subject_id)
              }}>移除</IrisButton></td></tr>
          ))}{members.length === 0 ? <tr><td className="empty-cell" colSpan={4}>暂无成员</td></tr> : null}</tbody>
        </table></div>
      </IrisCard>
    </div>
  )
}
