package thumbnail

// Mid-decode source-stream error classification tests (direction:
// "Stop flattening mid-decode storage/verification I/O errors into
// ErrUnsupported (client 400)"). The marker (sourceReadMarker) must turn
// non-EOF, non-context source-read failures into *SourceReadError at both
// decode sites — a server-side error class (REST maps it to 5xx/410, never
// 400 InvalidArgument) — while EOF/truncation, codec-synthesized errors and
// wrapped context sentinels keep their pinned behavior byte-for-byte.
//
// Determinism discipline: every failure is injected through deterministic
// reader doubles (sticky error-after-prefix or one-shot partial-error
// shapes), so no wall-clock timing enters any assertion.

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"testing"

	"github.com/aero-vault/aero-vault/internal/service"
)

// stubReadResult is one canned Read result for stubReader.
type stubReadResult struct {
	n   int
	err error
}

// stubReader serves a fixed sequence of Read results, then (0, io.EOF).
type stubReader struct {
	results []stubReadResult
}

func (r *stubReader) Read(p []byte) (int, error) {
	if len(r.results) == 0 {
		return 0, io.EOF
	}
	res := r.results[0]
	r.results = r.results[1:]
	return res.n, res.err
}

// TestSourceReadMarkerPassThrough pins the marker's own contract (the two
// load-bearing behaviors the codec legs only exercise transitively): a
// (0,nil) read passes through untouched (no busy-loop synthesis), a partial
// read carrying an error keeps its n and wraps the error in the injected
// instance, io.EOF passes through unmarked (truncation keeps classifying as
// ErrUnsupported), and errors matching a context sentinel via errors.Is
// (wrapped-SDK shape) pass through unmarked with the same instance.
func TestSourceReadMarkerPassThrough(t *testing.T) {
	ioErr := fmt.Errorf("s3 read failed: %w", errors.New("i/o error"))
	ctxErr := fmt.Errorf("wrapped: %w", context.DeadlineExceeded)
	for _, tc := range []struct {
		name   string
		in     stubReadResult
		wantN  int
		wantOK bool // wantOK=true: result must be *SourceReadError wrapping tc.in.err
	}{
		{"(0,nil) passes through untouched", stubReadResult{0, nil}, 0, false},
		{"(n>0,err) keeps n and wraps", stubReadResult{3, ioErr}, 3, true},
		{"(0,io.EOF) unmarked", stubReadResult{0, io.EOF}, 0, false},
		{"context sentinel unmarked (same instance)", stubReadResult{0, ctxErr}, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &sourceReadMarker{r: &stubReader{results: []stubReadResult{tc.in}}}
			p := make([]byte, 8)
			n, err := m.Read(p)
			if n != tc.wantN {
				t.Fatalf("n = %d, want %d", n, tc.wantN)
			}
			var sre *SourceReadError
			if tc.wantOK {
				if !errors.As(err, &sre) {
					t.Fatalf("err = %v (%T), want *SourceReadError", err, err)
				}
				if sre.Err != tc.in.err {
					t.Fatalf("wrapped instance = %v, want the injected instance %v (never re-wrapped)", sre.Err, tc.in.err)
				}
				if !errors.Is(err, tc.in.err) {
					t.Fatalf("errors.Is(err, injected) = false, want true (identity must traverse Unwrap)")
				}
				return
			}
			if err != tc.in.err {
				t.Fatalf("err = %v, want the same unmarked instance %v", err, tc.in.err)
			}
			if errors.As(err, &sre) {
				t.Fatalf("err = %v, must not be marked as *SourceReadError", err)
			}
		})
	}
}

// recordingReadCloser serves reads through an io.Reader and counts Close
// calls — pins the stream-lifecycle contract (close exactly once) on the
// new error path through the opener funnel.
type recordingReadCloser struct {
	io.Reader
	closed *atomic.Int32
}

func (r *recordingReadCloser) Close() error {
	r.closed.Add(1)
	return nil
}

// pngPostIHDRPartialErrorReader serves a valid PNG through the 33-byte
// signature+IHDR prefix, then returns 3 bytes plus err in the same Read once,
// then serves the remainder normally. It pins the io.ReadFull partial-read
// shape the PNG orientation walk must surface as *SourceReadError.
type pngPostIHDRPartialErrorReader struct {
	data  []byte
	err   error
	off   int
	fired bool
}

func (r *pngPostIHDRPartialErrorReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.off < 33 {
		n := 33 - r.off
		if n > len(p) {
			n = len(p)
		}
		if n > len(r.data)-r.off {
			n = len(r.data) - r.off
		}
		if n == 0 {
			return 0, io.EOF
		}
		copied := copy(p[:n], r.data[r.off:r.off+n])
		r.off += copied
		return copied, nil
	}
	if !r.fired {
		r.fired = true
		n := 3
		if n > len(p) {
			n = len(p)
		}
		if n > len(r.data)-r.off {
			n = len(r.data) - r.off
		}
		copied := copy(p[:n], r.data[r.off:r.off+n])
		r.off += copied
		return copied, r.err
	}
	if r.off >= len(r.data) {
		return 0, io.EOF
	}
	copied := copy(p, r.data[r.off:])
	r.off += copied
	return copied, nil
}

// wantKind discriminates the three expected outcomes of the AC-1 table.
type wantKind int

const (
	wantMarked       wantKind = iota // *SourceReadError wrapping the injected instance
	wantSameInstance                 // the exact injected instance, unmarked
	wantUnsupported                  // ErrUnsupported (truncation/codec error)
)

// TestGenerateContextMidStreamIOError pins the discrimination contract at
// the module boundary (AC-1): a non-context source-stream failure at either
// decode site surfaces as *SourceReadError — the marked instance itself,
// errors.Is-reachable to the injected error, never ErrUnsupported — while
// EOF truncation stays ErrUnsupported (corrupt input → 400) and wrapped
// context sentinels keep their exact-instance contract. Rows cover the
// DecodeConfig site (8-byte PNG signature: the config scan's next ReadFull
// fails), the walk/Decode site (33-byte prefix: DecodeConfig succeeds, the
// orientation walk defers, Decode re-encounters the failure), the JPEG
// codec leg (error mid entropy-coded scan, through jpeg fill's n==0
// identity path), the on-read verification shape (error wrapping
// service.ErrObjectCorrupt — the ETagVerifier mismatch contract), and the
// two controls.
func TestGenerateContextMidStreamIOError(t *testing.T) {
	injected := fmt.Errorf("s3 read failed: %w", errors.New("i/o error"))
	png := makePNG(t, 64, 64)

	// JPEG fixture: serve through the SOS segment plus a few entropy-coded
	// scan bytes, so DecodeConfig succeeds (its scan consumes the pre-SOS
	// segments and the SOS marker+length) and Decode's first payload fill
	// hits the injected error (jpeg fill returns (0, err) with identity when
	// n == 0). A 64×64 baseline cannot decode from 3 scan bytes, so the
	// decoder must read again and surface the error mid-scan.
	jpg := makeJPEG(t, 64, 64)
	sos := bytes.Index(jpg, []byte{0xFF, 0xDA})
	if sos < 0 {
		t.Fatal("JPEG fixture has no SOS marker")
	}
	segLen := int(binary.BigEndian.Uint16(jpg[sos+2:]))
	jpegPrefix := jpg[:sos+2+segLen+3]

	for _, tc := range []struct {
		name   string
		prefix []byte
		err    error
		want   wantKind
	}{
		{"DecodeConfig site, PNG", png[:8], injected, wantMarked},
		{"walk/Decode site, PNG", png[:33], injected, wantMarked},
		{"Decode site, JPEG", jpegPrefix, injected, wantMarked},
		{"verification mismatch (ErrObjectCorrupt wrap)", png[:33],
			fmt.Errorf("%w: expected abc, computed xyz", service.ErrObjectCorrupt), wantMarked},
		{"EOF control: truncation stays ErrUnsupported", png[:33], io.EOF, wantUnsupported},
		{"context-exemption control: same instance", png[:8],
			fmt.Errorf("wrapped: %w", context.DeadlineExceeded), wantSameInstance},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := tc.err
			reader := &errAfterDataReader{data: tc.prefix, err: want}
			_, err := GenerateContext(context.Background(), reader, 32, 32)
			switch tc.want {
			case wantMarked:
				var sre *SourceReadError
				if !errors.As(err, &sre) {
					t.Fatalf("err = %v (%T), want *SourceReadError", err, err)
				}
				if sre.Err != want {
					t.Fatalf("wrapped instance = %v, want the injected instance %v (never re-wrapped)", sre.Err, want)
				}
				if !errors.Is(err, want) {
					t.Fatalf("errors.Is(err, injected) = false, want true (identity must traverse Unwrap)")
				}
				if errors.Is(err, ErrUnsupported) {
					t.Fatalf("err = %v, must not be reclassified as ErrUnsupported", err)
				}
				wantStr := "thumbnail: reading image source stream: " + want.Error()
				if sre.Error() != wantStr {
					t.Fatalf("SourceReadError.Error() = %q, want %q", sre.Error(), wantStr)
				}
			case wantSameInstance:
				// The marker's context exemption: a wrapped context sentinel
				// passes through unmarked and the site's errors.Is fallback
				// returns the exact instance — never re-wrapped, never
				// flattened (mirrors the pinned wrapped-context tests).
				if err != want {
					t.Fatalf("err = %v, want the same wrapped instance %v", err, want)
				}
				if errors.Is(err, ErrUnsupported) {
					t.Fatalf("err = %v, must not be reclassified as ErrUnsupported", err)
				}
				var sre *SourceReadError
				if errors.As(err, &sre) {
					t.Fatalf("err = %v, must not be marked as *SourceReadError", err)
				}
			case wantUnsupported:
				// Truncation = corrupt input: io.EOF passes the marker
				// unmarked and the codec chains synthesize ErrUnexpectedEOF,
				// which keeps classifying as ErrUnsupported (→ 400).
				if !errors.Is(err, ErrUnsupported) {
					t.Fatalf("err = %v, want ErrUnsupported (truncation = corrupt input)", err)
				}
				var sre *SourceReadError
				if errors.As(err, &sre) {
					t.Fatalf("err = %v, must not be marked as *SourceReadError", err)
				}
			}
		})
	}
}

