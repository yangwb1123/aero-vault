package thumbnail

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

// boundedSource serves exactly total bytes (prefix then zeros) then EOF — the
// exact-cap boundary fixture for the source-cap discriminator.
type boundedSource struct {
	prefix []byte
	off    int
	total  int
}

func (s *boundedSource) Read(p []byte) (int, error) {
	if s.off >= s.total {
		return 0, io.EOF
	}
	remaining := s.total - s.off
	if len(p) > remaining {
		p = p[:remaining]
	}
	start := s.off
	if start > len(s.prefix) {
		start = len(s.prefix)
	}
	n := copy(p, s.prefix[start:])
	for i := n; i < len(p); i++ {
		p[i] = 0
	}
	s.off += len(p) // zeros count toward the total
	return len(p), nil
}

// TestGenerateSourceCapBoundaryPins C1: a source that EOFs at EXACTLY
// MaxSourceBytes is a complete (if undecodable) input — the probe finds
// nothing more, capped=false, and the failure is the client-argument class
// (ErrUnsupported/400). One byte beyond the cap trips the probe → the
// server-budget class (ErrSourceTooLarge/413). This is the discriminator's
// raison d'être: a counter-only regression flips the boundary wire class.
func TestGenerateSourceCapBoundaryPins(t *testing.T) {
	base := appnPaddedJPEG(t, 0)
	i := bytes.Index(base, []byte{0xFF, 0xDA})
	if i < 0 {
		t.Fatal("no SOS marker in fixture")
	}
	n := int(binary.BigEndian.Uint16(base[i+2 : i+4]))
	prefix := base[:i+2+n] // entropy data cut; replaced by zeros to the boundary

	// Exactly MaxSourceBytes then EOF: capped=false → ErrUnsupported.
	exact := &boundedSource{prefix: prefix, total: MaxSourceBytes}
	if _, err := Generate(exact, 100, 100); err != ErrUnsupported {
		t.Fatalf("exact-cap EOF: expected ErrUnsupported, got %v", err)
	}
	if exact.off != MaxSourceBytes {
		t.Fatalf("exact-cap: consumed %d bytes, want %d", exact.off, MaxSourceBytes)
	}

	// One byte beyond: capped=true → ErrSourceTooLarge.
	over := &boundedSource{prefix: prefix, total: MaxSourceBytes + 1}
	if _, err := Generate(over, 100, 100); err != ErrSourceTooLarge {
		t.Fatalf("cap+1: expected ErrSourceTooLarge, got %v", err)
	}
}

