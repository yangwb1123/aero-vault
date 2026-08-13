package thumbnail

// Direct-Pix fast kernels for *image.YCbCr, *image.Gray and *image.Paletted
// sources — the sibling of pixfast.go's RGBA/NRGBA kernels, closing the
// dominant production gap: every JPEG thumbnail decodes to *image.YCbCr and
// would otherwise traverse scaleGeneric at ≈ 6 allocs/pixel (4 At() boxes +
// Set box + rgbamodel re-box); transparent GIFs (*image.Paletted) pay ≈ 2
// allocs/pixel; grayscale PNGs (*image.Gray) likewise miss the dispatcher.
// The kernels read src.Pix directly with zero per-pixel allocation; the
// dispatch switches in thumbnail.go (scale) and rotate.go (applyOrientation)
// gain cases ahead of the generic references, which stay verbatim — the byte
// anchor of the suite.
//
// Byte-identity contract (FR-3): fast ≡ generic on all valid inputs, the
// same load-bearing contract as pixfast.go. The YCbCr kernels replicate the
// installed-stdlib color.YCbCr.RGBA() integer sequence instruction-for-
// instruction in one helper (ycbcrRGBA16) — conversion-then-lerp, never
// lerp-then-convert: the clamp makes the map piecewise-linear, so only the
// generic path's order (convert each of the 4 samples, then lerp the 16-bit
// values) is correct. The Paletted kernels replicate each palette entry's
// RGBA() semantics via a concrete type switch (palettedRGBA16) with
// entry.RGBA() as the covering fallback, so byte identity holds for any
// palette, not just the color.RGBA entries image/gif produces.
//
// Rect/Stride correctness (FR-4): the Y/Gray/Paletted planes read via
// y0*Stride + x0 — Min cancels in YOffset/PixOffset exactly as documented
// in pixfast.go's header — while the subsampled Cb/Cr planes read via the
// stdlib method src.COffset(b.Min.X+x, b.Min.Y+y): the 420/422/440/411/410
// forms use (y/2 - Rect.Min.Y/2) / (x/2 - Rect.Min.X/2), which do NOT
// cancel Min, so the offset is delegated to the exact function
// image.YCbCrAt uses rather than re-derived. Correct for hand-built images
// with any Rect.Min and padded Stride, and for SubImage re-slices.
//
// Cancellation (FR-5): every kernel consults ctx.Err() at the top of each
// cancelCheckRows-th row and returns (nil, ctx.Err()) unwrapped — the same
// cadence as the generic loops and the RGBA/NRGBA kernels. The cadence is
// load-bearing for the unit-level rotation pin
// (TestApplyOrientationHonorsCancelMidRotate) and keeps the generate-level
// pin (TestGenerateContextCancelMidRotationReleasesSlot drives a JPEG
// source → *image.YCbCr → rotateYCbCr) green — post-kernels the rotation
// is ~10–25 ms at 2048², so that pin's abort is phase-agnostic (rotation or
// in-encode ctxWriter).
//
// Degenerate inputs: an empty-Palette *image.Paletted panics in both paths
// (the generic path's nil.RGBA() and the kernel's slice index are both
// invalid-input panics); not a goal to fix.
import (
	"context"
	"image"
	"image/color"
)

// ycbcrRGBA16 converts one Y'CbCr sample to 16-bit RGB, replicating the
// installed-stdlib color.YCbCr.RGBA() integer sequence exactly (see
// image/color/ycbcr.go in the Go distribution). Two transcription traps:
// the in-range branch shifts right by 8 (r >>= 8 — NOT r >> 16: the input
// is already scaled by 0x10101 so the 16-bit range is reached in one
// shift), and the clamp branch is ^(r >> 31) & 0xffff — the & 0xffff is
// load-bearing (without it the overflow branch would return 0xffffffff).
// Alpha is always 0xffff. Single replication point for both scaleYCbCr and
// rotateYCbCr, pinned by TestFastPathByteIdentity's clamp-discriminating
// fixtures; if a future stdlib changes RGBA(), the parity battery fails
// loudly and this helper is the one-line sync point.
func ycbcrRGBA16(y, cb, cr uint8) (r, g, b uint16) {
	yy1 := int32(y) * 0x10101
	cb1 := int32(cb) - 128
	cr1 := int32(cr) - 128

	rr := yy1 + 91881*cr1
	if uint32(rr)&0xff000000 == 0 {
		r = uint16(rr >> 8)
	} else {
		r = uint16(^(rr >> 31) & 0xffff)
	}

	gg := yy1 - 22554*cb1 - 46802*cr1
	if uint32(gg)&0xff000000 == 0 {
		g = uint16(gg >> 8)
	} else {
		g = uint16(^(gg >> 31) & 0xffff)
	}

	bb := yy1 + 116130*cb1
	if uint32(bb)&0xff000000 == 0 {
		b = uint16(bb >> 8)
	} else {
		b = uint16(^(bb >> 31) & 0xffff)
	}
	return
}

