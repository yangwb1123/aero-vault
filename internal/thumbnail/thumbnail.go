// Package thumbnail generates downscaled JPEG previews of images using only the
// Go standard library (no external image dependencies). Supported source
// formats: JPEG, PNG, GIF.
package thumbnail

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"io"

	// Register decoders for the supported formats (side-effect imports).
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
)

// ErrUnsupported is returned when the bytes are not a decodable image.
var ErrUnsupported = errors.New("thumbnail: unsupported or invalid image")

// ErrImageTooLarge is returned when the source image's declared dimensions
// exceed MaxSourceDim. It is distinct from ErrUnsupported so callers can tell
// a dimension-capped rejection from a corrupt or non-image input.
var ErrImageTooLarge = errors.New("thumbnail: image dimensions exceed MaxSourceDim")

// ErrMetadataTooLarge is returned when the bytes consumed by image.DecodeConfig
// exceed MaxMetadataBytes (e.g. a JPEG whose pre-SOF region is packed with
// attacker-chosen APPn/COM segments). It is distinct from ErrUnsupported
// (corrupt/non-image) and ErrImageTooLarge (declared dimensions) so callers can
// tell a metadata-budget rejection from either. Do not confuse it with
// service.ErrMetadataTooLarge, which is the unrelated object-metadata size
// limit (internal/service/file.go).
var ErrMetadataTooLarge = errors.New("thumbnail: image metadata exceeds MaxMetadataBytes")

// errMetadataBudgetExceeded is the internal overflow cause written by
// limitedBuffer. Generate maps it to ErrMetadataTooLarge; keeping the cause
// unexported matches the package's sentinel pattern.
var errMetadataBudgetExceeded = errors.New("thumbnail: metadata budget exceeded")

// DefaultMax bounds thumbnail dimensions when the caller passes 0.
const (
	DefaultMax = 256
	HardMax    = 2048
	quality    = 82

	// MaxSourceDim caps each declared source dimension (pixels). Sources with
	// any side above this are rejected from the header before any pixel buffer
	// is allocated, bounding worst-case decode allocation to MaxSourceDim²×4 B
	// ≈ 268 MiB (PNG RGBA); a progressive JPEG at this size reaches ~1.1 GiB
	// per request (full-image coefficient buffers plus the decoded frame).
	// Images larger than this can no longer be thumbnailed; they are exactly
	// the inputs that make the endpoint a memory-DoS sink, and the output is
	// capped at HardMax anyway.
	//
	// Note: the 268 MiB worst case is per request, and MAX_INFLIGHT_REQUESTS /
	// PER_TENANT_CONCURRENCY_MAX both default to 0 (unlimited concurrency) —
	// aggregate memory pressure is bounded only by operator-set concurrency
	// and rate limits. Tighten those knobs, or lower this constant, if the
	// endpoint shows aggregate pressure.
	MaxSourceDim = 8192

	// MaxSourceBytes caps the compressed input consumed per Generate call.
	// A stream that ends before a complete image decodes within this cap
	// yields ErrUnsupported (reject; never hang, never partially decode).
	// The cap bounds reads, not object size: a stream whose image terminator
	// appears before the cap decodes successfully even if the underlying
	// object is larger.
	MaxSourceBytes = 128 << 20

	// MaxMetadataBytes caps what image.DecodeConfig may consume into the tee
	// buffer (head) before Generate aborts with ErrMetadataTooLarge. The
	// bound is necessary because image/jpeg's config scan reads every pre-SOF
	// segment (APPn/COM/DHT/DQT/DRI) in full and the segment count is
	// attacker-controlled, so head is not "metadata-size" by construction —
	// without the cap it is bounded only by MaxSourceBytes (128 MiB), and the
	// replay doubles that in memory. GIF/PNG DecodeConfig consume only tens
	// of bytes by design, but they pass through the same tee, so the cap
	// bounds any codec. 8 MiB covers the largest legitimate pre-SOF classes
	// (ICC profiles, XMP, EXIF) with >=2x margin; images with more pre-SOF
	// metadata can no longer be thumbnailed.
	MaxMetadataBytes = 8 << 20
)

