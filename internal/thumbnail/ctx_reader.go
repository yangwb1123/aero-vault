package thumbnail

import (
	"context"
	"io"
)

// ctxReader is the shared cancellation boundary for every live source-read
// phase in generateLocked: DecodeConfig, the PNG pre-IDAT orientation walk,
// payload Decode, and the source-cap probe (the probe has its own instance
// because it bypasses the LimitReader). While the context is alive this is a
// pure pass-through, preserving the underlying n and error exactly. Once the
// context is done, the next Read returns the exact ctx.Err() without
// consulting the source.
//
// The check is necessarily pre-delegation. A generic io.Reader cannot be
// forcibly interrupted after its Read method has started, so bytes and an
// error from an already-running read are allowed to pass through. The next
// guarded read then stops the codec. This preserves metadata-budget
// precedence: bytes from an in-flight DecodeConfig read can still overflow
// limitedBuffer and produce ErrMetadataTooLarge even if cancellation happened
// while that read was blocked. No buffering, wrapping, source draining, or
// helper goroutine is introduced.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
