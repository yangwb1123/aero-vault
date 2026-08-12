package thumbnail

// Direct-Pix fast paths for scale and applyOrientation: the generic loops
// (scaleGeneric / applyOrientationGeneric) go through the color.Color
// interface per pixel — src.At() boxes a 4-byte color.RGBA (4 allocs/pixel in
// bilinear), dst.Set() boxes the rgba16 result (1 alloc) and rgbamodel
// conversion re-boxes (1 alloc) — ≈ 6 allocs/pixel ≈ 393K allocs for a
// 256×256 downscale. The kernels below read/write src.Pix/dst.Pix directly,
// replicating the exact stdlib conversions (see the read/write notes), with
// zero per-pixel allocation.
//
// Byte-identity contract (FR-3): fast ≡ generic on all valid inputs. Every
// downstream consumer (jpeg encoder, compositeOnWhite Opaque(), REST ETag
// validators, the persistent cache keyed by source ETag) sees identical
// bytes because the kernels replicate, exactly, the installed-stdlib
// conversion semantics — pinned by TestFastPathByteIdentity, which compares
// the dispatchers against the preserved generic references.
//
// Rect/Stride correctness (FR-4): offsets derive from (y-Rect.Min.Y)*Stride +
// (x-Rect.Min.X)*4. The generic loops sample at (b.Min.X+x0, b.Min.Y+y0) /
// (b.Min.X+sc, b.Min.Y+sr); substituting into PixOffset cancels Min exactly
// (Bounds() == Rect), giving y0*Stride + x0*4 / sr*Stride + sc*4 — correct
// for hand-built images with any Rect.Min and padded Stride, and for
// SubImage re-slices (nonzero Min, shared Stride). Reads stay in bounds by
// construction: scale clamps x0/y0 to [0, sw-1]×[0, sh-1] and the
// orientation table yields in-range (sr, sc) for every o∈2..8 and every
// w,h ≥ 1 (empty frames run zero iterations), so no out-of-bounds branch is
// needed.
//
// Cancellation (FR-5): every kernel consults ctx.Err() at the top of each
// cancelCheckRows-th row of the outer loop — the same cadence as the
// generic loops — bounding post-cancel work to ≤ cancelCheckRows ×
// HardMax × 4 ≈ 0.5M pixel ops. The no-op checks (empty/in-bounds scale,
// o ≤ 1 || o ≥ 9) live in the dispatchers and never consult ctx (FR-6).
import (
	"context"
	"image"
)

// lerpQuad is the bilinear channel interpolation, replicated from bilinear's
// inline closure byte-for-byte. It MUST mirror the closure exactly: float64
// arithmetic is non-associative, so any regrouping would change output
// bytes. bilinear itself is deliberately NOT refactored to call this helper
// — the generic path is the absolute byte anchor of the suite, and
// TestFastPathByteIdentity (fast vs verbatim generic) is what makes
// fast ≡ generic imply fast ≡ today's bytes. Keep the two expressions in
// lockstep.
func lerpQuad(v00, v10, v01, v11 uint32, dx, dy float64) uint16 {
	top := float64(v00)*(1-dx) + float64(v10)*dx
	bot := float64(v01)*(1-dx) + float64(v11)*dx
	return uint16(top*(1-dy) + bot*dy)
}

// scaleRGBA downsamples an *image.RGBA source to tw×th via direct-Pix
// bilinear interpolation. Reads use the plain shift of color.RGBA.RGBA()
// (r16 = uint32(p[i]) | uint32(p[i])<<8); writes use the rgbamodel
// truncation (byte = uint8(v >> 8)). Sampling coordinates mirror
// scaleGeneric/bilinear exactly: fx/fy from (c+0.5)*dim/tw - 0.5, clamped
// x0/x1/y0/y1, and the four corner values lerped by lerpQuad.
func scaleRGBA(ctx context.Context, src *image.RGBA, sw, sh, tw, th int) (*image.RGBA, error) {
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
			i00, i10 := row00+x0*4, row00+x1*4
			i01, i11 := row01+x0*4, row01+x1*4
			di := dr + x*4
			dpix[di] = uint8(lerpQuad(uint32(pix[i00])|uint32(pix[i00])<<8, uint32(pix[i10])|uint32(pix[i10])<<8, uint32(pix[i01])|uint32(pix[i01])<<8, uint32(pix[i11])|uint32(pix[i11])<<8, dx, dy) >> 8)
			dpix[di+1] = uint8(lerpQuad(uint32(pix[i00+1])|uint32(pix[i00+1])<<8, uint32(pix[i10+1])|uint32(pix[i10+1])<<8, uint32(pix[i01+1])|uint32(pix[i01+1])<<8, uint32(pix[i11+1])|uint32(pix[i11+1])<<8, dx, dy) >> 8)
			dpix[di+2] = uint8(lerpQuad(uint32(pix[i00+2])|uint32(pix[i00+2])<<8, uint32(pix[i10+2])|uint32(pix[i10+2])<<8, uint32(pix[i01+2])|uint32(pix[i01+2])<<8, uint32(pix[i11+2])|uint32(pix[i11+2])<<8, dx, dy) >> 8)
			dpix[di+3] = uint8(lerpQuad(uint32(pix[i00+3])|uint32(pix[i00+3])<<8, uint32(pix[i10+3])|uint32(pix[i10+3])<<8, uint32(pix[i01+3])|uint32(pix[i01+3])<<8, uint32(pix[i11+3])|uint32(pix[i11+3])<<8, dx, dy) >> 8)
		}
	}
	return dst, nil
}

