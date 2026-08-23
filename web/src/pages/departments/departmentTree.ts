import type { IrisTreeNode } from '@iris-ui-kit/react'
import type { Department } from '../../api/access'

export function buildDepartmentTree(departments: Department[]): IrisTreeNode[] {
  const departmentsByID = new Map(departments.map((department) => [department.id, department]))
  const nodesByID = new Map<string, IrisTreeNode>()
  for (const department of departments) {
    nodesByID.set(department.id, { id: department.id, label: department.name, children: [] })
  }
  const roots: IrisTreeNode[] = []
  for (const department of departments) {
    const node = nodesByID.get(department.id)!
    const parent = nodesByID.get(department.parent_id ?? '')
    if (!parent || hasParentCycle(department, departmentsByID)) roots.push(node)
    else parent.children!.push(node)
  }
  sortNodes(roots)
  return roots
}

function hasParentCycle(department: Department, departments: Map<string, Department>): boolean {
  const visited = new Set([department.id])
  let parentID = department.parent_id
  while (parentID) {
    if (visited.has(parentID)) return true
    visited.add(parentID)
    parentID = departments.get(parentID)?.parent_id
  }
  return false
}

function sortNodes(nodes: IrisTreeNode[]): void {
  nodes.sort((left, right) => left.label.localeCompare(right.label, 'zh-CN'))
  for (const node of nodes) {
    if (node.children?.length) sortNodes(node.children)
    else delete node.children
  }
}
