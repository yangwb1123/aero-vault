// Package thumbnail generates downscaled JPEG previews of images using only the
// Go standard library (no external image dependencies). Supported source
// formats: JPEG, PNG, GIF. JPEG sources carrying a valid EXIF orientation
// (APP1 tag 0x0112) and PNG sources carrying a valid eXIf-chunk EXIF
// orientation (the same tag) are rotated/flipped before encoding so
// thumbnails render upright. As with JPEG, the orientation recorded in the
// profile is applied as-is even though the PNG spec flags possibly-stale Exif
// data as "of historical value only" — the industry-consistent behavior
// (browsers, Pillow's ImageOps.exif_transpose).
package thumbnail

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"io"

	// Register decoders for the supported formats (side-effect imports).
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
)

// ErrUnsupported is returned when the bytes are not a decodable image.
var ErrUnsupported = errors.New("thumbnail: unsupported or invalid image")

// ErrImageTooLarge is returned when the source image's declared dimensions
// exceed MaxSourceDim, exceed MaxProgressiveSourceDim for progressive (SOF2)
// JPEG sources, or exceed Max16BitSourceDim for depth-16 PNG sources. It is
// distinct from ErrUnsupported so callers can tell a dimension-capped
// rejection from a corrupt or non-image input.
var ErrImageTooLarge = errors.New("thumbnail: image dimensions exceed MaxSourceDim")

// ErrMetadataTooLarge is returned when the bytes consumed by image.DecodeConfig
// exceed MaxMetadataBytes (e.g. a JPEG whose pre-SOF region is packed with
// attacker-chosen APPn/COM segments, or a PNG whose pre-IDAT ancillary chunks
// — the eXIf orientation chunk included — push the pre-decode metadata walk
// past the cap; JPEG-parallel: PNGs with > 8 MiB of pre-IDAT metadata can no
// longer be thumbnailed). It is distinct from ErrUnsupported
// (corrupt/non-image) and ErrImageTooLarge (declared dimensions) so callers can
// tell a metadata-budget rejection from either. Do not confuse it with
// service.ErrMetadataTooLarge, which is the unrelated object-metadata size
// limit (internal/service/file.go).
var ErrMetadataTooLarge = errors.New("thumbnail: image metadata exceeds MaxMetadataBytes")

// ErrSourceTooLarge is returned when the compressed source stream is cut off
// by the MaxSourceBytes (128 MiB) read cap while the source still carried
// payload — a server-side processing-budget rejection. It is distinct from
// ErrUnsupported (corrupt/non-image input → client 400), ErrImageTooLarge
// (declared dimensions), and ErrMetadataTooLarge (metadata budget) so callers
// can tell a source-payload-budget rejection from each of the others.
var ErrSourceTooLarge = errors.New("thumbnail: source payload exceeds MaxSourceBytes (128 MiB)")

// errMetadataBudgetExceeded is the internal overflow cause written by
// limitedBuffer. Generate maps it to ErrMetadataTooLarge; keeping the cause
// unexported matches the package's sentinel pattern.
var errMetadataBudgetExceeded = errors.New("thumbnail: metadata budget exceeded")