// scaleNRGBA downsamples an *image.NRGBA source identically, but reads via
// the premultiply-shift of color.NRGBA.RGBA(): per R/G/B channel,
// (uint32(p[i]) | uint32(p[i])<<8) * a / 0xff with a = uint32(p[i+3]);
// alpha is a | a<<8. The max intermediate is 65535×255 = 16,711,425 < 2³²,
// so the multiplication cannot overflow. Writes are rgbamodel truncation,
// as in scaleRGBA.
func scaleNRGBA(ctx context.Context, src *image.NRGBA, sw, sh, tw, th int) (*image.RGBA, error) {
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
			i00, i10 := row00+x0*4, row00+x1*4
			i01, i11 := row01+x0*4, row01+x1*4
			di := dr + x*4
			// Premultiply-read helper inline: (v | v<<8) * a / 0xff.
			r00 := (uint32(pix[i00]) | uint32(pix[i00])<<8) * uint32(pix[i00+3]) / 0xff
			g00 := (uint32(pix[i00+1]) | uint32(pix[i00+1])<<8) * uint32(pix[i00+3]) / 0xff
			b00 := (uint32(pix[i00+2]) | uint32(pix[i00+2])<<8) * uint32(pix[i00+3]) / 0xff
			a00 := uint32(pix[i00+3]) | uint32(pix[i00+3])<<8
			r10 := (uint32(pix[i10]) | uint32(pix[i10])<<8) * uint32(pix[i10+3]) / 0xff
			g10 := (uint32(pix[i10+1]) | uint32(pix[i10+1])<<8) * uint32(pix[i10+3]) / 0xff
			b10 := (uint32(pix[i10+2]) | uint32(pix[i10+2])<<8) * uint32(pix[i10+3]) / 0xff
			a10 := uint32(pix[i10+3]) | uint32(pix[i10+3])<<8
			r01 := (uint32(pix[i01]) | uint32(pix[i01])<<8) * uint32(pix[i01+3]) / 0xff
			g01 := (uint32(pix[i01+1]) | uint32(pix[i01+1])<<8) * uint32(pix[i01+3]) / 0xff
			b01 := (uint32(pix[i01+2]) | uint32(pix[i01+2])<<8) * uint32(pix[i01+3]) / 0xff
			a01 := uint32(pix[i01+3]) | uint32(pix[i01+3])<<8
			r11 := (uint32(pix[i11]) | uint32(pix[i11])<<8) * uint32(pix[i11+3]) / 0xff
			g11 := (uint32(pix[i11+1]) | uint32(pix[i11+1])<<8) * uint32(pix[i11+3]) / 0xff
			b11 := (uint32(pix[i11+2]) | uint32(pix[i11+2])<<8) * uint32(pix[i11+3]) / 0xff
			a11 := uint32(pix[i11+3]) | uint32(pix[i11+3])<<8
			dpix[di] = uint8(lerpQuad(r00, r10, r01, r11, dx, dy) >> 8)
			dpix[di+1] = uint8(lerpQuad(g00, g10, g01, g11, dx, dy) >> 8)
			dpix[di+2] = uint8(lerpQuad(b00, b10, b01, b11, dx, dy) >> 8)
			dpix[di+3] = uint8(lerpQuad(a00, a10, a01, a11, dx, dy) >> 8)
		}
	}
	return dst, nil
}

// rotateRGBA applies the EXIF orientation table to an *image.RGBA source by
// raw byte copy: the generic path's dst.Set receives img.At's color.RGBA,
// which rgbamodel passes through via its c.(color.RGBA) identity branch, so
// the output bytes are the source bytes at the mapped coordinates for any
// alpha. Reads/writes use sr*Stride + sc*4 / r*dstride + c*4 (Min cancels
// per the header argument).
func rotateRGBA(ctx context.Context, src *image.RGBA, o, w, h, outW, outH int) (*image.RGBA, error) {
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
			si := sr*stride + sc*4
			di := dr + c*4
			dpix[di] = pix[si]
			dpix[di+1] = pix[si+1]
			dpix[di+2] = pix[si+2]
			dpix[di+3] = pix[si+3]
		}
	}
	return dst, nil
}

// rotateNRGBA applies the EXIF orientation table to an *image.NRGBA source:
// per pixel it premultiplies per the NRGBA read rule (color.NRGBA.RGBA())
// and truncate-writes (rgbamodel). For opaque pixels this is the identity;
// for semi-transparent pixels it is the same lossy premultiply→truncate
// roundtrip the generic path performs — TestFastPathByteIdentity's
// semi-transparent cases pin it.
func rotateNRGBA(ctx context.Context, src *image.NRGBA, o, w, h, outW, outH int) (*image.RGBA, error) {
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
			si := sr*stride + sc*4
			di := dr + c*4
			a := uint32(pix[si+3])
			dpix[di] = uint8(((uint32(pix[si]) | uint32(pix[si])<<8) * a / 0xff) >> 8)
			dpix[di+1] = uint8(((uint32(pix[si+1]) | uint32(pix[si+1])<<8) * a / 0xff) >> 8)
			dpix[di+2] = uint8(((uint32(pix[si+2]) | uint32(pix[si+2])<<8) * a / 0xff) >> 8)
			dpix[di+3] = uint8((a | a<<8) >> 8)
		}
	}
	return dst, nil
}
