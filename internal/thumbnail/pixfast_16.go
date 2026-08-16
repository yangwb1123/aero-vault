package thumbnail

// Direct-Pix fast kernels for the depth-16 PNG classes — *image.RGBA64,
// *image.NRGBA64 and *image.Gray16 — the sibling of pixfast.go's RGBA/NRGBA
// kernels and pixfast_more.go's YCbCr/Gray/Paletted kernels. image/png
// decodes every depth-16 color type into one of these three concrete types
// (cbTC16 → rgba64/nrgba64, cbGA16/cbTCA16 → nrgba64, cbG16 → gray16/nrgba64),
// so before this file every depth-16 thumbnail fell through the scale /
// applyOrientation switches to scaleGeneric / applyOrientationGeneric at
// ≈ 6 allocs/pixel (pixfast.go's header arithmetic). The kernels read
// src.Pix directly with zero per-pixel allocation.
//
// Byte-identity contract (FR-3): fast ≡ generic on all valid inputs, the
// same load-bearing contract as pixfast.go — every downstream consumer
// (jpeg encoder, compositeOnWhite Opaque(), REST ETag validators, the
// persistent cache keyed by source ETag) sees identical bytes. The parity
// battery (TestFastPathByteIdentity) compares the dispatchers against the
// preserved generic references.
//
// Stdlib conversion semantics the kernels replicate exactly (Go 1.26,
// src/image/color/color.go — the four transcription traps):
//  1. RGBA64 read: color.RGBA64.RGBA() returns the raw uint16 channel
//     values unscaled — no shift, no premultiply, including α=0 with RGB≠0
//     (legal straight color; the kernel must pass RGB through unchanged).
//  2. NRGBA64 read: color.NRGBA64.RGBA() premultiplies per channel,
//     r = uint32(c.R) * uint32(c.A) / 0xffff (same for G/B), a = raw pair.
//     Max intermediate 65535×65535 = 4,294,836,225 < 2³² — no overflow.
//  3. Gray16 read: color.Gray16.RGBA() returns y,y,y,0xffff directly — NO
//     0x101 replication (that is color.Gray's v|v<<8 rule; replicating it
//     here would diverge, e.g. y=0xffff → 0xfeff).
//  4. Write: rgbaModel has no identity branch for the 64-bit types — it
//     truncates via uint8(v >> 8); the generic path passes its own rgba16
//     struct, so the truncate-write applies uniformly to all three classes.
//
// Rect/Stride correctness (FR-4): offsets derive from (y-Rect.Min.Y)*Stride
// + (x-Rect.Min.X)*8 (×2 for Gray16). The generic loops sample at
// (b.Min.X+x0, b.Min.Y+y0) / (b.Min.X+sc, b.Min.Y+sr); substituting into
// PixOffset cancels Min exactly (Bounds() == Rect), giving y0*Stride +
// x0*8 / sr*Stride + sc*8 — correct for hand-built images with any
// Rect.Min and padded Stride, and for SubImage re-slices (all three
// SubImage methods return the concrete type). Reads stay in bounds by
// construction: scale clamps x0/y0 to [0, sw-1]×[0, sh-1] and the
// orientation table yields in-range (sr, sc) for every o∈2..8 and every
// w,h ≥ 1 (empty frames run zero iterations).
//
// Cancellation (FR16-9): every kernel consults ctx.Err() at the top of each
// cancelCheckRows-th row and returns (nil, ctx.Err()) unwrapped — the same
// cadence as the generic loops and the sibling kernels, pinned
// deterministically by TestDepth16KernelsHonorCancel.
//
// The three read helpers below are the single replication points for each
// class's conversions (the depth-16 analog of ycbcrRGBA16): if a future
// stdlib changes RGBA() semantics, the parity battery fails loudly at
// exactly the kernel and the helper is the one-line sync point. They are
// pinned directly by Test{RGBA64,NRGBA64,Gray16}ReadMatchesStdlib.
import (
	"context"
	"image"
)

