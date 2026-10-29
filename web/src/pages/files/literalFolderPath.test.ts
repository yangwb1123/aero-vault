import { describe, expect, it } from 'vitest'
import { literalChildFolderPath, literalFolderCrumbs, isLiteralDirectFile } from './literalFolderPath'

describe('literal server directory paths', () => {
  it('joins nested segments without trimming, decoding, or interpreting URL syntax', () => {
    const parent = literalChildFolderPath('', ' team ')
    expect(parent).toBe(' team /')
    for (const segment of ['  ', '目录', '100%', '%2F', '+', '?', '#', ' docs ']) {
      expect(literalChildFolderPath(parent, segment)).toBe(` team /${segment}/`)
    }
  })

  it('keeps every cumulative breadcrumb byte, including empty segments', () => {
    expect(literalFolderCrumbs(' team /目录/%2F+?# /')).toEqual([
      { label: ' team ', path: ' team /' }, { label: '目录', path: ' team /目录/' },
      { label: '%2F+?# ', path: ' team /目录/%2F+?# /' },
    ])
    expect(literalFolderCrumbs('a//b/')).toEqual([
      { label: 'a', path: 'a/' }, { label: '', path: 'a//' }, { label: 'b', path: 'a//b/' },
    ])
    expect(literalFolderCrumbs('')).toEqual([])
  })

  it('compares direct-file identities using the exact parent and opaque remainder', () => {
    const parent = ' team /%2F+?# /'
    for (const name of ['  ', '资料%25+?#.txt ', '100%.txt']) {
      expect(isLiteralDirectFile(`${parent}${name}`, parent)).toBe(true)
    }
    expect(isLiteralDirectFile('team/%2F+?# /a', parent)).toBe(false)
    expect(isLiteralDirectFile(`${parent}sub/a`, parent)).toBe(false)
    expect(isLiteralDirectFile(parent, parent)).toBe(false)
    expect(isLiteralDirectFile('  ', '')).toBe(true)
  })
})
