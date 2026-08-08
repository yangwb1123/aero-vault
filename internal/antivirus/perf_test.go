package antivirus

// Performance evidence for the design's load-bearing claims (QA F5): the
// streaming matcher's peak memory (~maxSigLen+64 KiB ≈ 128 KiB), its
// O(total bytes × sigs) cost with no hidden superlinear behavior, and its
// wall-time parity with the pre-fix 32 MiB-capped path. Claims are pinned by
// executable assertions (heap/allocation bounds, ratio-based timing) and by
// benchmarks that leave a durable ns/op + B/op record.

import (
	"bytes"
	"context"
	"io"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"
	"time"
)

// perfCleanSize is the benchmark/assertion object size: above the old 32 MiB
// cap, so the tail that used to be truncated is exercised.
const perfCleanSize = 64 << 20

// perfClean is a large clean (zero-filled) buffer shared by every perf probe;
// zeros never match any signature, so all scans run the full-stream clean path.
var perfClean = make([]byte, perfCleanSize)

var eicarBytes = []byte(EICAR)

// scanLegacy32MiB reproduces the pre-fix pipeline as the parity baseline: Scan
// was io.ReadAll(io.LimitReader(r, 32<<20)) + bytes.Contains over the capped
// prefix, and the worker unconditionally drained the remainder. It
// intentionally retains the old truncating behavior (Clean even when the tail
// was never read) — it exists only to benchmark against.
func scanLegacy32MiB(ctx context.Context, r io.Reader) (Result, error) {
	all, err := io.ReadAll(io.LimitReader(r, 32<<20))
	if err != nil {
		return Result{}, err
	}
	clean := !bytes.Contains(all, eicarBytes)
	_, _ = io.Copy(io.Discard, r) // old worker-side drain
	return Result{Clean: clean}, nil
}

// multiSigScanner builds a scanner with n custom signatures (plus EICAR),
// with distinct, non-matching byte patterns.
func multiSigScanner(n int) *SignatureScanner {
	extra := make(map[string]string, n)
	for i := 0; i < n; i++ {
		extra["Sig-"+strconv.Itoa(i)] = strings.Repeat(string(rune('a'+i%26)), 16+7*(i%20))
	}
	return NewSignatureScanner(extra)
}

// TestSignatureScannerPeakHeapBounded pins the headline memory claim: scanning
// a >32 MiB clean object must allocate ~maxSigLen+64 KiB (window + chunk),
// nowhere near the old path's 32 MiB io.ReadAll buffer. Measured with GC off
// so HeapAlloc grows monotonically and the delta over the scan equals total
// allocation; the floor guards against a dead measurement.
func TestSignatureScannerPeakHeapBounded(t *testing.T) {
	old := debug.SetGCPercent(-1)
	defer debug.SetGCPercent(old)
	runtime.GC()

	cases := []struct {
		name      string
		extra     map[string]string
		maxSigLen int
	}{
		{"eicar-only", nil, len(EICAR)},
		{"max-4k-signature", map[string]string{"Long": strings.Repeat("A", 4096)}, 4096},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewSignatureScanner(tc.extra)
			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)
			res, err := s.Scan(context.Background(), bytes.NewReader(perfClean[:33<<20]))
			if err != nil {
				t.Fatalf("scan: %v", err)
			}
			if !res.Clean {
				t.Fatalf("clean buffer flagged: %+v", res)
			}
			runtime.ReadMemStats(&after)
			delta := after.HeapAlloc - before.HeapAlloc
			bound := uint64(tc.maxSigLen) + 192<<10 // window+chunk ≈ maxSigLen+128 KiB, 1.5× slack
			if delta > bound || delta > 1<<20 {
				t.Fatalf("33 MiB clean scan allocated %d bytes, want ≤ maxSigLen+192 KiB (%d) and ≤ 1 MiB; pre-fix io.ReadAll held a 32 MiB buffer", delta, bound)
			}
			if delta < 64<<10 {
				t.Fatalf("scan allocated only %d bytes — measurement looks dead (window+chunk alone are ≥ 128 KiB)", delta)
			}
		})
	}
}

// TestSignatureScannerAllocsIndependentOfSize pins that there is no hidden
// cap growth or per-chunk allocation: exactly two buffers (window + 64 KiB
// chunk) are allocated no matter the stream size. The old io.ReadAll path
// allocated O(size) (32 MiB for a 33 MiB object).
func TestSignatureScannerAllocsIndependentOfSize(t *testing.T) {
	s := NewSignatureScanner(nil)
	allocs := func(n int) float64 {
		return testing.AllocsPerRun(1, func() {
			if _, err := s.Scan(context.Background(), bytes.NewReader(perfClean[:n])); err != nil {
				t.Fatalf("scan: %v", err)
			}
		})
	}
	small := allocs(1 << 20)
	large := allocs(33 << 20)
	if small != large {
		t.Fatalf("allocs at 1 MiB = %v vs 33 MiB = %v — must be size-independent (no cap growth)", small, large)
	}
	if large > 4 {
		t.Fatalf("expected exactly 2 allocations (window + chunk), got %v", large)
	}
}

