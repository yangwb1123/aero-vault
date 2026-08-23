export interface ObjectVersion {
  version_id: string
  size: number
  etag: string
  content_type?: string
  updated_at: string
  deleted_at?: string
  locked_until?: string
}

export interface PresignedLink {
  url: string
  expires: string
}

export interface LegalHold {
  key: string
  version_id?: string
  hold_reason: string
  created_by: string
  created_at: string
}

export interface ObjectPreview {
  body: Blob
  contentType: string
  partial: boolean
}
