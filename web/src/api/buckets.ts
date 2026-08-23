export type BucketConfig = {
  name: string
  versioning: boolean
  object_lock_seconds: number
  expire_after_days?: number
  expire_action?: 'soft_delete' | 'hard_delete' | string
  bucket_max_bytes?: number
  bucket_max_objects?: number
}

export type BucketStats = {
  bucket: string
  object_count: number
  total_size_bytes: number
}

export interface BucketLifecycleInput {
  days: number
  action: 'soft_delete' | 'hard_delete'
}
