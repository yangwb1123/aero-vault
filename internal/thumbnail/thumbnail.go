// Package thumbnail generates downscaled JPEG previews of images using only the
// Go standard library (no external image dependencies). Supported source
// formats: JPEG, PNG, GIF.
package thumbnail

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	"io"

	// Register decoders for the supported formats (side-effect imports).
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
)

// ErrUnsupported is returned when the bytes are not a decodable image.
var ErrUnsupported = errors.New("thumbnail: unsupported or invalid image")

// DefaultMax bounds thumbnail dimensions when the caller passes 0.
const (
	DefaultMax = 256
	HardMax    = 2048
	quality    = 82
)

// Generate decodes an image from r and returns a JPEG thumbnail no larger than
// maxW×maxH (aspect ratio preserved; never upscaled). Zero bounds default to
// DefaultMax; bounds are clamped to HardMax.
func Generate(r io.Reader, maxW, maxH int) ([]byte, error) {
	if maxW <= 0 {
		maxW = DefaultMax
	}
	if maxH <= 0 {
		maxH = DefaultMax
	}
	if maxW > HardMax {
		maxW = HardMax
	}
	if maxH > HardMax {
		maxH = HardMax
	}

	src, _, err := image.Decode(r)
	if err != nil {
		return nil, ErrUnsupported
	}
	dst := scale(src, maxW, maxH)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// scale downsamples src to fit within maxW×maxH using bilinear interpolation,
// preserving aspect ratio. Images already within bounds are returned unchanged.
func scale(src image.Image, maxW, maxH int) image.Image {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw == 0 || sh == 0 {
		return src
	}
	ratio := minF(float64(maxW)/float64(sw), float64(maxH)/float64(sh))
	if ratio >= 1 {
		return src // never upscale
	}
	tw := int(float64(sw) * ratio)
	th := int(float64(sh) * ratio)
	if tw < 1 {
		tw = 1
	}
	if th < 1 {
		th = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	for y := 0; y < th; y++ {
		// map destination pixel center back into source space
		fy := (float64(y)+0.5)*float64(sh)/float64(th) - 0.5
		for x := 0; x < tw; x++ {
			fx := (float64(x)+0.5)*float64(sw)/float64(tw) - 0.5
			dst.Set(x, y, bilinear(src, b.Min.X, b.Min.Y, sw, sh, fx, fy))
		}
	}
	return dst
}

func bilinear(src image.Image, ox, oy, sw, sh int, fx, fy float64) (c rgba16) {
	x0 := int(fx)
	y0 := int(fy)
	dx := fx - float64(x0)
	dy := fy - float64(y0)
	x1 := clamp(x0+1, 0, sw-1)
	y1 := clamp(y0+1, 0, sh-1)
	x0 = clamp(x0, 0, sw-1)
	y0 = clamp(y0, 0, sh-1)

	r00, g00, b00, a00 := src.At(ox+x0, oy+y0).RGBA()
	r10, g10, b10, a10 := src.At(ox+x1, oy+y0).RGBA()
	r01, g01, b01, a01 := src.At(ox+x0, oy+y1).RGBA()
	r11, g11, b11, a11 := src.At(ox+x1, oy+y1).RGBA()

	lerp := func(v00, v10, v01, v11 uint32) uint16 {
		top := float64(v00)*(1-dx) + float64(v10)*dx
		bot := float64(v01)*(1-dx) + float64(v11)*dx
		return uint16(top*(1-dy) + bot*dy)
	}
	return rgba16{
		R: lerp(r00, r10, r01, r11),
		G: lerp(g00, g10, g01, g11),
		B: lerp(b00, b10, b01, b11),
		A: lerp(a00, a10, a01, a11),
	}
}

// rgba16 implements color.Color with 16-bit channels (matches RGBA()).
type rgba16 struct{ R, G, B, A uint16 }

func (c rgba16) RGBA() (r, g, b, a uint32) {
	return uint32(c.R), uint32(c.G), uint32(c.B), uint32(c.A)
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
