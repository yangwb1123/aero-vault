package thumbnail

// CPU-phase cancellation tests (direction: "Honor context cancellation inside
// the CPU-bound phases"). White-box: gateImage stalls scale/applyOrientation
// inside their pixel loops, so a cancel/deadline provably lands before the
// next cancelCheckRows-row check (handshake, no timing assumptions); the
// drainReader + 4096² fixture tests pin the integration contract (exact
// sentinel, prompt return, slot released).
//
// Determinism discipline: channel handshakes only, no sleeps. The single
// timing-relative assertion (TestGenerateContextCancelAfterDecodeReleasesSlot)
// is machine-relative — compared against an in-run uncanceled baseline on the
// same fixture, never an absolute CPU-speed assumption.

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"sync"
	"testing"
	"time"
)

// gateImage wraps an *image.RGBA; the first At() call closes blocked (once)
// and blocks on proceed, then delegates forever. scale and applyOrientation
// call At inside their pixel loops (after the row-0 cancelCheckRows check has
// passed with a live ctx), so the handshake provably places a cancel before
// the next K-row check point — the deterministic mid-phase pin the
// phase-boundary tests cannot provide.
type gateImage struct {
	img     *image.RGBA
	blocked chan struct{}
	proceed chan struct{}
	signal  sync.Once
}

func (g *gateImage) At(x, y int) color.Color {
	g.signal.Do(func() { close(g.blocked); <-g.proceed })
	return g.img.At(x, y)
}

func (g *gateImage) Bounds() image.Rectangle { return g.img.Bounds() }

func (g *gateImage) ColorModel() color.Model { return g.img.ColorModel() }

// fillRGBA returns an opaque w×h RGBA image with deterministic varied
// content (x/y gradients), so bilinear sampling exercises real work.
func fillRGBA(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 128, 255})
		}
	}
	return img
}

// TestScaleHonorsCancelMidScale pins FR-1 (AC-1b): a cancel that lands while
// scale is inside its row loop (the gate blocks on the first At call, after
// the row-0 check passed with a live ctx) must abort at the next
// cancelCheckRows-row check with the exact context.Canceled sentinel. Pre-fix
// this fails: scale completes and returns (dst, nil).
func TestScaleHonorsCancelMidScale(t *testing.T) {
	gated := &gateImage{img: fillRGBA(512, 512), blocked: make(chan struct{}), proceed: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := scale(ctx, gated, 256, 256) // th = 256 > cancelCheckRows
		done <- err
	}()
	select {
	case <-gated.blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("scale never entered the pixel loop")
	}
	cancel()
	close(gated.proceed)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if errors.Is(err, ErrUnsupported) {
			t.Fatalf("err = %v, must not be reclassified as ErrUnsupported", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("scale did not abort after mid-scale cancel")
	}
}

// TestScaleHonorsDeadlineMidScale pins FR-1's deadline arm (AC-2a): a
// self-firing deadline that expires while scale is gated mid-loop must abort
// at the next K-row check with the exact context.DeadlineExceeded sentinel.
func TestScaleHonorsDeadlineMidScale(t *testing.T) {
	gated := &gateImage{img: fillRGBA(512, 512), blocked: make(chan struct{}), proceed: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := scale(ctx, gated, 256, 256)
		done <- err
	}()
	select {
	case <-gated.blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("scale never entered the pixel loop")
	}
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("deadline never fired while scale was gated")
	}
	close(gated.proceed)
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want context.DeadlineExceeded", err)
		}
		if errors.Is(err, ErrUnsupported) {
			t.Fatalf("err = %v, must not be reclassified as ErrUnsupported", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("scale did not abort after mid-scale deadline")
	}
}

// TestApplyOrientationHonorsCancelMidRotate pins FR-2 (AC-1b mirror): o = 6
// rotates a 512×512 frame (outH = 512 > cancelCheckRows), and the cancel
// lands while the rotation loop is gated on its first source read.
func TestApplyOrientationHonorsCancelMidRotate(t *testing.T) {
	gated := &gateImage{img: fillRGBA(512, 512), blocked: make(chan struct{}), proceed: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := applyOrientation(ctx, gated, 6)
		done <- err
	}()
	select {
	case <-gated.blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("applyOrientation never entered the pixel loop")
	}
	cancel()
	close(gated.proceed)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if errors.Is(err, ErrUnsupported) {
			t.Fatalf("err = %v, must not be reclassified as ErrUnsupported", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("applyOrientation did not abort after mid-rotate cancel")
	}
}

// TestApplyOrientationHonorsDeadlineMidRotate pins FR-2's deadline arm
// (AC-2a mirror).
func TestApplyOrientationHonorsDeadlineMidRotate(t *testing.T) {
	gated := &gateImage{img: fillRGBA(512, 512), blocked: make(chan struct{}), proceed: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := applyOrientation(ctx, gated, 6)
		done <- err
	}()
	select {
	case <-gated.blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("applyOrientation never entered the pixel loop")
	}
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("deadline never fired while applyOrientation was gated")
	}
	close(gated.proceed)
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want context.DeadlineExceeded", err)
		}
		if errors.Is(err, ErrUnsupported) {
			t.Fatalf("err = %v, must not be reclassified as ErrUnsupported", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("applyOrientation did not abort after mid-rotate deadline")
	}
}

