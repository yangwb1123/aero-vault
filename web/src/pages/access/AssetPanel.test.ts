import { describe, expect, it } from 'vitest'
import type { PublicAsset } from '../../api/access'
import { filterAssetsByBucket } from './AssetPanel'

const asset = (id: string, bucket: string): PublicAsset => ({
  id,
  bucket,
  key: `${id}.jpg`,
  slug: id,
  cache_control: 'public, max-age=86400',
  published_by: 'alice',
  published_at: '2026-08-23T10:00:00Z',
})

describe('filterAssetsByBucket', () => {
  it('keeps public assets scoped to the selected non-default bucket', () => {
    expect(filterAssetsByBucket([
      asset('default-image', 'default'),
      asset('project-image', 'project'),
    ], 'project')).toEqual([asset('project-image', 'project')])
  })
})
