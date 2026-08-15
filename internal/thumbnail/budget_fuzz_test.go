package thumbnail

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"io"
	"testing"
	"time"
)

// FuzzGenerateBudgets extends fuzz coverage to the MaxMetadataBytes (8 MiB)
// and MaxSourceBytes (128 MiB) budget boundaries that FuzzGenerate's 64 KiB
// input cap cannot reach. The budget machinery inside generateLocked is
// exercised directly on in-memory streams (no opener, no REST): the
// DecodeConfig tee (metadata budget → ErrMetadataTooLarge) and the source
// cap + probe discriminator (capped → ErrSourceTooLarge; EOF at/under the
// cap → ErrUnsupported).
//
// The input is a multi-argument selector, not a raw []byte: the boundary
// streams are constructed in-body from small args. Go's fuzz engine passes
// inputs through a 100 MiB shared-memory file (internal/fuzz/worker.go,
// workerSharedMemSize = 100<<20) and panics above it (internal/fuzz/mem.go,
// setValue) — a 128 MiB corpus seed cannot reach a worker, so a raw-byte
// signature is structurally impossible for the source-cap boundary.
//
// shape % 2 selects the leg: 0 = metadata flood (SOI + streaming APP1
// segments totaling MaxMetadataBytes+delta payload + data tail), 1 = source
// cap (boundedSource{prefix: data, total: MaxSourceBytes+delta}). delta is
// intentionally unclamped (int8 range [-128, 127]): boundary±1 is seeded
// and mutation drift probes budget-drift space. cancel=true runs
// GenerateContext with a deterministic cancel-vs-budget handshake: seed S7
// pins the budget-wins-over-cancel ordering at the exact 8 MiB boundary
// (the DecodeConfig tee is deliberately not ctx-wrapped, slot_open.go /
// ctx_reader.go); mutated cancel inputs assert the
// ErrMetadataTooLarge|ErrImageTooLarge|context.Canceled outcome set (the
// dims gates run before the post-config ctx check, so a canceled ctx with
// oversized declared dims yields ErrImageTooLarge, not the context error).
//
// Under -race the target's seeds are skipped wholesale (raceEnabled seam):
// measured on this machine, the CI race gate (ci.yml, -timeout 120s) runs
// at 117.6 s of 120 s — the shape-1 seeds stream 384 MiB (17.9 s under
// race) and even the shape-0 floods add ~1.7 s, inside flake margin. Every
// budget branch is race-covered by the unit tests that already run in that
// gate (TestGenerateSourceBytesBound, TestGenerateSourceCapBoundaryPins,
// TestGenerateMetadataAtExactBudget,
// TestGenerateContextMetadataBudgetWinsOverDeadline); the seed invariants
// run at full volume under plain `go test` (make check) and under
// `go test -fuzz`.
//
// Run: go test -fuzz=FuzzGenerateBudgets -run '^$' -fuzztime=30s ./internal/thumbnail/
func FuzzGenerateBudgets(f *testing.F) {
	base := appnPaddedJPEG(f, 0) // plain 8×8 baseline JPEG
	tail := base[2:]             // post-SOI bytes: S1's stream = SOI + flood + tail is byte-identical to appnPaddedJPEG(f, MaxMetadataBytes)
	i := bytes.Index(base, []byte{0xFF, 0xDA})
	if i < 0 {
		f.Fatal("no SOS marker in fixture")
	}
	n := int(binary.BigEndian.Uint16(base[i+2 : i+4]))
	prefix := base[:i+2+n] // through the SOS header; entropy replaced by zeros in-body

	// S1–S3: metadata budget at exact boundary and boundary±1. All reject:
	// the tee also counts SOI + segment headers + the tail on top of the
	// payload total (pinned by TestGenerateMetadataAtExactBudget).
	f.Add(tail, byte(0), int8(0), false)
	f.Add(tail, byte(0), int8(-1), false)
	f.Add(tail, byte(0), int8(+1), false)
	// S4–S6: source cap at exact boundary and boundary±1. EOF exactly at the
	// cap → capped=false → ErrUnsupported; one byte beyond → capped=true →
	// ErrSourceTooLarge (pinned by TestGenerateSourceCapBoundaryPins).
	f.Add(prefix, byte(1), int8(0), false)
	f.Add(prefix, byte(1), int8(+1), false)
	f.Add(prefix, byte(1), int8(-1), false)
	// S7: canceled ctx at the exact 8 MiB boundary — budget wins over cancel.
	f.Add(tail, byte(0), int8(0), true)

	f.Fuzz(func(t *testing.T, data []byte, shape byte, delta int8, cancel bool) {
		if raceEnabled {
			// See the doc comment: CI race-gate headroom is ~2.4 s; the
			// heavy seeds would push it past the 120 s timeout.
			return
		}
		shape %= 2
		if cancel {
			err := runCancelLeg(t, budgetStream(shape, delta, data))
			if shape == 0 && delta >= 0 {
				// Over budget by construction (flood payload ≥ 8 MiB plus
				// SOI/headers/tail): the budget abort must win over the
				// canceled ctx (the tee is not ctx-wrapped).
				if !errors.Is(err, ErrMetadataTooLarge) {
					t.Fatalf("canceled over-budget stream: err = %v, want ErrMetadataTooLarge", err)
				}
				return
			}
			// Under-budget or source-leg cancel: the config scan completes
			// (dims gate may fire ErrImageTooLarge first) and the post-config
			// ctx check surfaces context.Canceled; an over-budget mutation
			// trips the budget first.
			if !errors.Is(err, ErrMetadataTooLarge) && !errors.Is(err, ErrImageTooLarge) &&
				!errors.Is(err, context.Canceled) {
				t.Fatalf("canceled stream: err = %v, want ErrMetadataTooLarge, ErrImageTooLarge or context.Canceled", err)
			}
			return
		}
		out, err := Generate(budgetStream(shape, delta, data), 64, 64)
		if err != nil {
			if !errors.Is(err, ErrUnsupported) && !errors.Is(err, ErrImageTooLarge) &&
				!errors.Is(err, ErrMetadataTooLarge) && !errors.Is(err, ErrSourceTooLarge) {
				t.Fatalf("Generate returned non-sentinel error: %v", err)
			}
			return
		}
		img, format, derr := image.Decode(bytes.NewReader(out))
		if derr != nil || format != "jpeg" || img == nil ||
			img.Bounds().Dx() > 64 || img.Bounds().Dy() > 64 {
			dims := "nil"
			if img != nil {
				dims = img.Bounds().String()
			}
			t.Fatalf("invalid thumbnail: format=%q dims=%s decodeErr=%v", format, dims, derr)
		}
	})
}

