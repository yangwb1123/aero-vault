package thumbnail

// Allocation-pin for the scale→composite ordering at real source scale
// (V8 at MaxSourceDim; V16 at Max16BitSourceDim — the depth-16 cap, the
// legal maximum for the 8 B/px decode class), and a byte-level ordering pin
// that also discriminates under -race.
//
// Measured plain-mode TotalAlloc stage table (go1.26.5, in-package probes;
// cumulative deltas for one Generate(transparent, HardMax box)):
//
//	stage      | V8 (NRGBA, 4 B/px, 8192²) | V16 (NRGBA64, 8 B/px, 4096²)
//	decode     |           256.1 MiB       |           128.1 MiB
//	scale      |           144.0 MiB       |           208.0 MiB  (transient bilinear churn)
//	composite  |            16.0 MiB       |            16.0 MiB  (post-scale copy)
//	jpeg encode|             0.3 MiB       |             0.3 MiB
//	total      |           416.4 MiB       |           352.4 MiB
//
// The pre-scale composite regression (compositeOnWhite before scale) adds a
// full-frame copy at source resolution and measures ≈ 656.4 MiB (V8) /
// ≈ 336.4 MiB (V16) — totals, decode included. The V16 regression is NOT
// larger than the correct path at the depth-16 cap: the full-frame composite
// copies the 4096² frame to a 4 B/px RGBA (64 MiB) and the subsequent scale
// churns the cheaper 4 B/px class (~144 MiB), netting ≈ 16 MiB below the
// correct order — the cap itself is what defangs the pre-scale regression at
// 8 B/px, so V16 ordering discrimination is carried by the V8 arm (unchanged
// at 8192²) and the byte-level pin. All figures are cumulative TotalAlloc
// deltas; the live-peak contract (what the decode semaphore bounds) is
// different and is documented in thumbnail.go. Under -race the detector's
// bookkeeping inflates the correct-path deltas (measured V8 416.4 →
// 672.6 MiB, V16 352.4 → 544.6 MiB, ≈ 1.62×/1.55×), so the allocation pin
// runs in plain mode only; the feathered byte-level pin keeps the ordering
// discriminated under -race.
import (
	"bytes"
	"context"
	"image"
	"image/color"
	"runtime"
	"testing"
)

const (
	// scaleStageAllocBudget covers the transient bilinear per-pixel
	// interface churn (measured ≤ 208 MiB for NRGBA64 sources) plus 16 MiB
	// headroom for the scaled destination buffer itself.
	scaleStageAllocBudget = 224 << 20
	// compositeCopyBudget is the post-scale copy: the composite input is the
	// scaled buffer, at most HardMax² × 4 B = 16 MiB.
	compositeCopyBudget = HardMax * HardMax * 4
	// encodeAncillaryBudget covers the JPEG encoder's fixed allocation
	// (measured 0.3 MiB).
	encodeAncillaryBudget = 4 << 20
	// allocSlack absorbs stdlib ancillary growth within the same Go version
	// line; a future version that breaks the bound fails loudly (assertion),
	// never silently.
	allocSlack = 64 << 20
)

// transparentHalfNRGBA builds an MaxSourceDim² 8-bit PNG whose left half is
// fully transparent and whose right half is opaque: the downscaled buffer
// samples both halves, so the scaled image is genuinely non-opaque (a single
// transparent pixel would be skipped by the bilinear grid and the composite
// would be vacuous).
func transparentHalfNRGBA(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, MaxSourceDim, MaxSourceDim))
	for y := 0; y < MaxSourceDim; y++ {
		for x := 0; x < MaxSourceDim; x++ {
			if x < MaxSourceDim/2 {
				img.SetNRGBA(x, y, color.NRGBA{255, 0, 0, 0})
			} else {
				img.SetNRGBA(x, y, color.NRGBA{255, 0, 0, 255})
			}
		}
	}
	return pngEncodeBytes(t, img)
}

