import { describe, expect, it } from 'vitest'
import { SSEDecoder } from './sse'

describe('SSEDecoder', () => {
  it('keeps fragmented frames and the server event type', () => {
    const decoder = new SSEDecoder()
    expect(decoder.push('event: token\ndata: "hel')).toEqual([])
    expect(decoder.push('lo"\n\nevent: done\ndata: {"answer":"hello"}\n\n')).toEqual([
      { event: 'token', data: '"hello"' },
      { event: 'done', data: '{"answer":"hello"}' },
    ])
  })

  it('joins multiline data and ignores comments', () => {
    const decoder = new SSEDecoder()
    expect(decoder.push(': keepalive\nevent: error\ndata: first\ndata: second', true)).toEqual([
      { event: 'error', data: 'first\nsecond' },
    ])
  })
})
