import * as React from 'react'
import {
  IrisAlert,
  IrisButton,
  IrisCard,
  IrisEmptyState,
  IrisInput,
  IrisTree,
} from '@iris-ui-kit/react'
import type { Department } from '../../api/access'
import type { VaultClient } from '../../api/vault'
import { PageError, PageHeader, PageLoading } from '../../components/Page'
import { useResource } from '../../hooks/useResource'
import { DepartmentDetailPanel } from './DepartmentDetailPanel'
import { buildDepartmentTree } from './departmentTree'

export function DepartmentsPage({ client }: { client: VaultClient }): React.ReactElement {
  const [name, setName] = React.useState('')
  const [parentID, setParentID] = React.useState('')
  const [selectedID, setSelectedID] = React.useState('')
  const [busy, setBusy] = React.useState(false)
  const [error, setError] = React.useState<string>()
  const load = React.useCallback(() => client.listDepartments(), [client])
  const resource = useResource(load)

  const create = async () => {
    setBusy(true)
    setError(undefined)
    try {
      const created = await client.createDepartment(name.trim(), parentID)
      setName('')
      setParentID('')
      setSelectedID(created.id)
      resource.reload()
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '创建部门失败')
    } finally {
      setBusy(false)
    }
  }

  return (
    <section>
      <PageHeader title="部门与成员" description="维护 Aero Vault 文件授权使用的部门层级，并关联 aero-id Subject ID。需要租户管理员权限。" />
      <div className="access-stack">
        <IrisCard variant="outline" header="创建部门">
          <form className="department-create" onSubmit={(event) => { event.preventDefault(); void create() }}>
            <label className="access-field"><span>部门名称</span><IrisInput required value={name} placeholder="Platform Engineering" onChange={(event) => setName(event.target.value)} /></label>
            <ParentField departments={resource.data ?? []} value={parentID} onChange={setParentID} />
            <IrisButton type="submit" loading={busy}>创建</IrisButton>
          </form>
        </IrisCard>
        {error ? <IrisAlert tone="danger">{error}</IrisAlert> : null}
        {resource.loading ? <PageLoading /> : null}
        {resource.error ? <PageError error={resource.error} retry={resource.reload} /> : null}
        {resource.data ? (
          <div className="department-layout">
            <IrisCard variant="outline" header="部门层级">
              <IrisTree
                key={resource.data.map((department) => department.id).join(':')}
                ariaLabel="部门层级"
                nodes={buildDepartmentTree(resource.data)}
                defaultExpanded={resource.data.map((department) => department.id)}
                selected={selectedID ? [selectedID] : []}
                selectionMode="single"
                onSelectedChange={(selected) => setSelectedID(selected[0] ?? '')}
                emptyState="尚未创建部门"
              />
            </IrisCard>
            {selectedID ? (
              <DepartmentDetailPanel
                key={selectedID}
                client={client}
                departmentID={selectedID}
                onDeleted={() => { setSelectedID(''); resource.reload() }}
              />
            ) : <IrisEmptyState title="选择部门" description="从部门树选择一项以管理 aero-id 成员映射。" />}
          </div>
        ) : null}
      </div>
    </section>
  )
}

function ParentField({ departments, value, onChange }: { departments: Department[]; value: string; onChange(value: string): void }): React.ReactElement {
  return (
    <label className="access-field"><span>上级部门（可选）</span>
      <select value={value} onChange={(event) => onChange(event.target.value)}>
        <option value="">无（顶级部门）</option>
        {departments.map((department) => <option key={department.id} value={department.id}>{department.name}</option>)}
      </select>
    </label>
  )
}
