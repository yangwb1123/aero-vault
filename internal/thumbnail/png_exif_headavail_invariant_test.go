package thumbnail

import (
	"bytes"
	"errors"
	"testing"
)

// TestPNGChunkCursorHeadAvailInvariant pins the max(0, …) guard's defensive
// branch directly (qa F5): readN/skipN can never advance off past len(head)
// — readN's copy consumes only head[c.off:] — so headAvail() never goes
// negative and c.head[c.off:] can never panic. FuzzPNGOrientation covers this
// indirectly under arbitrary head/tail splits; this is the direct
// belt-and-suspenders pin against a future off-bookkeeping refactor.
func TestPNGChunkCursorHeadAvailInvariant(t *testing.T) {
	// readN past the head end with an exhausted budget: the copy consumes
	// the 4 head bytes, then the pre-read check rejects; off must land
	// exactly on len(head), never past it, and headAvail clamps to 0.
	c := &pngChunkCursor{head: []byte("abcd"), off: 0, remaining: 0}
	if got := c.headAvail(); got != 4 {
		t.Fatalf("headAvail before reads = %d, want 4", got)
	}
	var p [16]byte
	if err := c.readN(p[:]); !errors.Is(err, errMetadataBudgetExceeded) {
		t.Fatalf("readN past head+budget: err=%v want errMetadataBudgetExceeded", err)
	}
	if c.off != len(c.head) {
		t.Fatalf("off after readN = %d, want len(head) %d", c.off, len(c.head))
	}
	if got := c.headAvail(); got != 0 {
		t.Fatalf("headAvail after readN = %d, want 0", got)
	}
	// A second readN past the end must not advance off further (copy of an
	// empty head[c.off:] slice, still 0 bytes).
	if err := c.readN(p[:]); !errors.Is(err, errMetadataBudgetExceeded) {
		t.Fatalf("second readN: err=%v", err)
	}
	if c.off != len(c.head) || c.headAvail() != 0 {
		t.Fatalf("off=%d headAvail=%d after second readN, want off=%d headAvail=0",
			c.off, c.headAvail(), len(c.head))
	}

	// skipN past the head end with a generous r-budget: the head bytes are
	// consumed free, the remainder comes from r, and off stops exactly at
	// len(head) — headAvail 0, never negative.
	c2 := &pngChunkCursor{head: []byte("abcd"), off: 0, remaining: 100, r: bytes.NewReader(make([]byte, 100))}
	if err := c2.skipN(10); err != nil {
		t.Fatalf("skipN past head: %v", err)
	}
	if c2.off != len(c2.head) {
		t.Fatalf("off after skipN = %d, want len(head) %d", c2.off, len(c2.head))
	}
	if got := c2.headAvail(); got != 0 {
		t.Fatalf("headAvail after skipN = %d, want 0", got)
	}
}
