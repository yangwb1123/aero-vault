package thumbnail

import (
	"bytes"
	"context"
	"io"
)

// ctxWriter is the in-encode cancellation boundary for generateLocked: a
// pass-through io.Writer that observes ctx and delegates to buf while the
// context is alive, and aborts on the first write once it is done. It
// implements the full Flush/Write/WriteByte method set, so image/jpeg's
// Encode uses it directly (writer.go:211-215, :581) — no bufio in between —
// making the check granularity per emitted entropy byte, every marker write,
// and the final flush. While ctx.Err() == nil the output is byte-identical
// to writing buf directly (buffering never alters the bit stream).
//
// On a done context every method returns the exact ctx.Err() (never a
// wrapped or reclassified error) and writes nothing. jpeg.Encode stores the
// first error in its sticky e.err field, suppresses all subsequent writes,
// and returns it unwrapped (writer.go:231-249, :639-640), so generateLocked
// surfaces the context error end-to-end (REQ-1/REQ-2).
//
// If a future Go version stops using the writer interface (or the encoder is
// handed a non-implementing writer), ctxWriter is wrapped in a bufio: error
// propagation and byte-identity still hold; only the check granularity widens
// to the bufio's 4 KiB boundary — still correct, strictly coarser.
type ctxWriter struct {
	ctx context.Context
	buf *bytes.Buffer
}

func (w *ctxWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.buf.Write(p)
}

func (w *ctxWriter) WriteByte(c byte) error {
	if err := w.ctx.Err(); err != nil {
		return err
	}
	return w.buf.WriteByte(c)
}

func (w *ctxWriter) Flush() error { return w.ctx.Err() }

// encodeSink is the package seam between generateLocked and the
// context-checking encode writer: generateLocked routes jpeg.Encode's target
// through it (slot_open.go), and tests stub it to deterministically pin the
// wiring — the error path (sink errors surface unwrapped) and the terminal
// post-encode check — without timing. The default sink is the ctxWriter
// above; while ctx is alive it is a pure pass-through (byte-identical
// output). Unexported var: no exported API / config change (I5/I6). No test
// in this package runs in parallel and the stub tests restore the original
// via defer, so the override is race-free.
var encodeSink = func(ctx context.Context, buf *bytes.Buffer) io.Writer {
	return &ctxWriter{ctx: ctx, buf: buf}
}
