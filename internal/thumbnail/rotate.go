package thumbnail

import (
	"context"
	"image"
)

// orientIndex maps EXIF orientation values 1–8 to the source pixel
// (sr, sc) for a destination pixel (r, c) of a w×h source, per the standard
// EXIF transform table. Destination dims are w×h for orientations 1–4 and
// h×w for orientations 5–8 (the source's axes swap). Index 0 is unused.
//
//	o1 identity            out(r,c) = in(r,   c)
//	o2 flip horizontal     out(r,c) = in(r,   w-1-c)
//	o3 rotate 180°         out(r,c) = in(h-1-r, w-1-c)
//	o4 flip vertical       out(r,c) = in(h-1-r, c)
//	o5 transpose           out(r,c) = in(c,   r)
//	o6 rotate 90° CW       out(r,c) = in(h-1-c, r)
//	o7 transverse          out(r,c) = in(h-1-c, w-1-r)
//	o8 rotate 270° CW      out(r,c) = in(c,   w-1-r)
var orientIndex = [9]func(r, c, w, h int) (sr, sc int){
	1: func(r, c, w, h int) (int, int) { return r, c },
	2: func(r, c, w, h int) (int, int) { return r, w - 1 - c },
	3: func(r, c, w, h int) (int, int) { return h - 1 - r, w - 1 - c },
	4: func(r, c, w, h int) (int, int) { return h - 1 - r, c },
	5: func(r, c, w, h int) (int, int) { return c, r },
	6: func(r, c, w, h int) (int, int) { return h - 1 - c, r },
	7: func(r, c, w, h int) (int, int) { return h - 1 - c, w - 1 - r },
	8: func(r, c, w, h int) (int, int) { return c, w - 1 - r },
}

// applyOrientation returns img rotated/flipped per EXIF orientation o (1–8).
// o == 1 — and any out-of-range value — returns img unchanged: no allocation,
// byte-identity path. Output is a fresh *image.RGBA of the table's out dims;
// pixels are copied generically via At/Set, which preserves alpha wholesale:
// an opaque input yields an opaque output, so compositeOnWhite's fast path
// returns it unchanged (no third buffer — the rotation frame replaces the
// composite copy 1:1 in the per-request live peak). Correct for 1×1, 1×N,
// N×1 and HardMax² frames: source indices derive from the table formulas
// over w/h bounds, so no read can go out of range.
//
// It dispatches on the source's concrete type: *image.RGBA, *image.NRGBA,
// *image.YCbCr, *image.Gray, *image.Paletted and the depth-16 classes
// *image.RGBA64 / *image.NRGBA64 / *image.Gray16 take the direct-Pix fast
// kernels (pixfast.go / pixfast_more.go / pixfast_16.go); every other type
// falls through to applyOrientationGeneric — today's At/Set loop, preserved
// verbatim and byte-identical. Both paths consult ctx at the top of every
// cancelCheckRows-th row and return (nil, ctx.Err()) unwrapped on a done
// context; the o ≤ 1 / o ≥ 9 fast paths return (img, nil) without consulting
// ctx and without dispatching (FR-6).
func applyOrientation(ctx context.Context, img image.Image, o int) (image.Image, error) {
	if o <= 1 || o >= 9 {
		return img, nil
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	outW, outH := w, h
	if o >= 5 {
		outW, outH = h, w // orientations 5–8 swap the axes
	}
	switch s := img.(type) {
	case *image.RGBA:
		return rotateRGBA(ctx, s, o, w, h, outW, outH)
	case *image.NRGBA:
		return rotateNRGBA(ctx, s, o, w, h, outW, outH)
	case *image.YCbCr:
		return rotateYCbCr(ctx, s, o, w, h, outW, outH)
	case *image.Gray:
		return rotateGray(ctx, s, o, w, h, outW, outH)
	case *image.Paletted:
		return rotatePaletted(ctx, s, o, w, h, outW, outH)
	case *image.RGBA64:
		return rotateRGBA64(ctx, s, o, w, h, outW, outH)
	case *image.NRGBA64:
		return rotateNRGBA64(ctx, s, o, w, h, outW, outH)
	case *image.Gray16:
		return rotateGray16(ctx, s, o, w, h, outW, outH)
	default:
		return applyOrientationGeneric(ctx, img, o, b, w, h, outW, outH)
	}
}

// applyOrientationGeneric is applyOrientation's pre-fast-path loop, preserved
// verbatim: it is the byte anchor of the suite (TestFastPathByteIdentity
// compares the dispatcher against it) and the safe path for every
// non-RGBA/NRGBA source.
func applyOrientationGeneric(ctx context.Context, img image.Image, o int, b image.Rectangle, w, h, outW, outH int) (image.Image, error) {
	idx := orientIndex[o]
	dst := image.NewRGBA(image.Rect(0, 0, outW, outH))
	for r := 0; r < outH; r++ {
		if r%cancelCheckRows == 0 {
			if cerr := ctx.Err(); cerr != nil {
				return nil, cerr
			}
		}
		for c := 0; c < outW; c++ {
			sr, sc := idx(r, c, w, h)
			dst.Set(c, r, img.At(b.Min.X+sc, b.Min.Y+sr))
		}
	}
	return dst, nil
}
