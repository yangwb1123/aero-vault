package antivirus

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"
	"time"
)

type scanFn func(context.Context, io.Reader) (Result, error)

func interleavedBest(t *testing.T, s *SignatureScanner, sizes []int) []time.Duration {
	return interleavedBestFunc(t, s.Scan, sizes)
}

func interleavedBestFunc(t *testing.T, fn scanFn, sizes []int) []time.Duration {
	t.Helper()
	bests := make([]time.Duration, len(sizes))
	for i := range bests {
		bests[i] = time.Duration(1<<63 - 1)
	}
	for round := 0; round < 3; round++ {
		for i, n := range sizes {
			start := time.Now()
			if _, err := fn(context.Background(), bytes.NewReader(perfClean[:n])); err != nil {
				t.Fatalf("scan %d bytes: %v", n, err)
			}
			if d := time.Since(start); d < bests[i] {
				bests[i] = d
			}
		}
	}
	return bests
}

// quadraticScan simulates the regression class the pin guards: a window that
// grows with position → O(n²) total work.
func quadraticScan(ctx context.Context, r io.Reader) (Result, error) {
	all, _ := io.ReadAll(r)
	for i := 64 << 10; i < len(all); i += 64 << 10 {
		if bytes.Contains(all[:i], eicarBytes) {
			return Result{Clean: false}, nil
		}
	}
	return Result{Clean: true}, nil
}

func TestPerfProbeAll(t *testing.T) {
	s := NewSignatureScanner(nil)
	bs := interleavedBest(t, s, []int{8 << 20, 64 << 20, 4 << 20})
	fmt.Printf("STREAM bests t8=%v t64=%v tm=%v ratios %.2f %.2f\n",
		bs[0], bs[1], bs[2], float64(bs[1])/float64(bs[0]), float64(bs[2])/float64(bs[0]))

	// quadratic negative control: must exceed 24×
	bq := interleavedBestFunc(t, quadraticScan, []int{8 << 20, 64 << 20})
	q := float64(bq[1]) / float64(bq[0])
	fmt.Printf("QUADRATIC ratio=%.1f (want > 24): %v\n", q, q > 24)

	// parity test slack (sequential, as shipped)
	streaming := time.Duration(1<<63 - 1)
	for i := 0; i < 3; i++ {
		st := time.Now()
		if _, err := s.Scan(context.Background(), bytes.NewReader(perfClean)); err != nil {
			t.Fatal(err)
		}
		if d := time.Since(st); d < streaming {
			streaming = d
		}
	}
	legacy := time.Duration(1<<63 - 1)
	for i := 0; i < 3; i++ {
		st := time.Now()
		if _, err := scanLegacy32MiB(context.Background(), bytes.NewReader(perfClean)); err != nil {
			t.Fatal(err)
		}
		if d := time.Since(st); d < legacy {
			legacy = d
		}
	}
	fmt.Printf("PARITY streaming=%v legacy=%v ratio=%.2f (limit 3)\n", streaming, legacy, float64(streaming)/float64(legacy))
}
