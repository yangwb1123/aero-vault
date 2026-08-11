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
	"errors"
	"image/color"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestGenerateSemaphoreBoundsConcurrentAllocation(t *testing.T) {
	// C3: this test needs ~1.2 GiB live heap (16 × 256 MiB worst-case decodes)
	// and ~97s under -race; it must not run in short/CI-race mode. The
	// deterministic C2 tests below still pin the semaphore contract there.
	if testing.Short() {
		t.Skip("8192²×16 concurrent decode needs ~1.2 GiB; run without -short")
	}

	// C1 pin: the concurrency constant is part of the memory ceiling contract.
	// 4 × 256 MiB ≈ 1 GiB for 8-bit PNG RGBA (live per-request worst case ≈
	// 288 MiB incl. scale dst + composite copy → ≈ 1.125 GiB aggregate; see
	// the bit-depth-aware accounting in thumbnail.go). The 2 GiB ceiling
	// below is scoped to this 8-bit baseline — depth-16 sources (8 B/px)
	// legitimately aggregate to ≈ 2.125 GiB, documented separately. A silent
	// raise (4→8→16) must fail this test rather than silently growing the
	// allowed peak.
	if maxConcurrentDecodes != 4 {
		t.Fatalf("maxConcurrentDecodes = %d, want 4 (design pin: 4×256MiB ≈ 1GiB 8-bit baseline)", maxConcurrentDecodes)
	}

	// 8192² uniform NRGBA: tiny compressed fixture, full 256 MiB decode —
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
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(120 * time.Second):
		// C2 watchdog: a slot leak is a package-global permanent DoS — the
		// batch would hang forever; fail fast instead of the 10-min go-test
		// timeout.
		t.Fatal("watchdog: batch did not complete in 120s — decode slot leak?")
	}
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
	// C1 absolute ceiling: even if the constant were raised, live decode
	// memory must never approach the 2 GiB design ceiling (8-bit baseline;
	// the depth-16 aggregate ≈ 2.125 GiB is documented in thumbnail.go and
	// intentionally not subject to this 8-bit-scoped pin).
	if peak > 2<<30 {
		t.Fatalf("peak live heap %d bytes exceeds absolute 2 GiB ceiling", peak)
	}
}

// signalReader blocks on Read until closed; used to prove a Generate call is
// parked before it touches the input stream, then unblocked so the parked
// goroutine exits cleanly (no slot/goroutine leak into later tests).
type signalReader struct {
	c chan struct{}
}

func (r *signalReader) Read([]byte) (int, error) {
	<-r.c
	return 0, errors.New("reader closed")
}

func TestSemaphoreBlocksBeforeDecodeConfig(t *testing.T) {
	// C2 deterministic test 1: with every slot held, a Generate call must
	// park at acquisition — before DecodeConfig, before any input read.
	for i := 0; i < maxConcurrentDecodes; i++ {
		acquireDecodeSlot()
	}
	// No defer release here: the slots are released mid-test to unblock the
	// parked goroutine (below), and the final acquire/release cycle verifies
	// full recovery. A t.Fatal in the parked-branch leaks slots, which is
	// fine — the test has already failed and the process exits.

	r := &signalReader{c: make(chan struct{})}
	entered := make(chan struct{})
	go func() {
		// A read from the stream is the first observable effect of the
		// decode section; if acquisition is correctly placed before the
		// LimitReader/DecodeConfig, this goroutine never reads while
		// parked.
		_, _ = Generate(r, 8, 8)
		close(entered)
	}()
	select {
	case <-entered:
		t.Fatal("Generate returned while all slots held — acquisition is not before decode")
	case <-time.After(200 * time.Millisecond):
		// parked: correct
	}
	// Unblock the parked goroutine: first release the slots so it can enter
	// the decode section, then close the reader so its first Read fails and
	// Generate returns (releasing its slot via defer). Wait for a clean
	// exit so no slot or goroutine leaks into later tests.
	for i := 0; i < maxConcurrentDecodes; i++ {
		releaseDecodeSlot()
	}
	close(r.c)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("parked Generate did not exit after reader close — slot leak?")
	}
	// The goroutine's deferred release must have returned its slot: all
	// slots are acquirable again.
	for i := 0; i < maxConcurrentDecodes; i++ {
		acquireDecodeSlot()
	}
	for i := 0; i < maxConcurrentDecodes; i++ {
		releaseDecodeSlot()
	}
}

func TestSemaphoreReleasesOnError(t *testing.T) {
	// C2 deterministic test 2: an error path (undecodable input) must return
	// the slot — otherwise the package degrades to permanent DoS after one
	// bad request. After Generate fails, all slots must be acquirable.
	_, err := Generate(bytes.NewReader([]byte("not an image")), 8, 8)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
	for i := 0; i < maxConcurrentDecodes; i++ {
		acquireDecodeSlot()
	}
	for i := 0; i < maxConcurrentDecodes; i++ {
		releaseDecodeSlot()
	}
}

// panicReader panics on first read, modelling a reader fault mid-decode.
type panicReader struct{}

func (panicReader) Read([]byte) (int, error) { panic("boom: reader fault") }

func TestSemaphoreReleasesOnPanic(t *testing.T) {
	// C2 deterministic test 3: the defer-based release must survive a panic
	// inside the decode section; a leaked slot would wedge the package.
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic from reader")
		}
		// Panic recovered: Generate's deferred release has run; every slot
		// must now be acquirable (no leak).
		for i := 0; i < maxConcurrentDecodes; i++ {
			acquireDecodeSlot()
		}
		for i := 0; i < maxConcurrentDecodes; i++ {
			releaseDecodeSlot()
		}
	}()
	_, _ = Generate(panicReader{}, 8, 8)
}

// calibrateSingleWorstPeak runs three sequential single Generate calls on
// fixture, sampling the forced-GC live-heap peak during each, and returns the
// maximum peak observed. The sampler polls at ≤ 2 ms (GC time included): the
// 256 MiB decode buffer is live for the entire decode→scale→encode span, so a
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