// limitedBuffer is an io.Writer that accepts writes while the total bytes
// accepted stay within max, then fails every further write with
// errMetadataBudgetExceeded — writing nothing for the overflowing write.
// Failure is sticky: the first overflow zeroes the remaining budget, so every
// subsequent write returns the same error. Returning (0, err) rather than a
// partial write is load-bearing: image/jpeg's decoder fill treats a read that
// returns n > 0 together with an error as success, so a partial write would
// swallow the abort and reads would continue past the budget. io.TeeReader
// propagates the writer error as the Read error, so image.DecodeConfig aborts
// on the first read that crosses the budget.
type limitedBuffer struct {
	buf       bytes.Buffer
	remaining int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if len(p) > l.remaining {
		l.remaining = 0
		return 0, errMetadataBudgetExceeded
	}
	_, _ = l.buf.Write(p)
	l.remaining -= len(p)
	return len(p), nil
}

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

	// Bound the compressed input consumed per call; this also governs the
	// DecodeConfig reads below so no caller can drive unbounded reads.
	r = io.LimitReader(r, MaxSourceBytes)

	// Header-only dimension pre-check: no pixel buffer is ever allocated for
	// oversized sources. image.DecodeConfig consumes from the stream, and each
	// codec additionally wraps the reader in its own bufio whose read-ahead
	// drains it — so sharing a single buffered reader between DecodeConfig and
	// Decode would lose the header bytes for small inputs. Instead, tee what
	// DecodeConfig consumes into head, replay that exact prefix for Decode,
	// and continue from the raw stream r (not the tee): replaying head then r
	// is byte-exact and keeps Decode streaming (the payload is read live from
	// r, never buffered). head is capped at MaxMetadataBytes: image/jpeg's
	// config scan reads every pre-SOF segment (APPn/COM/DHT/DQT/DRI) in full
	// and the segment count is attacker-controlled, so without the cap head
	// would grow with the payload (bounded only by MaxSourceBytes); exceeding
	// the budget aborts the config scan with ErrMetadataTooLarge before the
	// payload is read further or any pixel buffer is allocated.
	head := &limitedBuffer{remaining: MaxMetadataBytes}
	cfgR := io.TeeReader(r, head)
	cfg, _, err := image.DecodeConfig(cfgR)
	if err != nil {
		if errors.Is(err, errMetadataBudgetExceeded) {
			return nil, ErrMetadataTooLarge
		}
		return nil, ErrUnsupported
	}
	if cfg.Width > MaxSourceDim || cfg.Height > MaxSourceDim {
		return nil, ErrImageTooLarge // payload never read
	}
	src, _, err := image.Decode(io.MultiReader(bytes.NewReader(head.buf.Bytes()), r))
	if err != nil {
		return nil, ErrUnsupported
	}
	dst := scale(src, maxW, maxH)
	dst = compositeOnWhite(dst) // JPEG has no alpha; flatten transparency before encode.

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// compositeOnWhite returns img unchanged when it is fully opaque; otherwise it
// returns an opaque copy of img composited onto a white background. JPEG has no
// alpha channel, so transparency must be flattened before encoding: the generic
// encoder path serializes premultiplied RGBA() values verbatim, which renders
// transparent regions black (or darkened). Skipping the composite for opaque
// images is required, not an optimization: it keeps opaque-source output
// byte-identical to the pre-fix encoder path.
func compositeOnWhite(img image.Image) image.Image {
	if o, ok := img.(interface{ Opaque() bool }); ok && o.Opaque() {
		return img
	}
	b := img.Bounds()
	out := image.NewRGBA(b)
	draw.Draw(out, b, image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(out, b, img, b.Min, draw.Over)
	return out
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
