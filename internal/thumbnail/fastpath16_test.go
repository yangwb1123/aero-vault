package thumbnail

// Deterministic builders and the depth-16-specific pins for the RGBA64 /
// NRGBA64 / Gray16 direct-Pix kernels (pixfast_16.go). The parity battery
// itself (TestFastPathByteIdentity) lives in fastpath_test.go; its fixtures
// map and SubImage arms reference the builders defined here. All builders
// are deterministic (suite discipline: no randomness) and their content is
// chosen to discriminate the kernels' conversion branches: the RGBA64
// gradient covers 0 and 0xffff in R/G (the truncation boundary 0xffff>>8 =
// 255) with α=0 RGB≠0 straight color; the NRGBA64 ramp is the
// anti-correlated α/c pair (α rising, c = 0xffff−α) — correlated
// premultiply rounding, the strongest NRGBA64 discriminator; the Gray16
// fixtures pin the no-0x101 rule (y=0xffff must stay 0xffff, and the solid
// 0x8000 mid-value at non-zero lerp weights).
import (
	"context"
	"errors"
	"image"
	"image/color"
	"testing"
)

// buildRGBA64 returns a deterministic opaque w×h *image.RGBA64: R ramps
// x*65535/max(1,w-1) (covers 0 and 0xffff), G ramps y*65535/max(1,h-1),
// B is a mid value, A=0xffff.
func buildRGBA64(w, h int) *image.RGBA64 {
	img := image.NewRGBA64(image.Rect(0, 0, w, h))
	wm, hm := max(1, w-1), max(1, h-1)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA64(x, y, color.RGBA64{
				R: uint16(x * 65535 / wm),
				G: uint16(y * 65535 / hm),
				B: 0x8000,
				A: 0xffff,
			})
		}
	}
	return img
}

// buildRGBA64AlphaZero returns straight RGBA64 with A=0 and non-zero RGB
// everywhere (legal in non-premultiplied RGBA64) — discriminates the
// no-premultiply read: the scaled output must retain RGB exactly as the
// generic path does.
func buildRGBA64AlphaZero(w, h int) *image.RGBA64 {
	img := image.NewRGBA64(image.Rect(0, 0, w, h))
	wm, hm := max(1, w-1), max(1, h-1)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA64(x, y, color.RGBA64{
				R: uint16(x*65535/wm + 1),
				G: uint16(y*65535/hm + 2),
				B: uint16((x + y) % 65536),
				A: 0,
			})
		}
	}
	return img
}

// buildNRGBA64 returns a deterministic opaque w×h *image.NRGBA64. For
// opaque pixels the NRGBA64 premultiply is the identity, so this
// discriminates the non-premultiplied classes from the ramp below.
func buildNRGBA64(w, h int) *image.NRGBA64 {
	img := image.NewNRGBA64(image.Rect(0, 0, w, h))
	wm, hm := max(1, w-1), max(1, h-1)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA64(x, y, color.NRGBA64{
				R: uint16(x * 65535 / wm),
				G: uint16(y * 65535 / hm),
				B: 0xc000,
				A: 0xffff,
			})
		}
	}
	return img
}

// buildNRGBA64Ramp returns the anti-correlated α/c ramp (the depth-16
// analog of buildNRGBARamp): α = x*65535/max(1,w-1) rises left→right,
// c = 0xffff−α for R/G/B — every pixel is semi-transparent with correlated
// premultiply rounding, the strongest discriminator for the NRGBA64 read
// conversion.
func buildNRGBA64Ramp(w, h int) *image.NRGBA64 {
	img := image.NewNRGBA64(image.Rect(0, 0, w, h))
	wm := max(1, w-1)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			a := uint16(x * 65535 / wm)
			c := 0xffff - a
			img.SetNRGBA64(x, y, color.NRGBA64{R: c, G: c, B: c, A: a})
		}
	}
	return img
}