// budgetStream constructs the leg stream for shape (0 = metadata flood,
// 1 = source cap) from the small selector args. Both legs stream — the
// boundary sizes are never materialized (per-iteration memory is
// O(len(data)) plus the ≤ 8 MiB DecodeConfig tee).
func budgetStream(shape byte, delta int8, data []byte) io.Reader {
	if shape == 1 {
		return &boundedSource{prefix: data, total: MaxSourceBytes + int(delta)}
	}
	return io.MultiReader(
		bytes.NewReader([]byte{0xFF, 0xD8}), // SOI
		&appnFlood{remaining: MaxMetadataBytes + int(delta)},
		bytes.NewReader(data), // seed tail / mutated tail
	)
}

// appnFlood streams JPEG APP1 segments (marker 0xFF 0xE1, 16-bit length
// n+2) whose payloads sum to exactly remaining, then (0, io.EOF). The
// segment shape matches appnPaddedJPEG (payload ≤ 65533); the flood is
// streamed, never buffered. A total ≤ 0 yields an empty flood (SOI + tail
// only).
type appnFlood struct{ remaining int }

func (f *appnFlood) Read(p []byte) (int, error) {
	if f.remaining <= 0 {
		return 0, io.EOF
	}
	n := 0
	for n < len(p) && f.remaining > 0 {
		avail := len(p) - n
		if avail < 5 { // no room for a header plus ≥ 1 payload byte; caller re-Reads
			break
		}
		payload := avail - 4
		if payload > f.remaining {
			payload = f.remaining
		}
		if payload > 65533 {
			payload = 65533
		}
		p[n], p[n+1] = 0xFF, 0xE1 // APP1
		binary.BigEndian.PutUint16(p[n+2:n+4], uint16(payload+2))
		for i := 0; i < payload; i++ {
			p[n+4+i] = 0x42
		}
		n += 4 + payload
		f.remaining -= payload
	}
	return n, nil
}

// runCancelLeg runs GenerateContext over stream with a deterministic
// cancel-vs-budget handshake (the gatedReader pattern from
// TestGenerateContextMetadataBudgetWinsOverDeadline): the first read is
// blocked until the caller cancels the ctx, so the canceled context is
// guaranteed to precede the config-scan outcome. The DecodeConfig tee is
// deliberately not ctx-wrapped, so an over-budget stream still trips the
// budget abort even under the canceled ctx; a cancel that lands after the
// scan completes surfaces at the post-config boundary check. The goroutine
// must not call t methods.
func runCancelLeg(t *testing.T, stream io.Reader) error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	blocked := make(chan struct{})
	proceed := make(chan struct{})
	g := &gatedReader{r: stream, blocked: blocked, proceed: proceed}
	done := make(chan error, 1)
	go func() {
		_, err := GenerateContext(ctx, g, 64, 64)
		done <- err
	}()
	select {
	case <-blocked: // first read attempted: slot acquired, config scan started
	case <-time.After(5 * time.Second):
		t.Fatal("GenerateContext never started reading")
	}
	cancel()
	close(proceed)
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("GenerateContext did not return after cancel")
	}
	return nil // unreachable
}

// TestGenerateCanceledCtxOversizedDims is the C1 deterministic pin: the ctx
// fast-fail at slot acquisition PRECEDES the dimension pre-check, so a
// canceled request carrying oversized declared dims must classify as
// context.Canceled — never ErrImageTooLarge. A reorder of the two gates
// (dims check before the ctx check) would silently flip the wire class for
// dead requests and fail this test.
func TestGenerateCanceledCtxOversizedDims(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// 100000² declared dims: would be ErrImageTooLarge on a live request.
	bomb := headerOnlyPNG(t, 100000, 100000, 8, 6)
	if _, err := GenerateContext(ctx, bytes.NewReader(bomb), 100, 100); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled ctx + oversized dims: err = %v, want context.Canceled (ctx fast-fail precedes the dims gate)", err)
	}
	// Live control: the same fixture on a live ctx is the dims class.
	if _, err := Generate(bytes.NewReader(bomb), 100, 100); !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("live ctx + oversized dims: err = %v, want ErrImageTooLarge", err)
	}
	// The fuzz-shaped seed (budget target, canceled variant) must also
	// classify Canceled, not the dims sentinel: the same ordering pin at the
	// fuzz-target boundary.
	if _, err := GenerateContext(ctx, bytes.NewReader(headerOnlyPNG(t, 8192, 8192, 16, 6)), 100, 100); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled ctx + depth-16 at 8192: err = %v, want context.Canceled", err)
	}
}