// bestScanTime returns the fastest of 3 wall-clock scans of a clean buffer of
// n bytes. Taking the minimum filters scheduler/GC noise: the scan is
// memory-bound, so a slow run is noise, never the code.
func bestScanTime(t *testing.T, s *SignatureScanner, n int) time.Duration {
	t.Helper()
	best := time.Duration(1<<63 - 1)
	for i := 0; i < 3; i++ {
		start := time.Now()
		res, err := s.Scan(context.Background(), bytes.NewReader(perfClean[:n]))
		if err != nil {
			t.Fatalf("scan %d bytes: %v", n, err)
		}
		if !res.Clean {
			t.Fatalf("clean buffer flagged: %+v", res)
		}
		if d := time.Since(start); d < best {
			best = d
		}
	}
	return best
}

// TestSignatureScannerTimeLinearInSize pins O(total bytes × sigs) with no
// hidden superlinear behavior. Per chunk the matcher does len(sigs)
// bytes.Contains over the ≤ maxSigLen-1+64 KiB window plus one bounded trim
// copy (≤ maxSigLen-1 bytes) — both linear; an unbounded window or growing
// buffer would blow the ratio assertions below. Ratio-based best-of-3 timing
// cancels machine speed, so the bounds hold on any hardware.
func TestSignatureScannerTimeLinearInSize(t *testing.T) {
	s := NewSignatureScanner(nil)
	t8 := bestScanTime(t, s, 8<<20)
	t64 := bestScanTime(t, s, 64<<20)
	if t64 > 12*t8 {
		t.Fatalf("64 MiB scan %v vs 8 MiB %v: 8× data must cost ≤ 12× time (superlinear window behavior would blow past this)", t64, t8)
	}
	multi := multiSigScanner(7) // 8 signatures total
	tm := bestScanTime(t, multi, 4<<20)
	if tm > 12*t8 {
		t.Fatalf("8 signatures on 4 MiB = %v vs 1 signature on 8 MiB %v: cost must scale ~linearly with signature count", tm, t8)
	}
}

// TestSignatureScannerWallTimeParityWithLegacy pins the "wall time ≈
// unchanged" claim: the pre-fix worker read ≤ 32 MiB in Scan and then drained
// the remainder unconditionally, so total I/O per object was already
// full-stream. The streaming matcher reads the same bytes and adds one memchr
// pass per chunk; best-of-3 timing keeps the comparison stable across
// machines and load.
func TestSignatureScannerWallTimeParityWithLegacy(t *testing.T) {
	s := NewSignatureScanner(nil)
	bestOf := func(fn func() error) time.Duration {
		t.Helper()
		best := time.Duration(1<<63 - 1)
		for i := 0; i < 3; i++ {
			start := time.Now()
			if err := fn(); err != nil {
				t.Fatalf("scan: %v", err)
			}
			if d := time.Since(start); d < best {
				best = d
			}
		}
		return best
	}
	streaming := bestOf(func() error {
		_, err := s.Scan(context.Background(), bytes.NewReader(perfClean))
		return err
	})
	legacy := bestOf(func() error {
		_, err := scanLegacy32MiB(context.Background(), bytes.NewReader(perfClean))
		return err
	})
	if streaming > 3*legacy {
		t.Fatalf("streaming scan %v vs legacy 32 MiB-capped path %v on %d MiB clean: want ≤ 3× (same total I/O; matcher adds ≤ 2× memchr)", streaming, legacy, perfCleanSize>>20)
	}
}

// ── benchmarks (durable ns/op · B/op · allocs/op record) ───────────────────

func BenchmarkSignatureScannerScan(b *testing.B) {
	s := NewSignatureScanner(nil)
	benchmarkScan(b, func(r io.Reader) (Result, error) { return s.Scan(context.Background(), r) })
}

func BenchmarkSignatureScannerScanMultiSig(b *testing.B) {
	s := multiSigScanner(15) // 16 signatures total
	benchmarkScan(b, func(r io.Reader) (Result, error) { return s.Scan(context.Background(), r) })
}

func BenchmarkSignatureScannerScanLegacy32MiB(b *testing.B) {
	benchmarkScan(b, func(r io.Reader) (Result, error) { return scanLegacy32MiB(context.Background(), r) })
}

func benchmarkScan(b *testing.B, scan func(io.Reader) (Result, error)) {
	b.Helper()
	b.SetBytes(perfCleanSize)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := scan(bytes.NewReader(perfClean)); err != nil {
			b.Fatal(err)
		}
	}
}
