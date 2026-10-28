import type { NavNode } from '@iris-ui-kit/react'
import type { VaultSession } from './api/vault'

export type ManagementAccess = Record<'buckets' | 'departments' | 'operations', boolean>

const primary: NavNode[] = [
  { key: 'overview', title: '空间概览', icon: 'home' },
  { key: 'files', title: '文件', icon: 'folder', badge: '回收站' },
  { key: 'access', title: '访问与发布', icon: 'lock', badge: '分享' },
  { key: 'activity', title: '对象动态', icon: 'clock' },
]
const optionalAI: NavNode[] = [
  { key: 'search', title: '对象检索', icon: 'search' },
  { key: 'chat', title: '对象问答', icon: 'link' },
  { key: 'agent', title: '对象 Agent', icon: 'grid' },
  { key: 'lineage', title: 'AI 使用记录', icon: 'clock' },
]
const management: Array<NavNode & { key: keyof ManagementAccess }> = [
  { key: 'buckets', title: '存储桶', icon: 'archive' },
  { key: 'departments', title: '部门与成员', icon: 'users' },
  { key: 'operations', title: '运行运维', icon: 'settings' },
]
const services: NavNode = { key: 'services', title: '平台服务', icon: 'grid' }
const sessionKinds = new Set(['anonymous', 'user', 'service', 'system', 'public', 'share'])
const managementKinds = new Set(['user', 'service'])
const managementRoles = new Set(['vault.tenant_admin', 'vault.file_admin'])

export const routeKeys = new Set([...primary, ...optionalAI, ...management, services].map((item) => item.key))

function stringList(value: unknown): value is string[] {
  return Array.isArray(value) && value.every((item) => typeof item === 'string')
}

export function isSessionObservation(value: unknown): value is VaultSession {
  if (!value || typeof value !== 'object') return false
  const session = value as Partial<VaultSession>
  return typeof session.authenticated === 'boolean' &&
    typeof session.tenant_id === 'string' && session.tenant_id.length > 0 &&
    typeof session.principal_kind === 'string' && sessionKinds.has(session.principal_kind) &&
    [session.roles, session.groups, session.scopes].every(stringList)
}

// Coarse hints only: requireRESTScope still gates reads; requireAccessAdmin and
// bucket settings use access.IsAdministrator. Explicit ACL/Deny, tenant state,
// Space boundaries and optional service availability remain server decisions.
export function managementForSession(session?: VaultSession, operatorObserved = false): ManagementAccess {
  if (!isSessionObservation(session) || !session.authenticated || !managementKinds.has(session.principal_kind)) {
    return { buckets: false, departments: false, operations: false }
  }
  const admin = session.scopes.includes('admin')
  const tenantManagement = (admin || session.scopes.includes('read')) &&
    (admin || session.roles.some((role) => managementRoles.has(role)))
  return { buckets: tenantManagement, departments: tenantManagement, operations: admin && operatorObserved }
}

export function isManagementRoute(route: string): route is keyof ManagementAccess {
  return management.some((item) => item.key === route)
}

export function navigationMenus(access: ManagementAccess): NavNode[] {
  const available = management.filter((item) => access[item.key])
  return [
    ...primary,
    { key: 'optional-ai', title: '可选 AI', icon: 'search', children: optionalAI },
    ...(available.length ? [{ key: 'management', title: '管理', icon: 'settings', children: available }] : []),
    services,
  ]
}