// palettedRGBA16 converts one palette entry to 16-bit RGBA, replicating the
// generic path's At().RGBA() semantics per concrete type. The fast branches
// cover the two types image/gif actually produces (color.RGBA, plus
// hand-built color.NRGBA fixtures — GIF decode never emits NRGBA entries,
// but the parity battery discriminates the premultiply branch); the default
// branch delegates to the entry's own RGBA() — an allocation-free interface
// call (value receivers) that covers Gray, Gray16, RGBA64, NRGBA64, YCbCr,
// Alpha, Alpha16 and custom palette entries, making byte identity hold for
// any palette. NRGBA premultiply intermediates peak at 65535×255 =
// 16,711,425 < 2³², so no overflow.
func palettedRGBA16(entry color.Color) (r, g, b, a uint16) {
	switch c := entry.(type) {
	case color.RGBA:
		r = uint16(c.R) | uint16(c.R)<<8
		g = uint16(c.G) | uint16(c.G)<<8
		b = uint16(c.B) | uint16(c.B)<<8
		a = uint16(c.A) | uint16(c.A)<<8
	case color.NRGBA:
		r = uint16((uint32(c.R) | uint32(c.R)<<8) * uint32(c.A) / 0xff)
		g = uint16((uint32(c.G) | uint32(c.G)<<8) * uint32(c.A) / 0xff)
		b = uint16((uint32(c.B) | uint32(c.B)<<8) * uint32(c.A) / 0xff)
		a = uint16(c.A) | uint16(c.A)<<8
	default:
		rr, gg, bb, aa := entry.RGBA()
		return uint16(rr), uint16(gg), uint16(bb), uint16(aa)
	}
	return
}

// scaleYCbCr downsamples an *image.YCbCr source to tw×th via direct-Pix
// bilinear interpolation, mirroring scaleRGBA's structure: same fx/fy
// sampling coordinates, clamp to [0, sw-1]×[0, sh-1], lerpQuad per channel,
// rgbamodel truncation writes, alpha 0xff (YCbCr A=0xffff always). Luma
// reads use y0*YStride + x0 (Min cancels in YOffset); chroma reads use
// src.COffset(b.Min.X+x, b.Min.Y+y) — the exact function YCbCrAt uses —
// computed once per sample for both Cb and Cr.
func scaleYCbCr(ctx context.Context, src *image.YCbCr, sw, sh, tw, th int) (*image.RGBA, error) {
	minX, minY := src.Rect.Min.X, src.Rect.Min.Y
	stride := src.YStride
	pix := src.Y
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
			i00, i10 := row00+x0, row00+x1
			i01, i11 := row01+x0, row01+x1
			ci00 := src.COffset(minX+x0, minY+y0)
			ci10 := src.COffset(minX+x1, minY+y0)
			ci01 := src.COffset(minX+x0, minY+y1)
			ci11 := src.COffset(minX+x1, minY+y1)
			r00, g00, b00 := ycbcrRGBA16(pix[i00], src.Cb[ci00], src.Cr[ci00])
			r10, g10, b10 := ycbcrRGBA16(pix[i10], src.Cb[ci10], src.Cr[ci10])
			r01, g01, b01 := ycbcrRGBA16(pix[i01], src.Cb[ci01], src.Cr[ci01])
			r11, g11, b11 := ycbcrRGBA16(pix[i11], src.Cb[ci11], src.Cr[ci11])
			di := dr + x*4
			dpix[di] = uint8(lerpQuad(uint32(r00), uint32(r10), uint32(r01), uint32(r11), dx, dy) >> 8)
			dpix[di+1] = uint8(lerpQuad(uint32(g00), uint32(g10), uint32(g01), uint32(g11), dx, dy) >> 8)
			dpix[di+2] = uint8(lerpQuad(uint32(b00), uint32(b10), uint32(b01), uint32(b11), dx, dy) >> 8)
			dpix[di+3] = 0xff
		}
	}
	return dst, nil
}

// scaleGray downsamples an *image.Gray source: reads use the 0x101
// replication of color.Gray.RGBA() (v := uint32(pix[i]); v |= v << 8). R=G=B
// for every sample, so one lerpQuad computation is byte-identical to the
// generic path's three; alpha is 0xff.
func scaleGray(ctx context.Context, src *image.Gray, sw, sh, tw, th int) (*image.RGBA, error) {
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
			i00, i10 := row00+x0, row00+x1
			i01, i11 := row01+x0, row01+x1
			v00 := uint32(pix[i00]) | uint32(pix[i00])<<8
			v10 := uint32(pix[i10]) | uint32(pix[i10])<<8
			v01 := uint32(pix[i01]) | uint32(pix[i01])<<8
			v11 := uint32(pix[i11]) | uint32(pix[i11])<<8
			lerped := uint8(lerpQuad(v00, v10, v01, v11, dx, dy) >> 8)
			di := dr + x*4
			dpix[di] = lerped
			dpix[di+1] = lerped
			dpix[di+2] = lerped
			dpix[di+3] = 0xff
		}
	}
	return dst, nil
}