// buildGray16 returns a deterministic w×h *image.Gray16 with content
// uint16(x*65535/max(1,w-1)) ^ uint16(y*65535/max(1,h-1)) — 0 at (0,0) and
// 0xffff on both axes' last row/column, discriminating the no-0x101 direct
// return (a 0x101 replication would turn y=0xffff into 0xfeff).
func buildGray16(w, h int) *image.Gray16 {
	img := image.NewGray16(image.Rect(0, 0, w, h))
	wm, hm := max(1, w-1), max(1, h-1)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetGray16(x, y, color.Gray16{Y: uint16(x*65535/wm) ^ uint16(y*65535/hm)})
		}
	}
	return img
}

// buildGray16Solid returns a constant-0x8000 *image.Gray16 — an exact
// mid-value pin for the no-0x101 trap at non-zero lerp weights (a 0x101
// replication changes the float64 lerp inputs, and 0x8000's truncated high
// byte is weight-dependent rather than being washed out by constant
// content).
func buildGray16Solid(w, h int) *image.Gray16 {
	img := image.NewGray16(image.Rect(0, 0, w, h))
	for i := 0; i+2 <= len(img.Pix); i += 2 {
		img.Pix[i], img.Pix[i+1] = 0x80, 0x00
	}
	return img
}

// TestFastKernelsDispatchedDepth16 proves the scale/rotate dispatchers take
// the six depth-16 kernels via the alloc-count discriminator (the
// mutation-verified pattern of TestFastKernelsDispatchedGrayPaletted): the
// generic At/Set path boxes once per pixel (≈ 1.6M allocs at 512²), the
// kernels allocate a fresh RGBA plus scaffolding (< 40000). The parity
// battery cannot prove dispatch — fast ≡ generic byte identity holds even
// when a case is dropped (the generic path IS the byte anchor) — so this
// alloc gate is the dispatch proof, alongside the T2 depth-16 arm.
func TestFastKernelsDispatchedDepth16(t *testing.T) {
	if raceEnabled {
		t.Skip("alloc count is race-inflated; TestFastPathByteIdentity pins byte identity under -race")
	}
	ctx := context.Background()
	rgba64 := image.NewRGBA64(image.Rect(0, 0, 512, 512))
	for i := 0; i+8 <= len(rgba64.Pix); i += 8 {
		v := uint8(i >> 3)
		rgba64.Pix[i], rgba64.Pix[i+1] = v, 0
		rgba64.Pix[i+2], rgba64.Pix[i+3] = v, 0
		rgba64.Pix[i+4], rgba64.Pix[i+5] = v, 0
		rgba64.Pix[i+6], rgba64.Pix[i+7] = 0xff, 0xff
	}
	nrgba64 := image.NewNRGBA64(image.Rect(0, 0, 512, 512))
	for i := 0; i+8 <= len(nrgba64.Pix); i += 8 {
		v := uint8(i >> 3)
		nrgba64.Pix[i], nrgba64.Pix[i+1] = v, 0
		nrgba64.Pix[i+2], nrgba64.Pix[i+3] = v, 0
		nrgba64.Pix[i+4], nrgba64.Pix[i+5] = v, 0
		nrgba64.Pix[i+6], nrgba64.Pix[i+7] = 0x80, 0x00
	}
	gray16 := image.NewGray16(image.Rect(0, 0, 512, 512))
	for i := 0; i+2 <= len(gray16.Pix); i += 2 {
		gray16.Pix[i] = uint8(i >> 2)
	}
	cases := []struct {
		name string
		run  func() (image.Image, error)
	}{
		{"scaleRGBA64", func() (image.Image, error) { return scale(ctx, rgba64, 256, 256) }},
		{"scaleNRGBA64", func() (image.Image, error) { return scale(ctx, nrgba64, 256, 256) }},
		{"scaleGray16", func() (image.Image, error) { return scale(ctx, gray16, 256, 256) }},
		{"rotateRGBA64", func() (image.Image, error) { return applyOrientation(ctx, rgba64, 6) }},
		{"rotateNRGBA64", func() (image.Image, error) { return applyOrientation(ctx, nrgba64, 6) }},
		{"rotateGray16", func() (image.Image, error) { return applyOrientation(ctx, gray16, 6) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.run(); err != nil {
				t.Fatalf("run: %v", err)
			}
			n := testing.AllocsPerRun(20, func() {
				out, err := tc.run()
				if err != nil {
					t.Fatalf("run: %v", err)
				}
				if _, ok := out.(*image.RGBA); !ok {
					t.Fatalf("output is %T, want *image.RGBA", out)
				}
			})
			if n >= 40000 {
				t.Fatalf("%s allocated %.0f objects/op, want < 40000 (kernel class ≈ 5–10; generic ≈ 1.6M) — dispatch not taken", tc.name, n)
			}
		})
	}
}

