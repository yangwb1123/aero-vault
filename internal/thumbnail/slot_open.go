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

// SourceReadError wraps an error returned by the caller-supplied source
// stream mid-decode. It lets callers distinguish storage/verification read
// failures that surface through the codec read chains (local FS I/O, S3/OSS/
// COS network errors, an on-read ETagVerifier checksum mismatch) from
// codec-synthesized decode errors (image.ErrFormat, jpeg/png FormatError,
// io.ErrUnexpectedEOF from truncation) and from context errors, which stay
// unwrapped. The sets are disjoint, so errors.As on *SourceReadError before
// sentinel checks is exact (same contract as OpenError).
type SourceReadError struct{ Err error }

func (e *SourceReadError) Error() string {
	return "thumbnail: reading image source stream: " + e.Err.Error()
}

func (e *SourceReadError) Unwrap() error { return e.Err }

// sourceReadMarker marks non-EOF, non-context errors returned by the source
// stream so the decode sites can classify them as *SourceReadError instead
// of flattening them into ErrUnsupported. io.EOF passes through unmarked
// (truncated/corrupt objects keep classifying as ErrUnsupported → 400), as
// do errors matching context.DeadlineExceeded/Canceled via errors.Is
// (wrapped-SDK context errors keep their exact-instance contract at the
// decode sites). A partial read carrying an error keeps its n; a (0,nil)
// read passes through (no busy-loop synthesis).
type sourceReadMarker struct{ r io.Reader }

func (m *sourceReadMarker) Read(p []byte) (int, error) {
	n, err := m.r.Read(p)
	if err == nil || errors.Is(err, io.EOF) {
		return n, err
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return n, err
	}
	return n, &SourceReadError{Err: err}
}

// probeCapBytes bounds the single probe read sourceCapRecorder performs to
// decide whether the MaxSourceBytes budget was exhausted mid-stream. It is
// the only permitted over-read past the cap (pinned by the read-count
// assertions: ≤ MaxSourceBytes + 64 KiB).
const probeCapBytes = 64 << 10

// sourceCapRecorder wraps the io.LimitReader output: a byte-exact passthrough
// that records whether the limit's synthesized EOF cut a source stream that
// still carried data (capped). On the first (0, io.EOF) it performs ONE
// bounded probe read (≤ probeCapBytes) from the underlying source: the
// ctxReader-wrapped, sourceReadMarker-wrapped stream (construction site in
// generateLocked). n > 0, or a non-EOF error, means the source was cut off
// mid-stream (capped = true); a (0, io.EOF) means the source itself ended at
// or before the cap (capped = false — genuine truncation stays
// ErrUnsupported). The probe bypasses the DecodeConfig tee, so the metadata
// budget is unaffected. The probe runs at most once (probed): a codec that
// keeps reading after EOF must not re-probe.
//
// Context: the probe source is wrapped in ctxReader (outermost) around the
// marker (innermost) — mirroring the decode-payload path — so a dead context
// aborts the probe with (0, ctx.Err()) WITHOUT consulting the source (no
// uncancellable bounded read against slow remote storage, no decode slot held
// past the deadline; the exact ctx.Err() instance is recorded in probeErr).
// A non-EOF, non-context probe error is recorded in probeErr (marked
// *SourceReadError by the marker) and consulted at the two decode sites
// BEFORE capped: a genuine source-stream failure is a server-side error (500/
// 410 via the REST *SourceReadError arm), never misclassified as 413
// ErrSourceTooLarge. The marker stays the single classification seam: the
// limit's synthesized EOF is never marked (the marker sits inside the limit),
// and context errors never reach the marker (ctxReader checks ctx first).
//
// The recorder must wrap the LimitReader output (never sit inside it): the
// marker stays inside the limit, so the limit's synthesized EOF is never
// marked as a *SourceReadError.
type sourceCapRecorder struct {
	r        io.Reader // the io.LimitReader output
	src      io.Reader // the ctxReader→marker-wrapped source, for the probe
	capped   bool
	probed   bool
	probeErr error // non-EOF probe error (marked *SourceReadError via src); consult before capped
}

func (c *sourceCapRecorder) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if err == io.EOF && n == 0 && !c.probed {
		c.probed = true
		var buf [probeCapBytes]byte
		n, perr := c.src.Read(buf[:])
		c.capped = n > 0 || perr != io.EOF
		if perr != nil && perr != io.EOF {
			c.probeErr = perr
		}
	}
	return n, err
}

// open3 is the opener contract shared by the generation entry points: it
// returns the object stream plus the opened object's source ETag. The cached
// entry point uses the ETag to verify content identity before storing (a
// concurrent PUT between the caller's Stat and the open must never store
// new-version bytes under an old-version key); GenerateContextWithOpener
// discards it (its adapter returns "").
type open3 func() (io.ReadCloser, string, error)

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
	img, _, err := generateContextWithOpener3(ctx, maxW, maxH, func() (io.ReadCloser, string, error) {
		rc, err := open()
		return rc, "", err
	})
	return img, err
}