// scalePaletted downsamples an *image.Paletted source: the pixel bytes are
// palette indices (PixOffset cancels Min), resolved via palettedRGBA16.
// Zero per-pixel allocation — indexing the interface slice copies the
// interface value without boxing, the exact saving over the generic path's
// Set box + rgbamodel re-box (≈ 2 allocs/pixel).
func scalePaletted(ctx context.Context, src *image.Paletted, sw, sh, tw, th int) (*image.RGBA, error) {
	stride := src.Stride
	pix := src.Pix
	pal := src.Palette
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
			i00, i10 := row00+x0, row00+x1
			i01, i11 := row01+x0, row01+x1
			r00, g00, b00, a00 := palettedRGBA16(pal[pix[i00]])
			r10, g10, b10, a10 := palettedRGBA16(pal[pix[i10]])
			r01, g01, b01, a01 := palettedRGBA16(pal[pix[i01]])
			r11, g11, b11, a11 := palettedRGBA16(pal[pix[i11]])
			di := dr + x*4
			dpix[di] = uint8(lerpQuad(uint32(r00), uint32(r10), uint32(r01), uint32(r11), dx, dy) >> 8)
			dpix[di+1] = uint8(lerpQuad(uint32(g00), uint32(g10), uint32(g01), uint32(g11), dx, dy) >> 8)
			dpix[di+2] = uint8(lerpQuad(uint32(b00), uint32(b10), uint32(b01), uint32(b11), dx, dy) >> 8)
			dpix[di+3] = uint8(lerpQuad(uint32(a00), uint32(a10), uint32(a01), uint32(a11), dx, dy) >> 8)
		}
	}
	return dst, nil
}

// rotateYCbCr applies the EXIF orientation table to an *image.YCbCr source:
// per pixel it converts the single mapped sample via ycbcrRGBA16 and
// truncate-writes; alpha 0xff. Source indices (sr, sc) come from
// orientIndex[o], in-range for every o∈2..8 and w,h ≥ 1.
func rotateYCbCr(ctx context.Context, src *image.YCbCr, o, w, h, outW, outH int) (*image.RGBA, error) {
	idx := orientIndex[o]
	minX, minY := src.Rect.Min.X, src.Rect.Min.Y
	stride := src.YStride
	pix := src.Y
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
			si := sr*stride + sc
			ci := src.COffset(minX+sc, minY+sr)
			rv, gv, bv := ycbcrRGBA16(pix[si], src.Cb[ci], src.Cr[ci])
			di := dr + c*4
			dpix[di] = uint8(rv >> 8)
			dpix[di+1] = uint8(gv >> 8)
			dpix[di+2] = uint8(bv >> 8)
			dpix[di+3] = 0xff
		}
	}
	return dst, nil
}

// rotateGray applies the EXIF orientation table to an *image.Gray source:
// the 0x101-replicated luma is written to all three RGB channels, alpha
// 0xff.
func rotateGray(ctx context.Context, src *image.Gray, o, w, h, outW, outH int) (*image.RGBA, error) {
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
			v := uint32(pix[sr*stride+sc])
			v |= v << 8
			b := uint8(v >> 8)
			di := dr + c*4
			dpix[di] = b
			dpix[di+1] = b
			dpix[di+2] = b
			dpix[di+3] = 0xff
		}
	}
	return dst, nil
}

// rotatePaletted applies the EXIF orientation table to an *image.Paletted
// source: the mapped index resolves through palettedRGBA16 and truncate-
// writes, preserving alpha (transparent entries stay transparent, exactly
// as the generic path's At/Set roundtrip produces).
func rotatePaletted(ctx context.Context, src *image.Paletted, o, w, h, outW, outH int) (*image.RGBA, error) {
	idx := orientIndex[o]
	stride := src.Stride
	pix := src.Pix
	pal := src.Palette
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
			rv, gv, bv, av := palettedRGBA16(pal[pix[sr*stride+sc]])
			di := dr + c*4
			dpix[di] = uint8(rv >> 8)
			dpix[di+1] = uint8(gv >> 8)
			dpix[di+2] = uint8(bv >> 8)
			dpix[di+3] = uint8(av >> 8)
		}
	}
	return dst, nil
}