// TestRGBA64ReadMatchesStdlib pins the RGBA64 read helper against
// color.RGBA64.RGBA(): raw, unscaled, no premultiply — including the α=0
// with RGB≠0 straight-color tuple, which must pass through unchanged (the
// divergence the α=0 parity fixture also catches end-to-end).
func TestRGBA64ReadMatchesStdlib(t *testing.T) {
	tuples := [][4]uint16{
		{0, 0, 0, 0},
		{0xffff, 0xffff, 0xffff, 0xffff},
		{0x1020, 0x3040, 0x5060, 0x8000},
		{0x1234, 0x5678, 0x9abc, 0}, // α=0 with RGB≠0: raw straight color passes through
		{0x8000, 0x0001, 0x7fff, 0x0001},
	}
	for _, c := range tuples {
		pix := []byte{
			byte(c[0] >> 8), byte(c[0]), byte(c[1] >> 8), byte(c[1]),
			byte(c[2] >> 8), byte(c[2]), byte(c[3] >> 8), byte(c[3]),
		}
		sr, sg, sb, sa := color.RGBA64{c[0], c[1], c[2], c[3]}.RGBA()
		kr, kg, kb, ka := rgba64RGBA16(pix, 0)
		if sr != kr || sg != kg || sb != kb || sa != ka {
			t.Fatalf("rgba64RGBA16(%04x,%04x,%04x,%04x) = (%d,%d,%d,%d), stdlib RGBA = (%d,%d,%d,%d)",
				c[0], c[1], c[2], c[3], kr, kg, kb, ka, sr, sg, sb, sa)
		}
	}
}

// TestNRGBA64ReadMatchesStdlib pins the NRGBA64 read helper against
// color.NRGBA64.RGBA(): per-channel (v * a) / 0xffff, alpha = raw pair.
// The α=0 tuple must yield RGB 0 — discriminating NRGBA64's premultiply
// from RGBA64's raw read.
func TestNRGBA64ReadMatchesStdlib(t *testing.T) {
	tuples := [][4]uint16{
		{0, 0, 0, 0},
		{0xffff, 0xffff, 0xffff, 0xffff},
		{0x1020, 0x3040, 0x5060, 0x8000},
		{0x1234, 0x5678, 0x9abc, 0}, // α=0: premultiply must zero RGB
		{0x8000, 0x0001, 0x7fff, 0x0001},
		{0xffff, 0xffff, 0xffff, 0x8000},
	}
	for _, c := range tuples {
		pix := []byte{
			byte(c[0] >> 8), byte(c[0]), byte(c[1] >> 8), byte(c[1]),
			byte(c[2] >> 8), byte(c[2]), byte(c[3] >> 8), byte(c[3]),
		}
		sr, sg, sb, sa := color.NRGBA64{c[0], c[1], c[2], c[3]}.RGBA()
		kr, kg, kb, ka := nrgba64RGBA16(pix, 0)
		if sr != kr || sg != kg || sb != kb || sa != ka {
			t.Fatalf("nrgba64RGBA16(%04x,%04x,%04x,%04x) = (%d,%d,%d,%d), stdlib RGBA = (%d,%d,%d,%d)",
				c[0], c[1], c[2], c[3], kr, kg, kb, ka, sr, sg, sb, sa)
		}
	}
}

