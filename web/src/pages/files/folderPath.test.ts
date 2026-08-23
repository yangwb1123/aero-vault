import { describe, expect, it } from 'vitest'
import { childFolderPath, folderCrumbs, isDirectFile, normalizeFolderPath } from './folderPath'

describe('folder path helpers', () => {
  it('normalizes root and nested paths', () => {
    expect(normalizeFolderPath('')).toBe('')
    expect(normalizeFolderPath('/ team// docs /')).toBe('team/docs/')
    expect(childFolderPath('team/', 'reports')).toBe('team/reports/')
  })

  it('builds cumulative breadcrumbs', () => {
    expect(folderCrumbs('team/docs/')).toEqual([
      { label: 'team', path: 'team/' },
      { label: 'docs', path: 'team/docs/' },
    ])
  })

  it('distinguishes direct files from nested objects and directory markers', () => {
    expect(isDirectFile('team/readme.txt', 'team/')).toBe(true)
    expect(isDirectFile('team/docs/report.pdf', 'team/')).toBe(false)
    expect(isDirectFile('team/docs/', 'team/')).toBe(false)
  })
})
