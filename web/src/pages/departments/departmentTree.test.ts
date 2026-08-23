import { describe, expect, it } from 'vitest'
import type { Department } from '../../api/access'
import { buildDepartmentTree } from './departmentTree'

const department = (id: string, name: string, parent_id = ''): Department => ({
  id, name, parent_id, tenant_id: 'acme', created_at: '', updated_at: '',
})

describe('buildDepartmentTree', () => {
  it('builds and sorts a hierarchy independent of API order', () => {
    const nodes = buildDepartmentTree([
      department('child', 'Platform', 'root'),
      department('other', 'Finance'),
      department('root', 'Engineering'),
    ])
    expect(nodes.map((node) => node.id)).toEqual(['root', 'other'])
    expect(nodes[0].children?.map((node) => node.id)).toEqual(['child'])
  })

  it('keeps orphaned and cyclic records visible at the root', () => {
    const nodes = buildDepartmentTree([
      department('orphan', 'Orphan', 'missing'),
      department('a', 'Cycle A', 'b'),
      department('b', 'Cycle B', 'a'),
    ])
    expect(nodes.map((node) => node.id).sort()).toEqual(['a', 'b', 'orphan'])
  })
})
