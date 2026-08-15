package rest

import (
	"fmt"
	"io"
	"net/url"
	"strconv"

	"github.com/aero-vault/aero-vault/internal/service"
)

// thumbFreshnessMaxAge bounds the client-side freshness of the derived
// thumbnail resource (package constant, not config). The thumbnail URL
// carries no version pin, so a PUT that replaces the source object is
// invisible to caches keyed on the URL; bounded freshness (max-age) plus
// must-revalidate (RFC 9111 §5.2.2.2) forces revalidation once the window
// lapses, which engages the If-None-Match/re-Stat machinery in thumbnail.go —
// a replaced object is observed within this window instead of up to the
// historical 24 h.
const thumbFreshnessMaxAge = 300

// sniffHeadLen is the longest magic signature Sniff recognizes: RIFF(4) +
// size(4) + WEBP(4) = 12 bytes. Bucket-3 openers read exactly this many
// bytes from the object stream to classify it.
const sniffHeadLen = 12

// sniffedStream replays the sniffed magic head in front of the live object
// stream (io.MultiReader) and forwards Close to the underlying stream, which
// the API's deferred close runs on this wrapper — io.NopCloser would leak it
// (its Close is a no-op). Mirrors the close-forwarding precedent of
// service.ETagVerifier.
type sniffedStream struct {
	io.Reader
	rc io.Closer
}

func (s *sniffedStream) Close() error { return s.rc.Close() }

// parseThumbDim validates one ?w=/?h= thumbnail dimension parameter. An
// absent parameter yields 0 (default-size semantics per Generate's contract).
// Present values must parse as a non-negative integer; parse errors and
// negatives are client argument errors that the caller maps to 400 before any
// cache validator is emitted or the decode pipeline is entered.
func parseThumbDim(q url.Values, name string) (int, error) {
	if !q.Has(name) {
		return 0, nil
	}
	v := q.Get(name)
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%w: invalid ?%s value %q (must be a non-negative integer)",
			service.ErrInvalidArgs, name, v)
	}
	return n, nil
}
