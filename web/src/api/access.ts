export type ResourceKind = 'object' | 'folder' | 'bucket'
export type PrincipalType = 'user' | 'department' | 'group' | 'role' | 'authenticated' | 'everyone'
export type AccessEffect = 'allow' | 'deny'

export interface Share {
  id: string
  bucket: string
  key: string
  name?: string
  allow_preview: boolean
  allow_download: boolean
  max_uses?: number
  use_count: number
  expires_at?: string
  revoked_at?: string
  created_by: string
  created_at: string
}

export interface CreateShareInput {
  key: string
  name?: string
  password?: string
  allow_preview: boolean
  allow_download: boolean
  max_uses?: number
  ttl_seconds?: number
}

export interface CreatedShare {
  share: Share
  token: string
  url: string
}

export interface PublicAsset {
  id: string
  bucket: string
  key: string
  slug: string
  cache_control: string
  published_by: string
  published_at: string
}

export interface PublishedAsset {
  asset: PublicAsset
  url: string
}

export interface ACLEntry {
  id: string
  bucket: string
  key?: string
  resource_kind: ResourceKind
  principal_type: PrincipalType
  principal_id?: string
  action: string
  effect: AccessEffect
  inherit: boolean
  created_by: string
  created_at: string
}

export interface PutACLInput {
  key: string
  resource_kind: ResourceKind
  principal_type: PrincipalType
  principal_id: string
  actions: string[]
  effect: AccessEffect
  inherit: boolean
}

export interface Department {
  id: string
  tenant_id: string
  parent_id?: string
  name: string
  created_at: string
  updated_at: string
}

export type DepartmentRole = 'member' | 'manager'

export interface DepartmentMember {
  tenant_id: string
  department_id: string
  subject_id: string
  role: DepartmentRole
  created_at: string
}

export interface DepartmentDetail {
  department: Department
  members: DepartmentMember[]
}