// generateContextWithOpener3 is the shared slot→open→decode→close→release
// body behind GenerateContextWithOpener and the cached entry point
// (GenerateContextWithOpenerCached). The slot is held BEFORE open runs; on
// success or error the slot is released and, if a stream was opened, the
// stream is closed — close runs before release, so open streams ≤
// maxConcurrentDecodes stays airtight. A (nil, nil) open result is a
// programming error and returns an error rather than panicking. The opened
// object's ETag is returned alongside the bytes so the cached entry point can
// verify content identity before storing.
func generateContextWithOpener3(ctx context.Context, maxW, maxH int, open open3) ([]byte, string, error) {
	if err := acquireDecodeSlotContext(ctx); err != nil {
		return nil, "", err // no slot consumed, no stream opened
	}
	defer releaseDecodeSlot() // registered first → runs LAST
	rc, etag, err := open()
	if err != nil {
		// Defensive close: an opener may return a non-nil stream together
		// with an error; the stream must not leak. The close error is
		// subordinate to the open error (the open failure is the contract).
		if rc != nil {
			_ = rc.Close()
		}
		return nil, "", &OpenError{Err: err}
	}
	if rc == nil {
		return nil, "", errors.New("thumbnail: opener returned nil stream with nil error")
	}
	defer rc.Close() // registered second → runs FIRST: stream closed while the slot is still held
	img, err := generateLocked(ctx, rc, maxW, maxH)
	return img, etag, err
}