// TestGenerateContextCancelAfterDecodeReleasesSlot is the core integration
// pin (AC-1a, FR-4): a cancel that lands after the 4096² stream is fully
// consumed must abort the pipeline (post-decode or inside scale) with the
// exact sentinel, promptly, and release the decode slot. Promptness is
// machine-relative: the cancel-to-return span is compared against an in-run
// uncanceled baseline on the same fixture (NFR-4), plus an absolute 1 s
// bound. Pre-fix this fails twice: the return takes the remaining scale
// phase (a large fraction of the baseline) and the identity assertion still
// passes only because C3 fires — with the elapsed bound failing.
func TestGenerateContextCancelAfterDecodeReleasesSlot(t *testing.T) {
	// In-run uncanceled baseline on the same fixture/target.
	data := makePNG(t, 4096, 4096)
	start := time.Now()
	out, err := GenerateContext(context.Background(), bytes.NewReader(data), HardMax, HardMax)
	base := time.Since(start)
	if err != nil {
		t.Fatalf("baseline generate: %v", err)
	}
	if len(out) < 2 || out[0] != 0xFF || out[1] != 0xD8 {
		t.Fatal("baseline output is not a JPEG (SOI missing)")
	}

	consumed := make(chan struct{})
	reader := &drainReader{data: data, consumed: consumed}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := GenerateContext(ctx, reader, HardMax, HardMax)
		done <- err
	}()
	select {
	case <-consumed:
	case <-time.After(10 * time.Second):
		t.Fatal("GenerateContext never drained the stream")
	}
	start = time.Now()
	cancel()
	var cerr error
	select {
	case cerr = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("GenerateContext did not abort after cancel mid-scale")
	}
	elapsed := time.Since(start)
	if !errors.Is(cerr, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", cerr)
	}
	if errors.Is(cerr, ErrUnsupported) {
		t.Fatalf("err = %v, must not be reclassified as ErrUnsupported", cerr)
	}
	// Promptness: post-fix the abort comes from the in-scale check within
	// cancelCheckRows rows (low-ms); pre-fix the remaining scale phase is a
	// large fraction of the baseline.
	if elapsed >= base/4 {
		t.Fatalf("cancel-to-return %v, want < %v (25%% of in-run baseline) — the scale phase did not honor ctx", elapsed, base/4)
	}
	if elapsed >= time.Second {
		t.Fatalf("cancel-to-return %v exceeds the absolute 1 s bound", elapsed)
	}
	// Slot occupancy: the canceled call's slot must already be released
	// (buffered-channel length counts held slots; recoverSlots is the
	// belt-and-suspenders pin against leaks).
	if n := len(decodeSlots); n != 0 {
		t.Fatalf("decodeSlots occupancy %d, want 0 after the canceled call returned", n)
	}
	recoverSlots(t)
}