// DefaultMax bounds thumbnail dimensions when the caller passes 0.
const (
	DefaultMax = 256
	HardMax    = 2048
	quality    = 82

	// cancelCheckRows bounds post-cancel CPU work inside the pixel phases:
	// scale and applyOrientation consult ctx.Err() at the top of every
	// cancelCheckRows-th row, so a canceled or deadline-expired request
	// aborts within at most cancelCheckRows rows of pixel work (≤
	// cancelCheckRows × HardMax × 4 ≈ 0.5M src.At calls at the HardMax
	// frame, low-ms).
	cancelCheckRows = 64

	// MaxSourceDim caps each declared source dimension (pixels). Sources with
	// any side above this are rejected from the header before any pixel buffer
	// is allocated, bounding worst-case decode allocation to MaxSourceDim²×4 B
	// = 256 MiB (8-bit PNG RGBA/NRGBA). Depth-16 PNG and progressive JPEG
	// sources are additionally capped below by Max16BitSourceDim and
	// MaxProgressiveSourceDim. Images larger than this can no longer be
	// thumbnailed; they are exactly the inputs that make the endpoint a
	// memory-DoS sink, and the output is capped at HardMax anyway.
	//
	// Live per-request totals (what the semaphore bounds): decode + scale dst
	// (≤ HardMax²×4 B = 16 MiB) + white-composite copy (≤ 16 MiB, post-scale
	// — see the scale→composite ordering note) + EXIF-rotation frame (≤
	// HardMax²×4 B = 16 MiB, JPEG-with-orientation path only; that path
	// skips the composite copy 1:1, so ≈ 288 MiB (8-bit) / ≈ 160 MiB
	// (16-bit at Max16BitSourceDim) live still hold). These are live-peak
	// figures; cumulative TotalAlloc also includes transient bilinear-stage
	// churn (see large_transparent_allocation_test.go). The aggregate
	// in-flight decode memory bound lives in maxConcurrentDecodes' doc
	// comment: the depth-16 aggregate (≈ 4 × 128 MiB ≈ 512 MiB) is now
	// inside the 2 GiB ceiling pinned in semaphore_test.go.
	MaxSourceDim = 8192

	// MaxProgressiveSourceDim caps each declared source dimension (pixels)
	// for progressive (SOF2) JPEG sources, below MaxSourceDim. Progressive
	// JPEGs carry full-image coefficient buffers per scan band, so a
	// progressive source at MaxSourceDim allocates ~1.1 GiB per request —
	// 4× the PNG RGBA baseline. Rejecting progressive sources above this
	// bound from the header (before any pixel buffer) cuts the per-request
	// progressive worst case 4× (4096²/8192²) to ~275 MiB, and with it the
	// aggregate ceiling (maxConcurrentDecodes × per-request) to ~1.1 GiB —
	// the same class as the PNG RGBA baseline — while common progressive
	// web exports (≤ 2048, e.g. Chrome/Android) and 4K-class sources up to
	// 4096 keep working. Baseline (SOF0/SOF1) sources are unaffected and
	// may still reach MaxSourceDim.
	MaxProgressiveSourceDim = 4096

	// MaxSourceBytes caps the compressed input consumed per Generate call.
	// A stream whose payload exceeds the cap (the source still carries data
	// at the cap) yields ErrSourceTooLarge — a server budget rejection; a
	// stream that ends at or before the cap without a complete image yields
	// ErrUnsupported (truncated/corrupt). The cap bounds reads, not object
	// size: a stream whose image terminator appears before the cap decodes
	// successfully even if the underlying object is larger.
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

// EffectiveDims maps a requested thumbnail bound pair to the values the
// decode pipeline actually applies, per dimension and independently:
// non-positive bounds default to DefaultMax (256), bounds above HardMax
// (2048) are capped to HardMax, in-range values pass through unchanged.
// generateLocked clamps via this function and the REST thumbnail handler
// derives its cache validator (ETag) from it, so validator dimensions
// always match the produced bytes. Pure integer arithmetic — no I/O, no
// allocation, no error return; every input pair maps to exactly one
// effective pair.
func EffectiveDims(maxW, maxH int) (int, int) {
	if maxW <= 0 {
		maxW = DefaultMax
	}
	if maxW > HardMax {
		maxW = HardMax
	}
	if maxH <= 0 {
		maxH = DefaultMax
	}
	if maxH > HardMax {
		maxH = HardMax
	}
	return maxW, maxH
}

// maxConcurrentDecodes caps how many Generate calls may be inside their
// allocation-bearing section (DecodeConfig through jpeg.Encode) at once.
// Aggregate live decode memory is therefore bounded by
// maxConcurrentDecodes × per-request worst case: ≈ 4 × 288 MiB ≈
// 1.125 GiB (8-bit PNG RGBA); ≈ 4 × 128 MiB ≈ 512 MiB (depth-16 PNG at
// Max16BitSourceDim); ≈ 4 × ~275 MiB ≈ 1.1 GiB (progressive JPEG at
// MaxProgressiveSourceDim) — all inside the 2 GiB absolute ceiling
// pinned in semaphore_test.go, regardless of MAX_INFLIGHT_REQUESTS /
// PER_TENANT_CONCURRENCY_MAX / rate limits. Waiters hold no stream at
// all: the REST thumbnail path acquires the slot before opening the
// object stream (GenerateContextWithOpener), so the semaphore also bounds
// concurrently open object streams / in-flight storage GETs to
// maxConcurrentDecodes.
const maxConcurrentDecodes = 4

// decodeSlots is the package-level blocking semaphore backing
// maxConcurrentDecodes (buffered-channel idiom; cf. middleware.ConcurrencyLimiter).
// A waiter's park is bounded by its request context (client disconnect or
// server deadline) — see acquireDecodeSlotContext — not by queue drain;
// waiters still allocate nothing and hold no stream.
var decodeSlots = make(chan struct{}, maxConcurrentDecodes)

// acquireDecodeSlot blocks until a slot is free. It is the context-less
// variant pinned by the deterministic semaphore tests; production entry
// points use acquireDecodeSlotContext. The caller must pair it with
// releaseDecodeSlot (defer), which runs on both normal return and panic.
func acquireDecodeSlot() { decodeSlots <- struct{}{} }

// acquireDecodeSlotContext acquires a slot, honoring ctx: it returns ctx.Err()
// without consuming a slot when ctx is done before the call or while the
// caller waits. After a winning send it re-checks ctx: a canceled context can
// race a ready buffer (both select branches ready), and the caller must not
// decode for a dead request — the slot is returned and the error propagated
// instead. The slot-return is non-blocking by construction (select with a
// default case), so the acquire path can only wait inside the select, which
// is ctx-bounded — it can never park on the slot-return. In the current 1:1
// acquire/release pairing the return is always ready (the winning send
// guarantees the buffer still holds at least that token), so the default
// branch is unreachable today; it exists so a future pairing violation fails
// toward over-restriction (an idle token, ≤ maxConcurrentDecodes effective
// concurrency) instead of a permanent park.
func acquireDecodeSlotContext(ctx context.Context) error {
	select {
	case decodeSlots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		select {
		case <-decodeSlots: // return the slot taken by the winning send
		default: // unreachable in the clean 1:1 pairing; fail toward over-restriction
		}
		return err
	}
	return nil
}

// releaseDecodeSlot returns a slot acquired by acquireDecodeSlot or
// acquireDecodeSlotContext.
func releaseDecodeSlot() { <-decodeSlots }

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
// DefaultMax; bounds are clamped to HardMax. A nil reader returns an error.
//
// Generate is equivalent to GenerateContext with context.Background(); it
// exists so non-cancellation callers and the deterministic semaphore tests
// keep today's semantics.
func Generate(r io.Reader, maxW, maxH int) ([]byte, error) {
	return GenerateContext(context.Background(), r, maxW, maxH)
}

// GenerateContext is Generate with cancellation: if the caller's context is
// done while the caller is waiting for a decode slot, it returns ctx.Err()
// without reading from r and without consuming a slot. For a non-nil context,
// a nil reader returns an error before slot acquisition or decode. A nil ctx is
// a caller bug (stdlib convention); the wrapper and the REST handler always pass a
// non-nil context. Cancellation is honored at acquisition (returns ctx.Err()
// without reading the stream or consuming a slot) and mid-decode: a stream
// failure caused by the context's deadline or cancellation is surfaced as
// the context error, never reclassified as ErrUnsupported. Every live source-read phase
// — DecodeConfig, PNG pre-IDAT orientation, payload Decode, and the separate
// MaxSourceBytes cap probe — passes through a context-checking reader
// (ctx_reader.go). Cancellation aborts the next delegated read without
// draining the source; a read already in progress may complete. A decode
// whose stream is fully read still aborts at the next phase boundary
// (post-config, post-decode, pre-encode) — and, inside the scale and
// rotation phases, within cancelCheckRows rows of pixel work, and inside
// jpeg.Encode at every emitted byte via the context-checking encode writer
// (plus a terminal check after Encode returns) — and releases its decode
// slot.
//
// GenerateContext acquires the decode slot itself. Callers that must hold the
// slot across an object-stream open (e.g. the REST thumbnail handler, whose
// invariant is that no request parks on the semaphore while holding an open
// stream) should use GenerateContextWithOpener instead: it acquires the slot,
// invokes the opener, and runs this exact decode pipeline with a single
// acquisition, so at most maxConcurrentDecodes object streams are open at
// once and waiters hold no stream.
func GenerateContext(ctx context.Context, r io.Reader, maxW, maxH int) ([]byte, error) {
	_ = ctx.Done() // preserve the existing nil-context caller-bug behavior
	if r == nil {
		return nil, errors.New("thumbnail: nil reader")
	}
	if err := acquireDecodeSlotContext(ctx); err != nil {
		return nil, err // before generateLocked: stream untouched, no slot consumed
	}
	defer releaseDecodeSlot()
	return generateLocked(ctx, r, maxW, maxH)
}

// progressiveJPEG reports whether the JPEG header in buf declares a
// progressive (SOF2) start-of-frame marker. It walks markers segment-aware —
// skipping each segment's payload via its 16-bit big-endian length field —
// and stops at the first SOF-family marker (SOF0/SOF1/SOF2) or at SOS,
// never scanning past the SOS header into entropy-coded data, where
// coincidental 0xFF 0xC2 byte pairs are ordinary data. (A naive
// bytes.Contains would false-positive on APPn payloads such as XMP/EXIF/ICC
// or on read-ahead entropy bytes, wrongly rejecting baseline sources with
// dims in (MaxProgressiveSourceDim, MaxSourceDim].) On any parse anomaly the
// walk reports false — behavior equals today's; unreachable in practice
// because image.DecodeConfig validated the same structure. buf is the
// budget-capped DecodeConfig tee buffer (head), so the walk is bounded by
// MaxMetadataBytes by construction and allocates nothing.
func progressiveJPEG(buf []byte) bool {
	if len(buf) < 2 || buf[0] != 0xFF || buf[1] != 0xD8 {
		return false // no SOI: not a parseable JPEG header
	}
	i := 2 // past SOI
	for i < len(buf) {
		for i < len(buf) && buf[i] == 0xFF { // marker fill (Annex B)
			i++
		}
		if i >= len(buf) {
			return false
		}
		m := buf[i]
		i++
		switch {
		case m == 0x00: // byte-stuffed 0xFF 0x00 at marker level: unparseable
			return false
		case m == 0xC0 || m == 0xC1 || m == 0xC2: // first SOF: the verdict
			return m == 0xC2
		case m == 0xD8, m == 0xD9, m == 0xDA: // SOI/EOI/SOS before SOF: stop
			return false
		case m == 0x01, m >= 0xD0 && m <= 0xD7: // standalone: TEM, RSTn
		default: // segment marker: skip payload by 16-bit length
			if i+2 > len(buf) {
				return false
			}
			n := int(buf[i])<<8 | int(buf[i+1])
			if n < 2 || i+n > len(buf) {
				return false
			}
			i += n // length includes its own 2 bytes
		}
	}
	return false
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
// preserving aspect ratio. Images already within bounds are returned
// unchanged. It dispatches on the source's concrete type: *image.RGBA,
// *image.NRGBA, *image.YCbCr, *image.Gray, *image.Paletted and the depth-16
// classes *image.RGBA64 / *image.NRGBA64 / *image.Gray16 take the direct-Pix
// fast kernels (pixfast.go / pixfast_more.go / pixfast_16.go); every other
// type (gateImage, custom image.Image) falls through to scaleGeneric —
// today's At/Set loop, preserved verbatim and byte-identical.
// Both paths consult ctx at the top of every cancelCheckRows-th row and
// return (nil, ctx.Err()) unwrapped on a done context, so a canceled or
// deadline-expired request aborts within cancelCheckRows rows of pixel work;
// the no-op paths (empty or in-bounds src) return (src, nil) without
// consulting ctx and without dispatching (FR-6). The fast kernels read
// src.Pix via y0*Stride + x0*4 (the sampled coordinates b.Min+(x0,y0)
// cancel Min in PixOffset; see pixfast.go's header) and write a fresh
// *image.RGBA of the same bounds/strides scaleGeneric would produce, so the
// two paths are byte-identical (pinned by TestFastPathByteIdentity).
func scale(ctx context.Context, src image.Image, maxW, maxH int) (image.Image, error) {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw == 0 || sh == 0 {
		return src, nil
	}
	ratio := minF(float64(maxW)/float64(sw), float64(maxH)/float64(sh))
	if ratio >= 1 {
		return src, nil // never upscale
	}
	tw := int(float64(sw) * ratio)
	th := int(float64(sh) * ratio)
	if tw < 1 {
		tw = 1
	}
	if th < 1 {
		th = 1
	}

	switch s := src.(type) {
	case *image.RGBA:
		return scaleRGBA(ctx, s, sw, sh, tw, th)
	case *image.NRGBA:
		return scaleNRGBA(ctx, s, sw, sh, tw, th)
	case *image.YCbCr:
		return scaleYCbCr(ctx, s, sw, sh, tw, th)
	case *image.Gray:
		return scaleGray(ctx, s, sw, sh, tw, th)
	case *image.Paletted:
		return scalePaletted(ctx, s, sw, sh, tw, th)
	case *image.RGBA64:
		return scaleRGBA64(ctx, s, sw, sh, tw, th)
	case *image.NRGBA64:
		return scaleNRGBA64(ctx, s, sw, sh, tw, th)
	case *image.Gray16:
		return scaleGray16(ctx, s, sw, sh, tw, th)
	default:
		return scaleGeneric(ctx, src, sw, sh, tw, th)
	}
}

// scaleGeneric is scale's pre-fast-path loop, preserved verbatim: it is the
// byte anchor of the suite (TestFastPathByteIdentity compares the dispatcher
// against it) and the safe path for every non-RGBA/NRGBA source. b is
// re-derived from src.Bounds() exactly as the original body did.
func scaleGeneric(ctx context.Context, src image.Image, sw, sh, tw, th int) (image.Image, error) {
	b := src.Bounds()

	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	for y := 0; y < th; y++ {
		if y%cancelCheckRows == 0 {
			if cerr := ctx.Err(); cerr != nil {
				return nil, cerr
			}
		}
		// map destination pixel center back into source space
		fy := (float64(y)+0.5)*float64(sh)/float64(th) - 0.5
		for x := 0; x < tw; x++ {
			fx := (float64(x)+0.5)*float64(sw)/float64(tw) - 0.5
			dst.Set(x, y, bilinear(src, b.Min.X, b.Min.Y, sw, sh, fx, fy))
		}
	}
	return dst, nil
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
