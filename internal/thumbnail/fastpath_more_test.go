package thumbnail

// Deterministic builders and the dispatcher-taken assertion for the
// YCbCr/Gray/Paletted direct-Pix kernels (pixfast_more.go). The parity
// battery itself (TestFastPathByteIdentity) lives in fastpath_test.go; its
// fixtures map and SubImage arms reference the builders defined here. All
// builders are deterministic (suite discipline: no randomness) and their
// content is chosen to discriminate the kernels' conversion branches:
// YCbCr ramp mode hits 0 and 255 in every channel (negative/overflow clamp
// paths), extreme mode blocks through tuples covering all three branch
// classes per channel; the paletted NRGBA mode discriminates the
// premultiply branch and the gray-entries mode pins the default RGBA()
// fallback.
import (
	"context"
	"image"
	"image/color"
	"testing"
)

const (
	ycbcrModeRamp = iota
	ycbcrModeExtreme
)

const (
	palModeOpaque = iota
	palModeGradient
	palModeNRGBA
	palModeGray
)

// buildYCbCr returns a deterministic w×h *image.YCbCr at the given
// subsample ratio. Ramp mode: Y = (x*3+y*5)%256, Cb = (x*7)%256, Cr =
// (y*11)%256 — 0 and 255 both occur in every channel (gcd(7,256) =
// gcd(11,256) = 1), so the negative and overflow clamp branches of
// ycbcrRGBA16 are exercised. Extreme mode: 16×16 blocks cycling the
// clamp-discriminating tuples {0,0,0} (r,b negative clamp, g in-range
// 0x8776), {255,255,255} and {255,255,0} (overflow → 0xffff), {100,128,128}
// (all in-range → 0x6464), {128,128,128} (top byte 0x80), and the mixed
// tuples {255,0,0}, {0,255,0}, {0,0,255}. Chroma planes are written via
// the stdlib COffset, so subsampled blocks take the same mapping the
// kernels read.
func buildYCbCr(w, h int, ratio image.YCbCrSubsampleRatio, mode int) *image.YCbCr {
	img := image.NewYCbCr(image.Rect(0, 0, w, h), ratio)
	extreme := [...][3]uint8{
		{0, 0, 0},
		{255, 255, 255},
		{100, 128, 128},
		{255, 0, 0},
		{0, 255, 0},
		{0, 0, 255},
		{255, 255, 0},
		{128, 128, 128},
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var yv, cb, cr uint8
			switch mode {
			case ycbcrModeRamp:
				yv = uint8((x*3 + y*5) % 256)
				cb = uint8((x * 7) % 256)
				cr = uint8((y * 11) % 256)
			case ycbcrModeExtreme:
				t := extreme[(x/16+y/16)%len(extreme)]
				yv, cb, cr = t[0], t[1], t[2]
			default:
				panic("buildYCbCr: unknown mode")
			}
			img.Y[img.YOffset(x, y)] = yv
			ci := img.COffset(x, y)
			img.Cb[ci] = cb
			img.Cr[ci] = cr
		}
	}
	return img
}

// buildGray returns a deterministic w×h *image.Gray with ramp content
// uint8(x^y) — includes 0 and 255, discriminating the 0x101 replication.
func buildGray(w, h int) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Pix[y*img.Stride+x] = uint8(x ^ y)
		}
	}
	return img
}

// buildGraySolid returns a deterministic constant-0x80 *image.Gray.
func buildGraySolid(w, h int) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Pix[y*img.Stride+x] = 0x80
		}
	}
	return img
}

// buildPaletted returns a deterministic w×h *image.Paletted in one of four
// palette modes. Opaque: 4 color.RGBA entries, indices cycling (x+y)%4.
// Gradient: the full 256-entry color.RGBA{i, 255-i, i/2, 255} ramp,
// indices (x+y)%256. NRGBA: semi-transparent color.NRGBA entries including
// partial α and α=0 — never produced by GIF decode, hand-built only,
// discriminating the premultiply branch. Gray: color.Gray entries, pinning
// palettedRGBA16's default entry.RGBA() fallback.
func buildPaletted(w, h, mode int) *image.Paletted {
	var pal color.Palette
	idx := func(x, y int) uint8 { return uint8((x + y) % 256) }
	switch mode {
	case palModeOpaque:
		pal = color.Palette{
			color.RGBA{255, 0, 0, 255},
			color.RGBA{0, 255, 0, 255},
			color.RGBA{0, 0, 255, 255},
			color.RGBA{255, 255, 0, 255},
		}
		idx = func(x, y int) uint8 { return uint8((x + y) % 4) }
	case palModeGradient:
		pal = make(color.Palette, 256)
		for i := range pal {
			pal[i] = color.RGBA{uint8(i), uint8(255 - i), uint8(i / 2), 255}
		}
	case palModeNRGBA:
		pal = color.Palette{
			color.NRGBA{255, 0, 0, 128},
			color.NRGBA{0, 255, 0, 64},
			color.NRGBA{0, 0, 255, 192},
			color.NRGBA{255, 255, 255, 0},
		}
		idx = func(x, y int) uint8 { return uint8((x + y) % 4) }
	case palModeGray:
		pal = color.Palette{
			color.Gray{0},
			color.Gray{64},
			color.Gray{128},
			color.Gray{255},
		}
		idx = func(x, y int) uint8 { return uint8((x + y) % 4) }
	default:
		panic("buildPaletted: unknown mode")
	}
	img := image.NewPaletted(image.Rect(0, 0, w, h), pal)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetColorIndex(x, y, idx(x, y))
		}
	}
	return img
}

// TestApplyOrientationDispatchesFast proves applyOrientation takes the
// rotateYCbCr kernel rather than the generic fallback for a *image.YCbCr
// source, mirroring T2's proof-by-allocs pattern (the generic path boxes
// once per pixel: ≈ 2M allocs for 1024²; the kernel is a fresh RGBA plus
// scaffolding ≈ 5–10). It skips under -race per the package's
// allocation-delta convention; the rotateParity arms of
// TestFastPathByteIdentity pin byte identity there.
func TestApplyOrientationDispatchesFast(t *testing.T) {
	if raceEnabled {
		t.Skip("alloc count is race-inflated; the rotateParity arms of T1 pin byte identity under -race")
	}
	ctx := context.Background()
	src := buildYCbCr(1024, 1024, image.YCbCrSubsampleRatio420, ycbcrModeRamp)
	n := testing.AllocsPerRun(20, func() {
		out, err := applyOrientation(ctx, src, 6)
		if err != nil {
			t.Fatalf("applyOrientation(6): %v", err)
		}
		if _, ok := out.(*image.RGBA); !ok {
			t.Fatalf("output is %T, want *image.RGBA", out)
		}
	})
	if n >= 40000 {
		t.Fatalf("applyOrientation on a 1024² YCbCr allocated %.0f objects/op, want < 40000 (kernel class ≈ 5–10; generic ≈ 2M)", n)
	}
}
