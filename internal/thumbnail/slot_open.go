package thumbnail

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/jpeg"
	"io"
)

// OpenError wraps an error returned by the opener injected into
// GenerateContextWithOpener. It lets callers distinguish object-stream open
// failures (e.g. FileService ErrNotFound/ErrForbidden/ErrObjectCorrupt,
// storage or SSE errors, or a context error observed by the opener) from
// decode-pipeline sentinels and context errors, which are returned unwrapped.
// The sets are disjoint (service errors vs. thumbnail sentinels vs. context
// errors), so errors.As on *OpenError before sentinel checks is exact.
type OpenError struct{ Err error }

func (e *OpenError) Error() string { return "thumbnail: open object stream: " + e.Err.Error() }

func (e *OpenError) Unwrap() error { return e.Err }

// GenerateContextWithOpener acquires a decode slot, then invokes open to
// obtain the object stream, then runs the exact GenerateContext decode
// pipeline on it (generateLocked). Ordering is load-bearing: the slot is held
// BEFORE open runs, so at most maxConcurrentDecodes object streams are open
// at once (the REST thumbnail handler relies on this to bound fds / in-flight
// storage GETs), and a request parked on the semaphore holds no stream at
// all. Cancellation while parked returns ctx.Err() without opening the stream
// and without consuming a slot. On success or error the slot is released and,
// if a stream was opened, the stream is closed — close runs before release,
// so "opens only occur inside held slots" makes open streams ≤
// maxConcurrentDecodes airtight. A nil open func or a (nil, nil) open result
// is a programming error and returns an error rather than panicking. A nil
// ctx is a caller bug (stdlib convention), as in GenerateContext.
func GenerateContextWithOpener(ctx context.Context, maxW, maxH int, open func() (io.ReadCloser, error)) ([]byte, error) {
	if open == nil {
		return nil, errors.New("thumbnail: GenerateContextWithOpener: nil opener")
	}
	if err := acquireDecodeSlotContext(ctx); err != nil {
		return nil, err // no slot consumed, no stream opened
	}
	defer releaseDecodeSlot() // registered first → runs LAST
	rc, err := open()
	if err != nil {
		// Defensive close: an opener may return a non-nil stream together
		// with an error; the stream must not leak. The close error is
		// subordinate to the open error (the open failure is the contract).
		if rc != nil {
			_ = rc.Close()
		}
		return nil, &OpenError{Err: err}
	}
	if rc == nil {
		return nil, errors.New("thumbnail: GenerateContextWithOpener: opener returned nil stream with nil error")
	}
	defer rc.Close() // registered second → runs FIRST: stream closed while the slot is still held
	return generateLocked(ctx, rc, maxW, maxH)
}

