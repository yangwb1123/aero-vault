import { describe, expect, it } from 'vitest'
import { parseStringRecord } from './recordJSON'

describe('parseStringRecord', () => {
  it('accepts a JSON string map', () => {
    expect(parseStringRecord('{"owner":"alice"}', '元数据')).toEqual({ owner: 'alice' })
  })

  it('rejects arrays and non-string values', () => {
    expect(() => parseStringRecord('[]', '标签')).toThrow('JSON 对象')
    expect(() => parseStringRecord('{"count":2}', '标签')).toThrow('字符串')
  })
})