// generateLocked is GenerateContext's decode body (dimension clamping through
// jpeg.Encode, including the phase-boundary context checks and the
// io.LimitReader/MaxSourceBytes and tee/MaxMetadataBytes machinery) with the
// slot acquisition hoisted to the caller. It NEVER acquires or releases a
// slot: GenerateContext and GenerateContextWithOpener both hold the slot
// across the call. Clamping was relocated here from GenerateContext's entry
// (pure integer arithmetic; byte-identical output). Cancellation is honored
// at the same phase boundaries as before and additionally inside
// scale/applyOrientation (every cancelCheckRows rows), inside jpeg.Encode
// at every emitted byte via the context-checking encode writer (plus a
// terminal ctx.Err() check after Encode returns), and inside the decode
// phase: payload reads through the context-checking reader (ctx_reader.go)
// abort at the next codec buffer fill. Source-stream read failures (storage
// I/O, on-read verification) surface through the sourceReadMarker as
// *SourceReadError — a server-side error class — instead of ErrUnsupported;
// only codec-synthesized errors and EOF/truncation keep classifying as
// ErrUnsupported.
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
	// Mark source-stream read failures (storage I/O, on-read verification)
	// at the module boundary: the codecs surface them unwrapped (jpeg fill /
	// png ReadFull), and the sites below classify marked errors as
	// *SourceReadError instead of ErrUnsupported. The marker sits INSIDE the
	// LimitReader so the limit's synthesized EOF is never marked, and
	// outside the ctxReader so the payload path's raw ctx.Err() aborts
	// unmarked. errMetadataBudgetExceeded is synthesized by limitedBuffer
	// (the tee write side), never by the source — unaffected.
	r = &sourceReadMarker{r: r}
	capSrc := r // marker-wrapped source: the cap probe reads through it
	r = io.LimitReader(r, MaxSourceBytes)
	// The cap recorder wraps the LimitReader OUTPUT (the marker stays inside
	// the limit): it records whether the limit's synthesized EOF cut a source
	// stream that still carried data. The probe reads through the marker, so
	// the limit's EOF is never misread as a source failure, and the probe
	// result is only the capped boolean — both decode sites classify
	// capped → ErrSourceTooLarge (a server budget rejection), distinct from
	// ErrUnsupported (truncated/corrupt input).
	rec := &sourceCapRecorder{r: r, src: &ctxReader{ctx: ctx, r: capSrc}}
	r = rec

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
		// A marked source-stream failure (storage I/O, on-read verification)
		// is a server-side error, never a client "not an image": surface the
		// marked instance itself (identity preserved, never re-wrapped). Only
		// unmarked errors reach this branch (the marker exempts EOF and
		// context sentinels), so the classification is exact.
		var sre *SourceReadError
		if errors.As(err, &sre) {
			return nil, sre
		}
		if rec.probeErr != nil {
			// The cap probe surfaced a genuine source-stream failure (the
			// marker classified it as *SourceReadError): the budget was hit,
			// but the read failed — the storage error, not 413, is the
			// contract. Structurally unreachable here (the 8 MiB metadata tee
			// budget trips before the 128 MiB cap); kept for budget-drift
			// robustness, same philosophy as the capped check below.
			return nil, rec.probeErr
		}
		if rec.capped {
			// The MaxSourceBytes read cap cut a still-alive source mid-decode:
			// a server-side processing-budget rejection, not corrupt input.
			// Structurally unreachable here (the 8 MiB metadata tee budget
			// trips first); kept for budget-drift robustness, the same
			// philosophy as FuzzGenerate's accepted set.
			return nil, ErrSourceTooLarge
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
	// PNG eXIf orientation: unlike JPEG (whose APP1 DecodeConfig consumed
	// into head), a PNG's eXIf chunk sits mid-stream, so the walk must run
	// HERE — before Decode consumes the stream — reading pre-IDAT chunks
	// from head[33:] and then r through the replay tee. Every byte the walk
	// reads from the stream is replayed to Decode (byte-identical frame);
	// the walk is bounded by MaxMetadataBytes (head + replay ≤ budget, the
	// same expression both sides) and stops at IDAT (compressed data is
	// never scanned). Errors classify with the exact Decode-site block.
	// decodeR replays the exact prefix DecodeConfig (and, for PNG, the
	// orientation walk) consumed, then continues from the raw stream r
	// through the context-checking payload reader: mid-decode cancellation
	// aborts at the next codec buffer fill instead of running the decode to
	// completion (see ctx_reader.go). The tee paths above are intentionally
	// NOT wrapped — the config-scan budget and its budget-wins-over-ctx
	// ordering are pinned by TestGenerateContextMetadataBudgetWinsOverDeadline.
	payloadR := &ctxReader{ctx: ctx, r: r}
	pngOrient := 1
	decodeR := io.MultiReader(bytes.NewReader(head.buf.Bytes()), payloadR)
	if format == "png" {
		replay := &limitedBuffer{remaining: MaxMetadataBytes - len(head.buf.Bytes())}
		pngOrient, err = pngOrientation(ctx, head.buf.Bytes(), io.TeeReader(r, replay))
		if err != nil {
			if errors.Is(err, errMetadataBudgetExceeded) {
				return nil, ErrMetadataTooLarge
			}
			if cerr := ctx.Err(); cerr != nil {
				return nil, cerr
			}
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				return nil, err // same instance
			}
			// Any other read error (EOF/truncation/generic): stop the walk
			// with orientation 1 and let Decode re-encounter the same bytes
			// and error — marked source-stream errors re-surface as
			// *SourceReadError (server error class); codec/truncation errors
			// classify as before (→ ErrUnsupported).
			pngOrient = 1
		}
		decodeR = io.MultiReader(bytes.NewReader(head.buf.Bytes()), bytes.NewReader(replay.buf.Bytes()), payloadR)
	}
	src, _, err := image.Decode(decodeR)
	if err != nil {
		// Same identity preservation as the DecodeConfig branch above: a
		// deadline/cancellation that fires while the payload is read aborts
		// at the next codec buffer fill (≤ 4 KiB over-read, see ctx_reader.go)
		// and is surfaced as the context error — promptly, never flattened
		// to ErrUnsupported; ctx.Err() wins over a coincident genuine decode
		// error.
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return nil, err
		}
		// Same marked-error classification as the DecodeConfig branch: a
		// source-stream failure (storage I/O, on-read verification) that
		// surfaces mid-payload is a server-side error — return the marked
		// instance itself, never ErrUnsupported (identity preserved).
		var sre *SourceReadError
		if errors.As(err, &sre) {
			return nil, sre
		}
		if rec.probeErr != nil {
			// The cap probe surfaced a genuine source-stream failure (the
			// marker classified it as *SourceReadError): the budget was hit,
			// but the read failed — the storage error, not 413, is the
			// contract.
			return nil, rec.probeErr
		}
		if rec.capped {
			// The MaxSourceBytes read cap cut a still-alive source
			// mid-payload: a server-side processing-budget rejection, not
			// corrupt/truncated input. Distinct from ErrUnsupported so the
			// REST layer maps it to 413 SourceTooLarge, not 400 InvalidArgument.
			return nil, ErrSourceTooLarge
		}
		return nil, ErrUnsupported
	}
	if cerr := ctx.Err(); cerr != nil {
		return nil, cerr
	}
	// EXIF orientation (JPEG APP1 + PNG eXIf): the JPEG tag lives in the
	// APP1 segment that DecodeConfig consumed into head (its config scan
	// reads every pre-SOS segment in full), so extraction is free and bounded
	// by MaxMetadataBytes; the PNG walk ran pre-Decode (see above), bounded
	// by the same cap, and stops at IDAT. Orientations 5–8 swap the box so
	// the rotated frame still fits maxW×maxH. Rotation runs post-scale,
	// pre-composite; the rotated frame is opaque, so compositeOnWhite's fast
	// path keeps the ≈ 288 MiB ceiling.
	orient := 1
	if format == "jpeg" {
		orient = exifOrientation(head.buf.Bytes())
	} else if format == "png" {
		orient = pngOrient
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