// TestGenerateContextPNGOrientationPartialReadError pins the escaped PNG
// same-Read partial-error seam: once the post-IHDR walk sees 3 bytes plus a
// source error in the same Read, both Generate entry points must return the
// marked *SourceReadError, never succeed and never flatten to ErrUnsupported.
func TestGenerateContextPNGOrientationPartialReadError(t *testing.T) {
	t.Cleanup(func() { recoverSlots(t) })
	png := makePNG(t, 64, 64)
	injected := fmt.Errorf("s3 read failed: %w", errors.New("i/o error"))
	cases := []struct {
		name string
		run  func(io.Reader) ([]byte, error)
	}{
		{"Generate", func(r io.Reader) ([]byte, error) { return Generate(r, 32, 32) }},
		{"GenerateContext", func(r io.Reader) ([]byte, error) { return GenerateContext(context.Background(), r, 32, 32) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			img, err := tc.run(&pngPostIHDRPartialErrorReader{data: png, err: injected})
			var sre *SourceReadError
			if !errors.As(err, &sre) {
				t.Fatalf("err = %v (%T), want *SourceReadError", err, err)
			}
			if sre.Err != injected {
				t.Fatalf("wrapped instance = %v, want the injected instance %v", sre.Err, injected)
			}
			if !errors.Is(err, injected) {
				t.Fatalf("errors.Is(err, injected) = false, want true")
			}
			if errors.Is(err, ErrUnsupported) {
				t.Fatalf("err = %v, must not be ErrUnsupported", err)
			}
			if len(img) != 0 {
				t.Fatalf("len(img) = %d, want 0", len(img))
			}
		})
	}
}

func TestCachedGenerationPNGOrientationPartialReadErrorDoesNotStore(t *testing.T) {
	t.Cleanup(func() { recoverSlots(t) })
	cache := NewCache(1<<20, 0)
	identity := testIdentity("tenant-a")
	png := makePNG(t, 64, 64)
	injected := fmt.Errorf("s3 read failed: %w", errors.New("i/o error"))
	beforeLen, beforeBytes := cache.Len(), cache.Bytes()
	img, fromCache, err := GenerateContextWithOpenerCached(
		context.Background(), cache, identity, etagA, 32, 32,
		func() (io.ReadCloser, OpenedSource, error) {
			return io.NopCloser(&pngPostIHDRPartialErrorReader{data: png, err: injected}), OpenedSource{
				Identity: identity,
				ETag:     etagA,
				Bound:    true,
			}, nil
		},
	)
	var sre *SourceReadError
	if !errors.As(err, &sre) {
		t.Fatalf("err = %v (%T), want *SourceReadError", err, err)
	}
	if sre.Err != injected {
		t.Fatalf("wrapped instance = %v, want the injected instance %v", sre.Err, injected)
	}
	if !errors.Is(err, injected) {
		t.Fatalf("errors.Is(err, injected) = false, want true")
	}
	if fromCache {
		t.Fatal("fromCache = true, want false")
	}
	if len(img) != 0 {
		t.Fatalf("len(img) = %d, want 0", len(img))
	}
	if cache.Len() != beforeLen || cache.Bytes() != beforeBytes {
		t.Fatalf("cache changed: len=%d/%d bytes=%d/%d", cache.Len(), beforeLen, cache.Bytes(), beforeBytes)
	}
}