// generateLocked is GenerateContext's decode body (dimension clamping through
// jpeg.Encode, including the phase-boundary context checks and the
// io.LimitReader/MaxSourceBytes and tee/MaxMetadataBytes machinery) with the
// slot acquisition hoisted to the caller. It NEVER acquires or releases a
// slot: GenerateContext and GenerateContextWithOpener both hold the slot
// across the call. Clamping was relocated here from GenerateContext's entry
// (pure integer arithmetic; byte-identical output). Cancellation is honored
// at the same phase boundaries as before and additionally inside
// scale/applyOrientation (every cancelCheckRows rows) and inside jpeg.Encode
// at every emitted byte via the context-checking encode writer (plus a
// terminal ctx.Err() check after Encode returns).
func generateLocked(ctx context.Context, r io.Reader, maxW, maxH int) ([]byte, error) {
	// Single source of truth for the dimension rule: generateLocked and the
	// REST handler both derive effective bounds from EffectiveDims, so the
	// cache validator can never drift from the produced bytes.
	maxW, maxH = EffectiveDims(maxW, maxH)

	// Aggregate bound (unchanged contract): at most maxConcurrentDecodes calls
	// hold the allocation-bearing section below (config scan, decode, scale,
	// composite, encode) at once; held for the entire section so the bound is
	// airtight. The wait now honors ctx — a parked caller unblocks on cancel
	// and never reaches the decode section; waiters allocate nothing and hold
	// no stream.
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
	cfg, format, err := image.DecodeConfig(cfgR)
	if err != nil {
		if errors.Is(err, errMetadataBudgetExceeded) {
			return nil, ErrMetadataTooLarge
		}
		// A ctx-bound storage stream fails exactly when the request context is
		// done: surface the context error (DeadlineExceeded/Canceled) instead
		// of reclassifying it as ErrUnsupported (REST maps it to 504 or a
		// silent return). The ctx.Err() check precedes the sentinel fallback —
		// ctx.Err() is authoritative; the errors.Is fallback covers streams
		// that surface a literal/wrapped sentinel while ctx.Err() is
		// momentarily nil (e.g. SDK error paths captured the error earlier).
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return nil, err
		}
		return nil, ErrUnsupported
	}
	if cfg.Width > MaxSourceDim || cfg.Height > MaxSourceDim {
		return nil, ErrImageTooLarge // payload never read
	}
	// Progressive (SOF2) sources carry full-image coefficient buffers per
	// scan band, so they get a lower dimension bound than baseline sources:
	// a progressive JPEG above MaxProgressiveSourceDim is rejected from the
	// header with the same sentinel, before any pixel buffer is allocated.
	// The SOF2 marker is already inside head (DecodeConfig consumed it), so
	// the detection walk is free and is skipped entirely for the common
	// small-image path (the format/dims gates run first).
	if format == "jpeg" &&
		(cfg.Width > MaxProgressiveSourceDim || cfg.Height > MaxProgressiveSourceDim) &&
		progressiveJPEG(head.buf.Bytes()) {
		return nil, ErrImageTooLarge // payload never read
	}
	// Depth-16 PNG sources decode at 8 B/px (stdlib image/png), so they get
	// the same lower bound as progressive JPEGs: above Max16BitSourceDim
	// they are rejected from the header, before any pixel buffer is
	// allocated (see png16.go for the IHDR byte location and rules).
	if format == "png" && pngBitDepth(head.buf.Bytes()) == 16 &&
		(cfg.Width > Max16BitSourceDim || cfg.Height > Max16BitSourceDim) {
		return nil, ErrImageTooLarge // payload never read
	}
	if cerr := ctx.Err(); cerr != nil {
		return nil, cerr
	}
	src, _, err := image.Decode(io.MultiReader(bytes.NewReader(head.buf.Bytes()), r))
	if err != nil {
		// Same identity preservation as the DecodeConfig branch above: a
		// deadline/cancellation that fires while the payload is read is
		// surfaced as the context error, never flattened to ErrUnsupported;
		// ctx.Err() wins over a coincident genuine decode error.
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return nil, err
		}
		return nil, ErrUnsupported
	}
	if cerr := ctx.Err(); cerr != nil {
		return nil, cerr
	}
	// EXIF orientation (JPEG only): the tag lives in the APP1 segment that
	// DecodeConfig consumed into head (its config scan reads every pre-SOS
	// segment in full), so extraction is free and bounded by MaxMetadataBytes.
	// Orientations 5–8 swap the box so the rotated frame still fits maxW×maxH.
	// Rotation runs post-scale, pre-composite; the rotated frame is opaque, so
	// compositeOnWhite's fast path keeps the ≈ 288 MiB ceiling.
	orient := 1
	if format == "jpeg" {
		orient = exifOrientation(head.buf.Bytes())
	}
	boxW, boxH := maxW, maxH
	if orient >= 5 {
		boxW, boxH = maxH, maxW
	}
	dst, err := scale(ctx, src, boxW, boxH)
	if err != nil {
		return nil, err // ctx sentinel propagates unwrapped
	}
	if orient > 1 {
		dst, err = applyOrientation(ctx, dst, orient)
		if err != nil {
			return nil, err
		}
	}
	// Ordering is load-bearing (pinned by TestGenerateLargeTransparentAllocationBounded
	// and TestCompositeOrderingFeatheredByteLevel): the white composite must run on
	// the scaled dst (≤ HardMax², ≤ 16 MiB copy), never on the full-resolution src —
	// Opaque() is attacker-controlled (one transparent pixel at the decoded scale
	// suffices) and a pre-scale composite would copy the full decoded frame
	// (256/512 MiB) plus scale churn, ≈ 656/912 MiB per request, invalidating the
	// documented ceiling.
	dst = compositeOnWhite(dst) // JPEG has no alpha; flatten transparency before encode.
	if cerr := ctx.Err(); cerr != nil {
		return nil, cerr
	}
	var buf bytes.Buffer
	// In-encode cancellation boundary (encode_writer.go): while ctx is alive
	// the writer is a pure pass-through (byte-identical output — the encoder
	// uses it directly, dropping the bufio, which is transparent to bytes);
	// once ctx is done the first write returns the exact ctx.Err(), the
	// encoder's sticky error stops emission, and Encode returns it unwrapped.
	// The target goes through the encodeSink seam so tests can pin this
	// wiring deterministically (encode_cancel_test.go, F1/F2).
	if err := jpeg.Encode(encodeSink(ctx, &buf), dst, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	// Terminal check: a cancel landing after the final flush but before
	// Encode returns (or after Encode returned nil) must still surface the
	// context error rather than a 200 (F2 contract). No-op while ctx is alive.
	if cerr := ctx.Err(); cerr != nil {
		return nil, cerr
	}
	return buf.Bytes(), nil
}
