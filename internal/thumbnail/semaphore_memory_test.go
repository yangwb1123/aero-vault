package thumbnail

// HeapAlloc includes unreachable buffers awaiting GC. Sampling it while
// decoders retire and start is not a live in-flight memory measurement, even
// after runtime.GC: the concurrent mark can retain retiring frames, and new
// frames can be allocated before ReadMemStats. Hold each cohort at a source
// read with full pixel buffers live instead. A full-pipeline TotalAlloc budget
// also covers allocations AFTER that snapshot; neither bound is a process RSS
// or unreclaimed-heap limit.

import (
	"bytes"
	"context"
	"image/color"
	"runtime"
	"testing"
	"time"
)

func TestGenerateSemaphoreBoundsConcurrentAllocation(t *testing.T) {
	if testing.Short() {
		t.Skip("8192²×16 concurrent decode needs ~1.2 GiB; run without -short")
	}
	if maxConcurrentDecodes != 4 {
		t.Fatalf("maxConcurrentDecodes = %d, want 4", maxConcurrentDecodes)
	}
	fixture := uniformPNG(t, MaxSourceDim, MaxSourceDim, color.NRGBA{R: 12, G: 34, B: 56, A: 255})
	if len(fixture) <= 4096 {
		t.Fatal("fixture fits source read-ahead; the gate could precede pixel allocation")
	}
	single := calibrateSingleDecodeAllocation(t, fixture)
	frameBytes := uint64(MaxSourceDim) * MaxSourceDim * 4
	if single < frameBytes {
		t.Fatalf("full-pipeline allocation %d omits the %d-byte source frame", single, frameBytes)
	}
	limit := uint64(maxConcurrentDecodes)*single + 64<<20
	if limit > 2<<30 {
		t.Fatalf("aggregate full-pipeline allocation budget %d exceeds absolute 2 GiB ceiling", limit)
	}
	const n = 4 * maxConcurrentDecodes
	ready := startHeldMemoryDecodes(t, fixture, n)
	deadline := time.After(120 * time.Second)
	var peak uint64
	for batch := 0; batch < n/maxConcurrentDecodes; batch++ {
		cohort := awaitHeldMemoryDecodes(t, ready, deadline)
		peak = max(peak, heldDecodeHeap(t, frameBytes, limit))
		for _, decoder := range cohort {
			decoder.resume()
		}
		for _, decoder := range cohort {
			awaitMemoryDecodeResult(t, decoder, deadline)
		}
	}
	if len(decodeSlots) != 0 {
		t.Fatalf("batch left %d decode slots held", len(decodeSlots))
	}
	t.Logf("single full-pipeline allocations=%d, peak held decode heap=%d, aggregate limit=%d", single, peak, limit)
}

func calibrateSingleDecodeAllocation(t *testing.T, fixture []byte) uint64 {
	t.Helper()
	var worst uint64
	for run := 0; run < 3; run++ {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		_, err := Generate(bytes.NewReader(fixture), HardMax, HardMax)
		runtime.ReadMemStats(&after)
		if err != nil {
			t.Fatalf("calibration generate: %v", err)
		}
		worst = max(worst, after.TotalAlloc-before.TotalAlloc)
	}
	return worst
}

func startHeldMemoryDecodes(t *testing.T, fixture []byte, n int) <-chan *heldMemoryDecode {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan *heldMemoryDecode, n)
	decoders := make([]*heldMemoryDecode, n)
	start := make(chan struct{})
	for i := range decoders {
		decoder := newHeldMemoryDecode(fixture, ready)
		decoders[i] = decoder
		go func() {
			<-start
			_, decoder.err = GenerateContext(ctx, decoder, HardMax, HardMax)
			if !decoder.reached {
				ready <- decoder // early failure must not strand the gate waiter
			}
			close(decoder.done)
		}()
	}
	t.Cleanup(func() {
		cancel()
		for _, decoder := range decoders {
			decoder.resume()
		}
		deadline := time.After(120 * time.Second)
		for _, decoder := range decoders {
			select {
			case <-decoder.done:
			case <-deadline:
				t.Error("watchdog: decode cleanup did not complete in 120s")
				return
			}
		}
	})
	close(start)
	return ready
}

func awaitHeldMemoryDecodes(t *testing.T, ready <-chan *heldMemoryDecode, deadline <-chan time.Time) []*heldMemoryDecode {
	t.Helper()
	cohort := make([]*heldMemoryDecode, 0, maxConcurrentDecodes)
	for range maxConcurrentDecodes {
		select {
		case decoder := <-ready:
			if !decoder.reached {
				awaitMemoryDecodeResult(t, decoder, deadline)
				t.Fatal("decode completed without reaching the full-frame gate")
			}
			cohort = append(cohort, decoder)
		case <-deadline:
			t.Fatal("watchdog: batch did not reach decode gates in 120s — slot leak?")
		}
	}
	return cohort
}

func awaitMemoryDecodeResult(t *testing.T, decoder *heldMemoryDecode, deadline <-chan time.Time) {
	t.Helper()
	select {
	case <-decoder.done:
		if decoder.err != nil {
			t.Fatalf("generate: %v", decoder.err)
		}
	case <-deadline:
		t.Fatal("watchdog: batch did not complete in 120s — decode slot leak?")
	}
}

func heldDecodeHeap(t *testing.T, frameBytes, limit uint64) uint64 {
	t.Helper()
	if held := len(decodeSlots); held != maxConcurrentDecodes {
		t.Fatalf("gated decoders hold %d slots, want %d", held, maxConcurrentDecodes)
	}
	runtime.GC()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	if floor := uint64(maxConcurrentDecodes) * frameBytes; stats.HeapAlloc < floor {
		t.Fatalf("held heap %d omits %d bytes of decoded frames", stats.HeapAlloc, floor)
	}
	if stats.HeapAlloc > limit {
		t.Fatalf("held decode heap %d exceeds semaphore bound %d", stats.HeapAlloc, limit)
	}
	if stats.HeapAlloc > 2<<30 {
		t.Fatalf("held decode heap %d exceeds absolute 2 GiB ceiling", stats.HeapAlloc)
	}
	return stats.HeapAlloc
}