// TestGenerateContextWithOpenerMidStreamIOErrorClosesStream pins the opener
// funnel — the production entry point (the REST handler runs
// GenerateContextWithOpenerCached over the same body) — on the new error
// path: a marked mid-stream failure surfaces as *SourceReadError and the
// opened stream is closed exactly once (close-before-release contract),
// with the slot recovered (recoverSlots fails loudly on a leak).
func TestGenerateContextWithOpenerMidStreamIOErrorClosesStream(t *testing.T) {
	injected := fmt.Errorf("s3 read failed: %w", errors.New("i/o error"))
	var closed atomic.Int32
	rc := &recordingReadCloser{
		Reader: &errAfterDataReader{data: makePNG(t, 64, 64)[:33], err: injected},
		closed: &closed,
	}
	_, err := GenerateContextWithOpener(context.Background(), 32, 32, func() (io.ReadCloser, error) {
		return rc, nil
	})
	var sre *SourceReadError
	if !errors.As(err, &sre) {
		t.Fatalf("err = %v (%T), want *SourceReadError through the opener funnel", err, err)
	}
	if sre.Err != injected {
		t.Fatalf("wrapped instance = %v, want the injected instance %v", sre.Err, injected)
	}
	if got := closed.Load(); got != 1 {
		t.Fatalf("stream closed %d times, want exactly once", got)
	}
	recoverSlots(t) // a leaked slot fails loudly here
}

// TestGenerateProbeSourceErrorClassified is the AC3b discriminator: a genuine
// source-stream failure surfaced exactly at the MaxSourceBytes cap-probe
// point must classify as *SourceReadError (the marked instance, identity
// preserved — REST maps it to 500/410 via handler_helpers.go, never 400) and
// NEVER as ErrSourceTooLarge (413, a client-budget error). Pre-fix the probe
// error was folded into the capped boolean (c.capped = n > 0 || perr != io.EOF)
// and the decode site returned ErrSourceTooLarge — this test fails loudly on
// that build. The probe fires at off == errAt == MaxSourceBytes (the limit's
// synthesized EOF is the very next read after the source served exactly
// MaxSourceBytes), consumes 0 bytes, and the marker wraps the injected error
// into *SourceReadError; the decode-site probeErr check (before the capped
// check) returns it.
func TestGenerateProbeSourceErrorClassified(t *testing.T) {
	base := appnPaddedJPEG(t, 0)
	sos := bytes.Index(base, []byte{0xFF, 0xDA})
	if sos < 0 {
		t.Fatal("no SOS marker in fixture")
	}
	segLen := int(binary.BigEndian.Uint16(base[sos+2 : sos+4]))
	prefix := base[:sos+2+segLen] // entropy data cut; replaced by zeros to the cap
	genuineErr := fmt.Errorf("s3 read failed: %w", errors.New("i/o error"))
	src := &errAfterSource{prefix: prefix, errAt: MaxSourceBytes, probeErr: genuineErr}
	_, err := Generate(src, 100, 100) // Background ctx: the probe is the classification under test
	var sre *SourceReadError
	if !errors.As(err, &sre) {
		t.Fatalf("err = %v (%T), want *SourceReadError (probe-surfaced storage error)", err, err)
	}
	if sre.Err != genuineErr {
		t.Fatalf("wrapped instance = %v, want the injected instance %v (never re-wrapped)", sre.Err, genuineErr)
	}
	if !errors.Is(err, genuineErr) {
		t.Fatalf("errors.Is(err, injected) = false, want true (identity must traverse Unwrap)")
	}
	if errors.Is(err, ErrSourceTooLarge) {
		t.Fatalf("err = %v, must not be reclassified as ErrSourceTooLarge (413)", err)
	}
}