// rgba64RGBA16 reads one *image.RGBA64 pixel (8 bytes, big-endian pairs)
// at byte offset i and returns the four raw channel values —
// color.RGBA64.RGBA() semantics: unscaled, no premultiply, α=0 with RGB≠0
// passes through. Single replication point for scaleRGBA64/rotateRGBA64.
func rgba64RGBA16(pix []byte, i int) (r, g, b, a uint32) {
	r = uint32(uint16(pix[i])<<8 | uint16(pix[i+1]))
	g = uint32(uint16(pix[i+2])<<8 | uint16(pix[i+3]))
	b = uint32(uint16(pix[i+4])<<8 | uint16(pix[i+5]))
	a = uint32(uint16(pix[i+6])<<8 | uint16(pix[i+7]))
	return
}

// nrgba64RGBA16 reads one *image.NRGBA64 pixel and returns the
// premultiplied channels — color.NRGBA64.RGBA() semantics: per RGB channel
// (uint32(v) * uint32(a)) / 0xffff, alpha = the raw pair. The max
// intermediate is 65535×65535 = 4,294,836,225 < 2³², so the multiplication
// cannot overflow (mirrors scaleNRGBA's note). Single replication point
// for scaleNRGBA64/rotateNRGBA64.
func nrgba64RGBA16(pix []byte, i int) (r, g, b, a uint32) {
	r = uint32(uint16(pix[i])<<8 | uint16(pix[i+1]))
	g = uint32(uint16(pix[i+2])<<8 | uint16(pix[i+3]))
	b = uint32(uint16(pix[i+4])<<8 | uint16(pix[i+5]))
	a = uint32(uint16(pix[i+6])<<8 | uint16(pix[i+7]))
	r = r * a / 0xffff
	g = g * a / 0xffff
	b = b * a / 0xffff
	return
}

// gray16RGBA16 reads one *image.Gray16 pixel (2 bytes, big-endian pair)
// and returns y,y,y,0xffff — color.Gray16.RGBA() semantics: the raw value
// directly, NO 0x101 replication (transcription trap #3 above). Single
// replication point for scaleGray16/rotateGray16.
func gray16RGBA16(pix []byte, i int) (r, g, b, a uint32) {
	y := uint32(uint16(pix[i])<<8 | uint16(pix[i+1]))
	return y, y, y, 0xffff
}

// scaleRGBA64 downsamples an *image.RGBA64 source to tw×th via direct-Pix
// bilinear interpolation, mirroring scaleRGBA's structure exactly: same
// fx/fy sampling coordinates, clamp to [0, sw-1]×[0, sh-1], lerpQuad per
// channel, rgbamodel truncation writes. Reads raw big-endian pairs via
// rgba64RGBA16 (trap #1: no premultiply — α=0 with RGB≠0 passes through);
// offsets y0*Stride + x0*8 per the header's Min-cancellation argument.
func scaleRGBA64(ctx context.Context, src *image.RGBA64, sw, sh, tw, th int) (*image.RGBA, error) {
	stride := src.Stride
	pix := src.Pix
	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	dpix, dstride := dst.Pix, dst.Stride
	for y := 0; y < th; y++ {
		if y%cancelCheckRows == 0 {
			if cerr := ctx.Err(); cerr != nil {
				return nil, cerr
			}
		}
		fy := (float64(y)+0.5)*float64(sh)/float64(th) - 0.5
		y0 := int(fy)
		dy := fy - float64(y0)
		y1 := clamp(y0+1, 0, sh-1)
		y0 = clamp(y0, 0, sh-1)
		row00, row01 := y0*stride, y1*stride
		dr := y * dstride
		for x := 0; x < tw; x++ {
			fx := (float64(x)+0.5)*float64(sw)/float64(tw) - 0.5
			x0 := int(fx)
			dx := fx - float64(x0)
			x1 := clamp(x0+1, 0, sw-1)
			x0 = clamp(x0, 0, sw-1)
			i00, i10 := row00+x0*8, row00+x1*8
			i01, i11 := row01+x0*8, row01+x1*8
			r00, g00, b00, a00 := rgba64RGBA16(pix, i00)
			r10, g10, b10, a10 := rgba64RGBA16(pix, i10)
			r01, g01, b01, a01 := rgba64RGBA16(pix, i01)
			r11, g11, b11, a11 := rgba64RGBA16(pix, i11)
			di := dr + x*4
			dpix[di] = uint8(lerpQuad(r00, r10, r01, r11, dx, dy) >> 8)
			dpix[di+1] = uint8(lerpQuad(g00, g10, g01, g11, dx, dy) >> 8)
			dpix[di+2] = uint8(lerpQuad(b00, b10, b01, b11, dx, dy) >> 8)
			dpix[di+3] = uint8(lerpQuad(a00, a10, a01, a11, dx, dy) >> 8)
		}
	}
	return dst, nil
}