// transparentHalfNRGBA64 is the depth-16 analogue, built at Max16BitSourceDim
// (the legal maximum for the 8 B/px decode class since the depth-16 cap): the
// stdlib decodes it to *image.NRGBA64 at 8 B/px.
func transparentHalfNRGBA64(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA64(image.Rect(0, 0, Max16BitSourceDim, Max16BitSourceDim))
	for y := 0; y < Max16BitSourceDim; y++ {
		for x := 0; x < Max16BitSourceDim; x++ {
			if x < Max16BitSourceDim/2 {
				img.SetNRGBA64(x, y, color.NRGBA64{0xffff, 0, 0, 0})
			} else {
				img.SetNRGBA64(x, y, color.NRGBA64{0xffff, 0, 0, 0xffff})
			}
		}
	}
	return pngEncodeBytes(t, img)
}

// TestGenerateLargeTransparentAllocationBounded pins the scale→composite
// ordering at real scale: the white composite must run on the scaled buffer
// (≤ HardMax²), never on the full-resolution frame — a pre-scale composite
// copies the full decode (256/512 MiB) plus scale churn and measures
// ≈ 656/912 MiB per request, well above both bounds. Skips come FIRST
// (before any fixture construction): -short for CI race runners, and the
// race build for make test-race determinism.
func TestGenerateLargeTransparentAllocationBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("8192² transparent fixtures need ~1.5 GiB transient heap; run without -short")
	}
	if raceEnabled {
		t.Skip("TotalAlloc-delta is race-inflated (measured V8 416.4→672.6 MiB, V16 352.4→544.6 MiB); the feathered byte-level pin keeps ordering discriminated under -race")
	}
	cases := []struct {
		name        string
		fixture     func(t *testing.T) []byte
		decodeBytes uint64
		typ         string // expected decoded concrete type
		wantRect    image.Rectangle
	}{
		{name: "V8", fixture: transparentHalfNRGBA, decodeBytes: uint64(MaxSourceDim) * MaxSourceDim * 4, typ: "*image.NRGBA", wantRect: image.Rect(0, 0, MaxSourceDim, MaxSourceDim)},
		{name: "V16", fixture: transparentHalfNRGBA64, decodeBytes: uint64(Max16BitSourceDim) * Max16BitSourceDim * 8, typ: "*image.NRGBA64", wantRect: image.Rect(0, 0, Max16BitSourceDim, Max16BitSourceDim)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := tc.fixture(t)

			// Engagement guards: the fixture must decode to the exact
			// cap-sized frame of the expected bit depth, be genuinely
			// transparent, and — load-bearing — stay non-opaque AFTER the
			// downscale, because the composite's input is the scaled buffer.
			decoded, format, err := image.Decode(bytes.NewReader(payload))
			if err != nil || format != "png" {
				t.Fatalf("decode fixture: %v fmt=%q", err, format)
			}
			if b := decoded.Bounds(); b != tc.wantRect {
				t.Fatalf("decoded bounds %v, want %v — fixture shrink makes the pin vacuous", b, tc.wantRect)
			}
			if got := imageType(decoded); got != tc.typ {
				t.Fatalf("decoded type %s, want %s", got, tc.typ)
			}
			if o, ok := decoded.(interface{ Opaque() bool }); !ok || o.Opaque() {
				t.Fatal("decoded fixture is opaque — composite is a no-op")
			}
			scaled, err := scale(context.Background(), decoded, HardMax, HardMax)
			if err != nil {
				t.Fatalf("scale: %v", err)
			}
			if o, ok := scaled.(interface{ Opaque() bool }); !ok || o.Opaque() {
				t.Fatal("scaled fixture is opaque — the composite would be vacuous (transparent region lost in downscale)")
			}

			var m0, m1 runtime.MemStats
			runtime.ReadMemStats(&m0)
			out, err := Generate(bytes.NewReader(payload), HardMax, HardMax)
			runtime.ReadMemStats(&m1)
			if err != nil {
				t.Fatalf("generate: %v", err)
			}
			delta := m1.TotalAlloc - m0.TotalAlloc

			limit := tc.decodeBytes + scaleStageAllocBudget + uint64(compositeCopyBudget) + encodeAncillaryBudget + allocSlack
			if delta > limit {
				t.Fatalf("allocated %d bytes, want ≤ %d (decode %d + budgets) — composite likely ran pre-scale (regression ≈ 656/912 MiB)", delta, limit, tc.decodeBytes)
			}
			// Lower-bound floor: the decode alone allocates decodeBytes, and
			// the scale+composite stages add measurable churn — a shrunken
			// fixture (or a silently bypassed decode) must fail this floor
			// even if it sneaks under the ceiling.
			floor := tc.decodeBytes + 96<<20
			if delta < floor {
				t.Fatalf("allocated %d bytes, want ≥ %d (decode + 96 MiB) — fixture or decode bypassed, pin is stale", delta, floor)
			}

			// Output assertions: a JPEG no larger than HardMax².
			img, format, err := image.Decode(bytes.NewReader(out))
			if err != nil || format != "jpeg" {
				t.Fatalf("decode output: %v fmt=%q", err, format)
			}
			if img.Bounds().Dx() > HardMax || img.Bounds().Dy() > HardMax {
				t.Fatalf("output %dx%d exceeds HardMax %d", img.Bounds().Dx(), img.Bounds().Dy(), HardMax)
			}
		})
	}
}

