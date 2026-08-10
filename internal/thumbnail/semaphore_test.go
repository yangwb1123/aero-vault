package thumbnail

// Aggregate decode-memory bound test (direction: "Bound aggregate thumbnail
// decode memory: package-level concurrency semaphore"). White-box so the
// unexported semaphore state (decodeSlots) is observable.
//
// Measurement rationale: runtime.MemStats.TotalAlloc is a monotonic cumulative
// counter — even a perfectly semaphore-limited batch totals N × worst case, so
// a naive "peak TotalAlloc" reading is unmeasurable. The quantity intended —
// peak memory held simultaneously across goroutines — is measured as peak
// live heap (HeapAlloc) sampled by a goroutine that calls runtime.GC()
// immediately before each ReadMemStats: forcing GC makes each sample reflect
// only allocations that are live and uncollectable (i.e. in-flight decode
// buffers), which is exactly "peak allocation across goroutines" in the
// held-concurrently sense, and removes GC-timing flakiness. TotalAlloc-delta
// stays appropriate for the existing single-call allocation tests and is
// untouched there.

import (
	"bytes"
	"image/color"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestGenerateSemaphoreBoundsConcurrentAllocation(t *testing.T) {
	// 8192² uniform NRGBA: tiny compressed fixture, full 268 MiB decode —
	// the documented worst case exactly at the MaxSourceDim boundary
	// (pre-check is `> MaxSourceDim`, so 8192 passes).
	fixture := uniformPNG(t, MaxSourceDim, MaxSourceDim, color.NRGBA{R: 12, G: 34, B: 56, A: 255})

	singleWorstPeak := calibrateSingleWorstPeak(t, fixture) // 3 runs, max of peaks
	if singleWorstPeak < 100<<20 {
		t.Fatalf("calibration did not observe a genuine decode peak (%d bytes)", singleWorstPeak)
	}

	const n = 4 * maxConcurrentDecodes // 16 at the current constant
	results := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := Generate(bytes.NewReader(fixture), HardMax, HardMax)
			results[i] = err
		}(i)
	}

	stop := make(chan struct{})
	var peak uint64
	var peakInFlight int
	var sampler sync.WaitGroup
	sampler.Add(1)
	go func() { // sampler+poller: forced-GC HeapAlloc peak + len(decodeSlots) peak
		defer sampler.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			runtime.GC()
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			if m.HeapAlloc > peak {
				peak = m.HeapAlloc
			}
			if v := len(decodeSlots); v > peakInFlight {
				peakInFlight = v
			}
			time.Sleep(time.Millisecond)
		}
	}()

	close(start)
	wg.Wait()
	close(stop)
	sampler.Wait()

	for i, err := range results {
		if err != nil {
			t.Fatalf("generate %d: %v", i, err) // blocking, never drops: all 16 must succeed
		}
	}
	if peakInFlight < 2 {
		t.Fatalf("semaphore never engaged: max in-flight = %d, want >= 2", peakInFlight)
	}
	if peakInFlight > maxConcurrentDecodes {
		t.Fatalf("semaphore cap exceeded: max in-flight = %d, want <= %d", peakInFlight, maxConcurrentDecodes)
	}
	if limit := uint64(maxConcurrentDecodes)*singleWorstPeak + 64<<20; peak > limit {
		t.Fatalf("peak live heap %d bytes exceeds semaphore bound %d", peak, limit)
	}
}

// calibrateSingleWorstPeak runs three sequential single Generate calls on
// fixture, sampling the forced-GC live-heap peak during each, and returns the
// maximum peak observed. The sampler polls at ≤ 2 ms (GC time included): the
// 268 MiB decode buffer is live for the entire decode→scale→encode span, so a
// tight poll cannot miss it; the caller's 100 MiB floor guard fails the test
// if it ever does.
func calibrateSingleWorstPeak(t *testing.T, fixture []byte) uint64 {
	t.Helper()
	var peak uint64
	for run := 0; run < 3; run++ {
		stop := make(chan struct{})
		done := make(chan uint64, 1)
		go func() {
			var p uint64
			for {
				select {
				case <-stop:
					done <- p
					return
				default:
				}
				runtime.GC()
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				if m.HeapAlloc > p {
					p = m.HeapAlloc
				}
				time.Sleep(time.Millisecond)
			}
		}()
		_, err := Generate(bytes.NewReader(fixture), HardMax, HardMax)
		if err != nil {
			t.Fatalf("calibration generate: %v", err)
		}
		close(stop)
		if p := <-done; p > peak {
			peak = p
		}
	}
	return peak
}
