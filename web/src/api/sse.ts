export interface SSEFrame {
  event: string
  data: string
}

export class SSEDecoder {
  private buffer = ''

  push(chunk: string, final = false): SSEFrame[] {
    this.buffer += chunk.replace(/\r\n/g, '\n').replace(/\r/g, '\n')
    const frames: SSEFrame[] = []
    let boundary = this.buffer.indexOf('\n\n')
    while (boundary >= 0) {
      const raw = this.buffer.slice(0, boundary)
      this.buffer = this.buffer.slice(boundary + 2)
      const frame = parseFrame(raw)
      if (frame) frames.push(frame)
      boundary = this.buffer.indexOf('\n\n')
    }
    if (final && this.buffer.trim()) {
      const frame = parseFrame(this.buffer)
      if (frame) frames.push(frame)
      this.buffer = ''
    }
    return frames
  }
}

function parseFrame(raw: string): SSEFrame | undefined {
  let event = 'message'
  const data: string[] = []
  for (const line of raw.split('\n')) {
    if (!line || line.startsWith(':')) continue
    const colon = line.indexOf(':')
    const field = colon >= 0 ? line.slice(0, colon) : line
    let value = colon >= 0 ? line.slice(colon + 1) : ''
    if (value.startsWith(' ')) value = value.slice(1)
    if (field === 'event') event = value
    if (field === 'data') data.push(value)
  }
  return data.length > 0 ? { event, data: data.join('\n') } : undefined
}