// imageType returns the concrete type of an image as a string (for the
// engagement-guard assertion; *image.NRGBA64 must hold for depth-16 PNGs so
// the 8 B/px accounting is real, not a silent downconversion).
func imageType(img image.Image) string {
	switch img.(type) {
	case *image.NRGBA:
		return "*image.NRGBA"
	case *image.NRGBA64:
		return "*image.NRGBA64"
	case *image.RGBA:
		return "*image.RGBA"
	case *image.YCbCr:
		return "*image.YCbCr"
	default:
		return "other"
	}
}

// TestCompositeOrderingFeatheredByteLevel pins the ordering at the byte
// level with anti-correlated α/c ramps — and runs under -race, where the
// allocation pin must skip. Composite-onto-white is concave in α, so
// averaging-then-compositing (production order: scale → compositeOnWhite)
// yields strictly brighter pixels than compositing-then-averaging (the
// pre-scale regression). A reversal of the two calls inverts the
// brighter/darker ledger and fails this test.
func TestCompositeOrderingFeatheredByteLevel(t *testing.T) {
	// 64×64 anti-correlated ramps: α rises left→right, color falls (c = 255−α).
	src := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			a := uint8(x * 4) // 0..252
			src.SetNRGBA(x, y, color.NRGBA{R: 255 - a, G: 255 - a, B: 255 - a, A: a})
		}
	}
	scaled, err := scale(context.Background(), src, 16, 16)
	if err != nil {
		t.Fatalf("scale: %v", err)
	}
	if o, ok := scaled.(interface{ Opaque() bool }); !ok || o.Opaque() {
		t.Fatal("scaled fixture is opaque — composite is a no-op, pin vacuous")
	}
	post := compositeOnWhite(scaled)                                       // production order
	pre, err := scale(context.Background(), compositeOnWhite(src), 16, 16) // regression order
	if err != nil {
		t.Fatalf("scale: %v", err)
	}

	var brighter, darker int
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			pr, _, _, _ := pre.At(x, y).RGBA()
			qr, _, _, _ := post.At(x, y).RGBA()
			switch {
			case qr > pr:
				brighter++
			case qr < pr:
				darker++
			}
		}
	}
	if darker == 0 {
		t.Fatal("post-order not strictly darker anywhere — anti-correlated ramps make the pin vacuous")
	}
	if brighter != 0 {
		t.Fatalf("post-order brighter at %d pixels (want 0: premultiplied post-order composite is darker wherever α varies)", brighter)
	}
}