// TestGray16ReadMatchesStdlib pins the Gray16 read helper against
// color.Gray16.RGBA(): y,y,y,0xffff directly — NO 0x101 replication. The
// y=0xffff tuple is the trap: a v|v<<8 replication would yield 0xfeff.
func TestGray16ReadMatchesStdlib(t *testing.T) {
	values := []uint16{0, 0x8000, 0x0100, 0xffff}
	for _, y := range values {
		pix := []byte{byte(y >> 8), byte(y)}
		sr, sg, sb, sa := color.Gray16{Y: y}.RGBA()
		kr, kg, kb, ka := gray16RGBA16(pix, 0)
		if sr != kr || sg != kg || sb != kb || sa != ka {
			t.Fatalf("gray16RGBA16(%04x) = (%d,%d,%d,%d), stdlib RGBA = (%d,%d,%d,%d)",
				y, kr, kg, kb, ka, sr, sg, sb, sa)
		}
	}
}

// countingCancelCtx wraps a context and fails on the limit-th Err() call.
// The kernels consult Err() exactly once per cancelCheckRows-th row, so a
// fixed limit yields a deterministic cancellation row without timing.
type countingCancelCtx struct {
	context.Context
	calls, limit int
}

func (c *countingCancelCtx) Err() error {
	c.calls++
	if c.calls >= c.limit {
		return context.Canceled
	}
	return nil
}

// TestDepth16KernelsHonorCancel pins FR16-9 deterministically: the
// scaleGray16/rotateGray16 kernels must consult ctx.Err() at the top of
// every cancelCheckRows-th row and return (nil, ctx.Err()) unwrapped. With
// limit=2 on a 128-row output, row 0 passes (call 1) and row 64 — the
// cancelCheckRows-th row — fails (call 2); a limit=3 control never trips
// within 128 rows and completes with a valid RGBA. One scale + one rotate
// suffices: the cadence is copy-identical across all six kernels.
func TestDepth16KernelsHonorCancel(t *testing.T) {
	src := buildGray16(128, 128)
	for _, tc := range []struct {
		name string
		run  func(ctx context.Context) (image.Image, error)
	}{
		{"scaleGray16", func(ctx context.Context) (image.Image, error) {
			return scaleGray16(ctx, src, 128, 128, 128, 128)
		}},
		{"rotateGray16", func(ctx context.Context) (image.Image, error) {
			return rotateGray16(ctx, src, 6, 128, 128, 128, 128)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cc := &countingCancelCtx{Context: context.Background(), limit: 2}
			out, err := tc.run(cc)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v, want context.Canceled", err)
			}
			if out != nil {
				// The kernels return (nil, cerr) — a typed-nil *image.RGBA,
				// which wraps into a non-nil image.Image interface; only a
				// non-nil pointer (or a different concrete type) is a real
				// output.
				if r, ok := out.(*image.RGBA); !ok || r != nil {
					t.Fatalf("output = %T, want nil on cancellation", out)
				}
			}
		})
	}
	// Control: limit=3 never trips within 128 rows (only rows 0 and 64 are
	// checked) — the kernel completes with a valid 128×128 RGBA.
	cc := &countingCancelCtx{Context: context.Background(), limit: 3}
	out, err := scaleGray16(cc, src, 128, 128, 128, 128)
	if err != nil {
		t.Fatalf("control: err = %v", err)
	}
	if out == nil || out.Bounds().Dx() != 128 || out.Bounds().Dy() != 128 {
		t.Fatalf("control: output %v, want 128×128 *image.RGBA", out.Bounds())
	}
}

// TestClampTable is a cheap hygiene pin on the clamp helper the new kernels
// call at 4×/pixel volume (below-lo / above-hi / in-range).
func TestClampTable(t *testing.T) {
	for _, tc := range []struct {
		name      string
		v, lo, hi int
		want      int
	}{
		{"below-lo", 0, 5, 10, 5},
		{"above-hi", 20, 5, 10, 10},
		{"in-range", 7, 5, 10, 7},
	} {
		if got := clamp(tc.v, tc.lo, tc.hi); got != tc.want {
			t.Fatalf("clamp(%d, %d, %d) = %d, want %d", tc.v, tc.lo, tc.hi, got, tc.want)
		}
	}
}