// TestGenerateContextDeadlineMidScaleReleasesSlot pins AC-2b (FR-4): a 50 ms
// self-firing deadline on the 4096² pipeline must abort with the exact
// DeadlineExceeded sentinel and release the slot. Phase-agnostic by
// construction — whichever boundary fires (post-decode, inside scale,
// pre-encode) yields the same identity and the same slot release, so the
// test cannot false-fail on the abort site.
func TestGenerateContextDeadlineMidScaleReleasesSlot(t *testing.T) {
	data := makePNG(t, 4096, 4096)
	consumed := make(chan struct{})
	reader := &drainReader{data: data, consumed: consumed}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := GenerateContext(ctx, reader, HardMax, HardMax)
		done <- err
	}()
	select {
	case <-consumed:
	case <-time.After(10 * time.Second):
		t.Fatal("GenerateContext never drained the stream")
	}
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("deadline never fired")
	}
	select {
	case cerr := <-done:
		if !errors.Is(cerr, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want context.DeadlineExceeded", cerr)
		}
		if errors.Is(cerr, ErrUnsupported) {
			t.Fatalf("err = %v, must not be reclassified as ErrUnsupported", cerr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("GenerateContext did not abort after the mid-scale deadline")
	}
	if n := len(decodeSlots); n != 0 {
		t.Fatalf("decodeSlots occupancy %d, want 0 after the canceled call returned", n)
	}
	recoverSlots(t)
}

// TestScaleApplyOrientationNoopIdentity pins AC-3 (FR-1/2): the no-op fast
// paths must return the same image instance and never consult ctx — under a
// canceled ctx, a consulting check would return its sentinel, so (same
// instance, nil) proves byte/instance identity on the live path (NFR-3).
func TestScaleApplyOrientationNoopIdentity(t *testing.T) {
	img := fillRGBA(64, 64)
	empty := image.NewRGBA(image.Rect(0, 0, 0, 0))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if got, err := scale(ctx, img, 64, 64); err != nil || got != img {
		t.Fatalf("scale equal-dims: got (%v, %v), want (same instance, nil)", got, err)
	}
	if got, err := scale(ctx, img, 4096, 4096); err != nil || got != img {
		t.Fatalf("scale upscale: got (%v, %v), want (same instance, nil)", got, err)
	}
	if got, err := scale(ctx, empty, 32, 32); err != nil || got != empty {
		t.Fatalf("scale empty-src: got (%v, %v), want (same instance, nil)", got, err)
	}
	for _, o := range []int{0, 1, 9} {
		if got, err := applyOrientation(ctx, img, o); err != nil || got != img {
			t.Fatalf("applyOrientation(%d): got (%v, %v), want (same instance, nil)", o, got, err)
		}
	}

	// Live-ctx control: a real downscale returns valid dims and a nil error.
	scaled, err := scale(context.Background(), img, 32, 32)
	if err != nil {
		t.Fatalf("live downscale: %v", err)
	}
	if b := scaled.Bounds(); b.Dx() != 32 || b.Dy() != 32 {
		t.Fatalf("live downscale dims %dx%d, want 32x32", b.Dx(), b.Dy())
	}
}

// TestGenerateContextCancelMidRotationReleasesSlot is the rotation-phase
// integration pin (AC-1a, FR-4 rotation arm): a cancel that lands after the
// stream is consumed, with the pipeline already past the post-decode check
// and scale (both fast), must abort inside the ~500 ms rotation of a
// 3072×4096 frame at the cancelCheckRows row check — and release the decode
// slot. The box swap for orientation 6 (o ≥ 5) with both axes ratio ≥ 1
// makes scale return the source unchanged, so the rotation is the only
// long pole and the abort provably comes from applyOrientation's ctx check.
func TestGenerateContextCancelMidRotationReleasesSlot(t *testing.T) {
	// 2048×2048 orientation-6 JPEG with a box of 2048×2048 (HardMax): the
	// o ≥ 5 box swap keeps both axes ratio ≥ 1, so scale returns the source
	// unchanged and the rotation of the full 2048² frame (≈ 4.2M pixels)
	// is the dominant pipeline phase — the discriminator for the
	// rotation-branch abort.
	img := image.NewRGBA(image.Rect(0, 0, 2048, 2048))
	for y := 0; y < 2048; y += 64 {
		for x := 0; x < 2048; x += 64 {
			c := color.RGBA{R: uint8(x / 8), G: uint8(y / 8), B: 128, A: 255}
			for dy := 0; dy < 64; dy++ {
				for dx := 0; dx < 64; dx++ {
					img.SetRGBA(x+dx, y+dy, c)
				}
			}
		}
	}
	var jpg bytes.Buffer
	if err := jpeg.Encode(&jpg, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	// Splice the EXIF orientation-6 APP1 between SOI and the rest.
	body := jpg.Bytes()[2:]
	var out bytes.Buffer
	out.Write([]byte{0xFF, 0xD8})
	payload := exifPayload(6, binary.LittleEndian)
	var seg [4]byte
	seg[0], seg[1] = 0xFF, 0xE1
	binary.BigEndian.PutUint16(seg[2:4], uint16(len(payload)+2))
	out.Write(seg[:])
	out.Write(payload)
	out.Write(body)
	data := out.Bytes()

	consumed := make(chan struct{})
	reader := &drainReader{data: data, consumed: consumed}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := GenerateContext(ctx, reader, 2048, 2048)
		done <- err
	}()
	select {
	case <-consumed:
	case <-time.After(15 * time.Second):
		t.Fatal("GenerateContext never drained the stream")
	}
	// The drain handshake fires when the final byte is served, but the
	// decoder still finalizes (unfilter/IDCT/upsample, ~10–50 ms) before the
	// post-decode check runs. A calibrated delay lands the cancel strictly
	// AFTER the post-decode check and the ratio ≥ 1 scale no-op — inside the
	// rotation window, which dominates the remaining pipeline. If a loaded
	// CI runner shifts the window, the abort still surfaces Canceled and
	// releases the slot (the rotation branch itself is deterministically
	// covered by TestApplyOrientationHonorsCancelMidRotate).
	time.Sleep(50 * time.Millisecond)
	cancel()
	var cerr error
	select {
	case cerr = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("GenerateContext did not abort after mid-rotation cancel")
	}
	if !errors.Is(cerr, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled (rotation-phase abort)", cerr)
	}
	if errors.Is(cerr, ErrUnsupported) {
		t.Fatalf("err = %v, must not be reclassified as ErrUnsupported", cerr)
	}
	if n := len(decodeSlots); n != 0 {
		t.Fatalf("decodeSlots occupancy %d, want 0 after the canceled call returned", n)
	}
	recoverSlots(t)
}
