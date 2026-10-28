import { describe, expect, it } from 'vitest'
import type { VaultSession } from './api/vault'
import { isManagementRoute, isSessionObservation, managementForSession, navigationMenus, routeKeys } from './navigation'

const reader: VaultSession = {
  authenticated: true, tenant_id: 'acme', principal_kind: 'user',
  roles: [], groups: [], scopes: ['read'],
}
const denied = { buckets: false, departments: false, operations: false }

describe('coarse management route eligibility', () => {
  it.each([
    undefined,
    { ...reader, scopes: ['read', 'write'] },
    { ...reader, subject_id: 'owner', roles: ['owner', 'admin', 'vault.publisher_admin'] },
    { ...reader, roles: ['vault.file_admin.extra', 'vault.tenant_admin:acme'] },
    { ...reader, tenant_id: '*' },
    { ...reader, authenticated: false, principal_kind: 'anonymous', scopes: ['admin'] },
    { ...reader, principal_kind: 'share', scopes: ['admin'] },
    { ...reader, principal_kind: 'public', scopes: ['admin'] },
    { ...reader, principal_kind: 'system', scopes: ['admin'] },
    { ...reader, principal_kind: 'unrecognized', scopes: ['admin'] },
  ])('does not mistake identity or file access for management %#', (session) => {
    expect(managementForSession(session, true)).toEqual(denied)
  })

  it.each(['vault.tenant_admin', 'vault.file_admin'])('matches the exact backend administrator role %s', (role) => {
    expect(managementForSession({ ...reader, roles: [role] })).toEqual({
      buckets: true, departments: true, operations: false,
    })
    expect(managementForSession({ ...reader, roles: [role], scopes: ['write'] })).toEqual(denied)
  })

  it.each(['user', 'service'])('recognizes an admin-scoped %s without inventing operator eligibility', (kind) => {
    const session = { ...reader, principal_kind: kind, scopes: ['admin'] }
    expect(managementForSession(session)).toEqual({ buckets: true, departments: true, operations: false })
    expect(managementForSession(session, true)).toEqual({ buckets: true, departments: true, operations: true })
  })

  it('does not let a successful operator observation upgrade a reader or role-only administrator', () => {
    expect(managementForSession(reader, true)).toEqual(denied)
    expect(managementForSession({ ...reader, roles: ['vault.tenant_admin'] }, true)).toEqual({
      buckets: true, departments: true, operations: false,
    })
  })
})

describe('session observations', () => {
  it.each([
    null, {}, { ...reader, authenticated: 'true' }, { ...reader, tenant_id: '' },
    { ...reader, scopes: undefined }, { ...reader, roles: 'vault.tenant_admin' },
    { ...reader, scopes: ['read', 7] }, { ...reader, groups: null },
    { ...reader, principal_kind: 'unknown' },
  ])('rejects incomplete or unknown observations %#', (value) => {
    expect(isSessionObservation(value)).toBe(false)
    expect(managementForSession(value as VaultSession, true)).toEqual(denied)
  })

  it('accepts the normalized anonymous response but does not infer disabled authentication', () => {
    const anonymous = { ...reader, authenticated: false, principal_kind: 'anonymous', scopes: [] }
    expect(isSessionObservation(anonymous)).toBe(true)
    expect(managementForSession(anonymous, true)).toEqual(denied)
  })
})

describe('navigation separation', () => {
  it('keeps files, recovery and sharing ahead of optional AI and omits unavailable management', () => {
    const menus = navigationMenus(denied)
    expect(menus.slice(0, 3).map((item) => item.key)).toEqual(['overview', 'files', 'access'])
    expect(menus.find((item) => item.key === 'files')?.badge).toBe('回收站')
    expect(menus.find((item) => item.key === 'access')?.badge).toBe('分享')
    expect(menus.find((item) => item.key === 'optional-ai')?.children?.map((item) => item.key))
      .toEqual(['search', 'chat', 'agent', 'lineage'])
    expect(menus.find((item) => item.key === 'management')).toBeUndefined()
  })

  it('groups only the management routes currently observed as eligible', () => {
    const menus = navigationMenus({ buckets: true, departments: true, operations: false })
    expect(menus.find((item) => item.key === 'management')?.children?.map((item) => item.key))
      .toEqual(['buckets', 'departments'])
    expect(isManagementRoute('operations')).toBe(true)
    expect(isManagementRoute('access')).toBe(false)
    expect(routeKeys.has('operations')).toBe(true)
    expect(routeKeys.has('services')).toBe(true)
    expect(routeKeys.has('management')).toBe(false)
    expect(routeKeys.has('optional-ai')).toBe(false)
  })
})