// scaleNRGBA64 downsamples an *image.NRGBA64 source identically, reading
// via nrgba64RGBA16 — the premultiply of color.NRGBA64.RGBA() (trap #2:
// per-channel (v * a) / 0xffff, a = raw pair). All four channels are lerped
// (alpha included, matching the generic path); writes are rgbamodel
// truncation.
func scaleNRGBA64(ctx context.Context, src *image.NRGBA64, sw, sh, tw, th int) (*image.RGBA, error) {
	stride := src.Stride
	pix := src.Pix
	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	dpix, dstride := dst.Pix, dst.Stride
	for y := 0; y < th; y++ {
		if y%cancelCheckRows == 0 {
			if cerr := ctx.Err(); cerr != nil {
				return nil, cerr
			}
		}
		fy := (float64(y)+0.5)*float64(sh)/float64(th) - 0.5
		y0 := int(fy)
		dy := fy - float64(y0)
		y1 := clamp(y0+1, 0, sh-1)
		y0 = clamp(y0, 0, sh-1)
		row00, row01 := y0*stride, y1*stride
		dr := y * dstride
		for x := 0; x < tw; x++ {
			fx := (float64(x)+0.5)*float64(sw)/float64(tw) - 0.5
			x0 := int(fx)
			dx := fx - float64(x0)
			x1 := clamp(x0+1, 0, sw-1)
			x0 = clamp(x0, 0, sw-1)
			i00, i10 := row00+x0*8, row00+x1*8
			i01, i11 := row01+x0*8, row01+x1*8
			r00, g00, b00, a00 := nrgba64RGBA16(pix, i00)
			r10, g10, b10, a10 := nrgba64RGBA16(pix, i10)
			r01, g01, b01, a01 := nrgba64RGBA16(pix, i01)
			r11, g11, b11, a11 := nrgba64RGBA16(pix, i11)
			di := dr + x*4
			dpix[di] = uint8(lerpQuad(r00, r10, r01, r11, dx, dy) >> 8)
			dpix[di+1] = uint8(lerpQuad(g00, g10, g01, g11, dx, dy) >> 8)
			dpix[di+2] = uint8(lerpQuad(b00, b10, b01, b11, dx, dy) >> 8)
			dpix[di+3] = uint8(lerpQuad(a00, a10, a01, a11, dx, dy) >> 8)
		}
	}
	return dst, nil
}

// scaleGray16 downsamples an *image.Gray16 source: reads the 2-byte pair
// via gray16RGBA16 (trap #3: direct y,y,y,0xffff — no 0x101). R=G=B for
// every sample, so one lerpQuad computation is byte-identical to the
// generic path's three; alpha is 0xff (A=0xffff always → truncate).
func scaleGray16(ctx context.Context, src *image.Gray16, sw, sh, tw, th int) (*image.RGBA, error) {
	stride := src.Stride
	pix := src.Pix
	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	dpix, dstride := dst.Pix, dst.Stride
	for y := 0; y < th; y++ {
		if y%cancelCheckRows == 0 {
			if cerr := ctx.Err(); cerr != nil {
				return nil, cerr
			}
		}
		fy := (float64(y)+0.5)*float64(sh)/float64(th) - 0.5
		y0 := int(fy)
		dy := fy - float64(y0)
		y1 := clamp(y0+1, 0, sh-1)
		y0 = clamp(y0, 0, sh-1)
		row00, row01 := y0*stride, y1*stride
		dr := y * dstride
		for x := 0; x < tw; x++ {
			fx := (float64(x)+0.5)*float64(sw)/float64(tw) - 0.5
			x0 := int(fx)
			dx := fx - float64(x0)
			x1 := clamp(x0+1, 0, sw-1)
			x0 = clamp(x0, 0, sw-1)
			i00, i10 := row00+x0*2, row00+x1*2
			i01, i11 := row01+x0*2, row01+x1*2
			y00, _, _, _ := gray16RGBA16(pix, i00)
			y10, _, _, _ := gray16RGBA16(pix, i10)
			y01, _, _, _ := gray16RGBA16(pix, i01)
			y11, _, _, _ := gray16RGBA16(pix, i11)
			lerped := uint8(lerpQuad(y00, y10, y01, y11, dx, dy) >> 8)
			di := dr + x*4
			dpix[di] = lerped
			dpix[di+1] = lerped
			dpix[di+2] = lerped
			dpix[di+3] = 0xff
		}
	}
	return dst, nil
}

