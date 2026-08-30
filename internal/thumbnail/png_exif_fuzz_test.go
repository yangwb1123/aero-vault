package thumbnail

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

// FuzzPNGOrientation drives the bounded PNG eXIf chunk walk with arbitrary
// mutations: any input must yield either a 1..8 orientation or an error from
// the walk's contract set (budget / context sentinels / plain io errors —
// EOF/truncation surfaces raw and is deferred to Decode), never a panic.
// Each seed is split at a random point into head + r, exercising the seam.
// Run: go test -fuzz=FuzzPNGOrientation -fuzztime=60s ./internal/thumbnail/
func FuzzPNGOrientation(f *testing.F) {
	ihdr := makePNG(f, 64, 64)
	f.Add(ihdr[:33], ihdr[33:]) // clean split: no eXIf
	oriented := orientedPNG(f, 64, 64, 6, false)
	f.Add(oriented[:33], oriented[33:])                                               // eXIf wholly in r
	f.Add(oriented[:40], oriented[40:])                                               // mid-eXIf-header split
	f.Add(oriented[:48], oriented[48:])                                               // mid-eXIf-data split
	f.Add(oriented, []byte(nil))                                                      // whole stream in head
	f.Add(orientedPNG(f, 64, 64, 6, true)[:33], orientedPNG(f, 64, 64, 6, true)[33:]) // prefixed deviation
	after := spliceAfterIDAT(f, orientedPNG(f, 64, 64, 0, false), "eXIf", bareExifPayload(6, binary.LittleEndian))
	f.Add(after[:33], after[33:])                           // post-IDAT eXIf (ignored)
	f.Add([]byte("garbage"), []byte(nil))                   // not a PNG header
	f.Add(ihdr[:33], []byte{0x00, 0x01})                    // truncated chunk header
	f.Add(ihdr[:33], pngChunkHeader(f, "eXIf", 0x80000000)) // ≥2³¹ eXIf declared length: clean stop, Decode classifies (AC-1 branch)

	f.Fuzz(func(t *testing.T, head, tail []byte) {
		orient, err := pngOrientation(context.Background(), head, bytes.NewReader(tail))
		if err != nil {
			if !errors.Is(err, errMetadataBudgetExceeded) &&
				!errors.Is(err, context.DeadlineExceeded) &&
				!errors.Is(err, context.Canceled) &&
				!errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("unexpected error class: %v", err)
			}
			return
		}
		if orient < 1 || orient > 8 {
			t.Fatalf("pngOrientation = %d, want 1..8", orient)
		}
	})
}
