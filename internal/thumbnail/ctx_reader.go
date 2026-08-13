package thumbnail

import (
	"context"
	"io"
)

// ctxReader is the in-decode cancellation boundary for generateLocked: a
// pass-through io.Reader that observes ctx and delegates to r while the
// context is alive, and aborts on the next Read once it is done. It mirrors
// the ctxWriter seam (encode_writer.go) on the read side.
//
// While ctx.Err() == nil it is a pure pass-through: every Read serves exactly
// the bytes r serves (no buffering, no error synthesis), so byte-identical
// decode output on the happy path. Once ctx is done, the next Read returns
// (0, ctx.Err()) — the exact context error instance, never wrapped, never
// copied — and r is not consulted (the source stream is not drained). Both
// stdlib codec read chains surface a (0, non-EOF) reader error unwrapped:
// image/jpeg's fill (jpeg/reader.go:152-169) returns it as-is when n == 0,
// and image/png's chunk reads (png/reader.go:870) return io.ReadFull errors
// unwrapped, so the decode aborts at the next buffer fill — at most one
// codec read (≤ 4 KiB) past the cancel point for JPEG/PNG (image/gif wraps
// its own decompressor over the same bufio, so the bound holds there too but
// is not pinned by the tests). generateLocked's Decode-site block checks
// ctx.Err() before the sentinel fallback (slot_open.go), so the caller
// receives the exact context.Canceled / context.DeadlineExceeded instance,
// never ErrUnsupported.
//
// Scope: this reader wraps ONLY the decode payload path (the trailing element
// of generateLocked's decodeR MultiReaders). The DecodeConfig tee and the PNG
// orientation-walk tee are intentionally unwrapped: a cancel during the
// metadata scan is bounded by MaxMetadataBytes (8 MiB) and the budget abort
// (ErrMetadataTooLarge) has pinned priority over a coincident context error
// (TestGenerateContextMetadataBudgetWinsOverDeadline) — wrapping the tee
// would silently flip that ordering. The deadline leg is exercised in
// production only when the serving layer arms a request timeout
// (REQUEST_TIMEOUT_SECONDS in the REST handler, which maps DeadlineExceeded →
// 504 and Canceled → silent return); a bare context.Background never expires.
//
// Accepted residual (F6-test): the REST E10 tests (internal/api/rest/thumbnail_test.go,
// TestThumbnailMidDecode*) pin error classification (504 / silent return) only;
// the promptness property itself (≤ 4 KiB over-read, stream not drained, slot
// released) is pinned at the package level in decode_cancel_test.go. This
// division keeps REST assertions free of wall-clock timing; a regression that
// reintroduces unbounded decode reads would fail the package-level pins.
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