// rotateRGBA64 applies the EXIF orientation table to an *image.RGBA64
// source. The generic path's dst.Set receives img.At's color.RGBA64, which
// rgbamodel converts via the raw values and uint8(v >> 8) truncation — the
// output byte is the source pair's high byte, so the kernel writes the
// mapped pair's high bytes directly (mirrors rotateRGBA's raw-copy
// argument at 8 B/px). Source indices (sr, sc) come from orientIndex[o],
// in-range for every o∈2..8 and w,h ≥ 1.
func rotateRGBA64(ctx context.Context, src *image.RGBA64, o, w, h, outW, outH int) (*image.RGBA, error) {
	idx := orientIndex[o]
	stride := src.Stride
	pix := src.Pix
	dst := image.NewRGBA(image.Rect(0, 0, outW, outH))
	dpix, dstride := dst.Pix, dst.Stride
	for r := 0; r < outH; r++ {
		if r%cancelCheckRows == 0 {
			if cerr := ctx.Err(); cerr != nil {
				return nil, cerr
			}
		}
		dr := r * dstride
		for c := 0; c < outW; c++ {
			sr, sc := idx(r, c, w, h)
			si := sr*stride + sc*8
			di := dr + c*4
			dpix[di] = pix[si]
			dpix[di+1] = pix[si+2]
			dpix[di+2] = pix[si+4]
			dpix[di+3] = pix[si+6]
		}
	}
	return dst, nil
}

// rotateNRGBA64 applies the EXIF orientation table to an *image.NRGBA64
// source: per pixel it premultiplies per the NRGBA64 read rule
// (nrgba64RGBA16) and truncate-writes — the same lossy
// premultiply→truncate roundtrip the generic path performs; the parity
// battery's semi-transparent fixtures pin it.
func rotateNRGBA64(ctx context.Context, src *image.NRGBA64, o, w, h, outW, outH int) (*image.RGBA, error) {
	idx := orientIndex[o]
	stride := src.Stride
	pix := src.Pix
	dst := image.NewRGBA(image.Rect(0, 0, outW, outH))
	dpix, dstride := dst.Pix, dst.Stride
	for r := 0; r < outH; r++ {
		if r%cancelCheckRows == 0 {
			if cerr := ctx.Err(); cerr != nil {
				return nil, cerr
			}
		}
		dr := r * dstride
		for c := 0; c < outW; c++ {
			sr, sc := idx(r, c, w, h)
			rv, gv, bv, av := nrgba64RGBA16(pix, sr*stride+sc*8)
			di := dr + c*4
			dpix[di] = uint8(rv >> 8)
			dpix[di+1] = uint8(gv >> 8)
			dpix[di+2] = uint8(bv >> 8)
			dpix[di+3] = uint8(av >> 8)
		}
	}
	return dst, nil
}

// rotateGray16 applies the EXIF orientation table to an *image.Gray16
// source: the direct pair value (gray16RGBA16 — no 0x101) is truncated to
// its high byte and written to all three RGB channels, alpha 0xff.
func rotateGray16(ctx context.Context, src *image.Gray16, o, w, h, outW, outH int) (*image.RGBA, error) {
	idx := orientIndex[o]
	stride := src.Stride
	pix := src.Pix
	dst := image.NewRGBA(image.Rect(0, 0, outW, outH))
	dpix, dstride := dst.Pix, dst.Stride
	for r := 0; r < outH; r++ {
		if r%cancelCheckRows == 0 {
			if cerr := ctx.Err(); cerr != nil {
				return nil, cerr
			}
		}
		dr := r * dstride
		for c := 0; c < outW; c++ {
			sr, sc := idx(r, c, w, h)
			yv, _, _, _ := gray16RGBA16(pix, sr*stride+sc*2)
			b := uint8(yv >> 8)
			di := dr + c*4
			dpix[di] = b
			dpix[di+1] = b
			dpix[di+2] = b
			dpix[di+3] = 0xff
		}
	}
	return dst, nil
}