// TestSourceCapRecorderUnit pins C2: the probe contract —
//   - capped=true when the source still has bytes after the cap EOF;
//   - capped=false when the source also EOFs (the input ended exactly at
//     the cap);
//   - probed flips exactly once (a second EOF-triggered Read does not
//     re-probe);
//   - a non-EOF probe error counts as capped (budget exhaustion already
//     proven — the probe could not rule out more data).
func TestSourceCapRecorderUnit(t *testing.T) {
	jpeg := appnPaddedJPEG(t, 0)
	t.Run("capped true", func(t *testing.T) {
		src := &boundedSource{prefix: jpeg, total: MaxSourceBytes + 1}
		rec := &sourceCapRecorder{r: &limitedReader{src: src, limit: MaxSourceBytes}, src: src}
		var buf [32 << 10]byte
		var total int
		for {
			n, err := rec.Read(buf[:])
			total += n
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("read: %v", err)
			}
		}
		if total != MaxSourceBytes {
			t.Fatalf("consumed %d, want %d", total, MaxSourceBytes)
		}
		if !rec.capped {
			t.Fatal("capped = false, want true (source had bytes beyond the cap)")
		}
		if !rec.probed {
			t.Fatal("probed = false, want true (probe ran on the cap EOF)")
		}
	})
	t.Run("capped false", func(t *testing.T) {
		src := &boundedSource{prefix: jpeg, total: MaxSourceBytes}
		rec := &sourceCapRecorder{r: &limitedReader{src: src, limit: MaxSourceBytes}, src: src}
		var buf [32 << 10]byte
		for {
			if _, err := rec.Read(buf[:]); err == io.EOF {
				break
			}
		}
		if rec.capped {
			t.Fatal("capped = true, want false (source EOFed exactly at the cap)")
		}
		if !rec.probed {
			t.Fatal("probed = false, want true")
		}
	})
	t.Run("probe single-shot", func(t *testing.T) {
		src := &boundedSource{prefix: jpeg, total: MaxSourceBytes + 1}
		rec := &sourceCapRecorder{r: &limitedReader{src: src, limit: MaxSourceBytes}, src: src}
		var buf [32 << 10]byte
		for {
			if _, err := rec.Read(buf[:]); err == io.EOF {
				break
			}
		}
		if !rec.probed {
			t.Fatal("probed = false after the cap EOF")
		}
		// A subsequent read after EOF must not re-probe (the flag stays set
		// and the source is not touched again).
		n, err := rec.Read(buf[:])
		if n != 0 || err != io.EOF {
			t.Fatalf("post-EOF read = (%d, %v), want (0, io.EOF)", n, err)
		}
	})
	t.Run("probe error counts capped", func(t *testing.T) {
		probeErr := errors.New("probe failed")
		src := &errAfterSource{prefix: jpeg, errAt: MaxSourceBytes, probeErr: probeErr}
		rec := &sourceCapRecorder{r: &limitedReader{src: src, limit: MaxSourceBytes}, src: src}
		var buf [32 << 10]byte
		for {
			if _, err := rec.Read(buf[:]); err == io.EOF {
				break
			}
		}
		if !rec.capped {
			t.Fatal("capped = false, want true (a non-EOF probe error proves the budget was hit)")
		}
		if !errors.Is(rec.probeErr, probeErr) {
			t.Fatalf("probeErr = %v, want the injected %v (recorded, not folded into capped)", rec.probeErr, probeErr)
		}
		if src.off != MaxSourceBytes {
			t.Fatalf("probe consumed %d bytes, want 0 past the cap (off == errAt == MaxSourceBytes)", src.off-MaxSourceBytes)
		}
	})
	t.Run("probe ctx aborts: no source read", func(t *testing.T) {
		src := &maxSourceBytesJPEGPayload{prefix: appnPaddedJPEG(t, 0)}
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // pre-canceled: the probe must not consult the source
		rec := &sourceCapRecorder{
			r:   &limitedReader{src: src, limit: MaxSourceBytes},
			src: &ctxReader{ctx: ctx, r: src}, // the exact R1/D1 wire shape: ctxReader outermost, marker innermost
		}
		var buf [32 << 10]byte
		for {
			if _, err := rec.Read(buf[:]); err == io.EOF {
				break
			}
		}
		if src.off != MaxSourceBytes {
			t.Fatalf("probe consumed %d bytes past the cap, want 0 (src.off == MaxSourceBytes)", src.off-MaxSourceBytes)
		}
		if !errors.Is(rec.probeErr, context.Canceled) {
			t.Fatalf("probeErr = %v, want context.Canceled (recorded, never wrapped)", rec.probeErr)
		}
		if !rec.capped {
			t.Fatal("capped = false, want true (a non-EOF probe error proves the budget was hit)")
		}
	})
}

// limitedReader serves at most limit bytes from src then EOF (the
// io.LimitReader contract the recorder wraps).
type limitedReader struct {
	src   io.Reader
	limit int
	off   int
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.off >= l.limit {
		return 0, io.EOF
	}
	if len(p) > l.limit-l.off {
		p = p[:l.limit-l.off]
	}
	n, err := l.src.Read(p)
	l.off += n
	return n, err
}

// errAfterSource serves prefix+zeros until errAt bytes have been served, then
// returns probeErr sticky on every subsequent read (the probe-error fixture).
// errAt is the error position (was: total — the dormant duplicate; with the
// error at exactly errAt, the probe's first read at off == errAt returns
// (0, probeErr) consuming 0 bytes).
type errAfterSource struct {
	prefix   []byte
	off      int
	errAt    int
	probeErr error
}

func (s *errAfterSource) Read(p []byte) (int, error) {
	if s.off >= s.errAt {
		return 0, s.probeErr
	}
	remaining := s.errAt - s.off
	if len(p) > remaining {
		p = p[:remaining]
	}
	start := s.off
	if start > len(s.prefix) {
		start = len(s.prefix)
	}
	n := copy(p, s.prefix[start:])
	for i := n; i < len(p); i++ {
		p[i] = 0
	}
	s.off += len(p) // zeros count toward the total
	return len(p), nil
}
