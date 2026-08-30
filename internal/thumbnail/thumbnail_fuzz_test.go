package thumbnail

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"io"
	"testing"
)

// FuzzGenerate drives Generate with arbitrary mutations of the existing
// fixture shapes, asserting the package's documented contract: no panic;
// errors are always nil or one of the four sentinels; a nil error yields a
// decodable JPEG no larger than the requested bounds. The 64 KiB input cap
// bounds per-iteration work (a hang surfaces as a fuzz worker timeout); the
// module's MaxSourceBytes/MaxMetadataBytes budgets and the MaxSourceDim
// pre-check bound the decoder side regardless of mutation. ErrMetadataTooLarge
// and ErrSourceTooLarge are both unreachable under the 64 KiB cap (the 8 MiB
// metadata budget and the 128 MiB source cap are far above it) and are pinned
// by TestGenerateRejectsOversizedMetadata* / TestGenerateEndlessMetadata /
// TestGenerateSourceBytesBound; they stay in the accepted set so coverage
// becomes automatic if budgets change.
// The error-set assertion depends on jpeg.Encode's error being unreachable:
// both of its error sources (the dims guard at >= 1<<16 and the sink write
// path) are structurally impossible here — post-scale bounds are <= 64x64
// and the decoded source is <= MaxSourceDim < 1<<16, and a bytes.Buffer
// never fails. Re-open deliberately if the sink or the scale bounds change.
// Run: go test -fuzz=FuzzGenerate -fuzztime=60s ./internal/thumbnail/
func FuzzGenerate(f *testing.F) {
	// Fixture builders are typed testing.TB (REQ-8) so *testing.F and
	// *testing.T both work (F embeds common, not T).
	f.Add(headerOnlyPNG(f, 8, 8, 8, 6))                                  // ErrUnsupported: no IDAT
	f.Add(headerOnlyPNG(f, 100000, 100000, 8, 6))                        // ErrImageTooLarge: dims > MaxSourceDim (33 B)
	f.Add(headerOnlyPNG(f, Max16BitSourceDim+1, 1024, 16, 6))            // ErrImageTooLarge: depth-16 > Max16BitSourceDim
	f.Add(headerOnlyPNG(f, Max16BitSourceDim, Max16BitSourceDim, 16, 6)) // boundary: ErrUnsupported (no IDAT)
	f.Add(appnPaddedJPEG(f, 1<<16))                                      // APP1 flood, truncated at the input cap
	prefix := make([]byte, 64<<10)
	_, _ = io.ReadFull(&endlessAPP1Stream{}, prefix) // streaming-flood shape, finite prefix
	f.Add(prefix)
	png := makePNG(f, 400, 200) // 744 B: known-good decode + downscale seed (REQ-4)
	f.Add(png)
	f.Add(png[:len(png)/2])                         // mid-IDAT truncation
	f.Add(headerOnlyProgressiveJPEG(f, 8192, 8192)) // ErrImageTooLarge: progressive > MaxProgressiveSourceDim (~130 B)
	f.Add(realProgressiveJPEG(f, 8, 8))             // valid progressive decode seed (338 B)
	// EXIF-carrying seeds (qa F2): a real JPEG with a valid orientation-6
	// APP1 spliced before the scan, and the same payload after a SOF0 header
	// (the walker must stop at the SOF family and ignore it).
	if jpg := realBaselineJPEG(f, 16, 16, 82); len(jpg) > 2 {
		payload := exifPayload(6, binary.LittleEndian)
		var exifJPEG []byte
		{
			var b bytes.Buffer
			b.Write([]byte{0xFF, 0xD8})
			var seg [4]byte
			seg[0], seg[1] = 0xFF, 0xE1
			binary.BigEndian.PutUint16(seg[2:4], uint16(len(payload)+2))
			b.Write(seg[:])
			b.Write(payload)
			b.Write(jpg[2:])
			exifJPEG = b.Bytes()
		}
		f.Add(exifJPEG)                     // valid decode + orientation-6 applied
		f.Add(jpegWithPostSOFExif(payload)) // post-SOF APP1 must be ignored
	}

	// PNG eXIf seeds (direction: honor PNG eXIf-chunk EXIF orientation): an
	// oriented bare-layout PNG, the tolerated "Exif\x00\x00"+TIFF prefixed
	// deviation, and an eXIf placed after IDAT (must be ignored — orientation
	// 1; the walk terminates at IDAT).
	f.Add(orientedPNG(f, 16, 16, 6, false))
	f.Add(orientedPNG(f, 16, 16, 6, true))
	f.Add(spliceAfterIDAT(f, orientedPNG(f, 16, 16, 0, false), "eXIf", bareExifPayload(6, binary.LittleEndian)))

	// GIF seeds (direction: GIF decode-pipeline test coverage). Fixture
	// builders live in gif_test.go; makeGIF is the sniff-level helper (typed
	// testing.TB, so *testing.F works — F embeds common). Classes: known-good
	// decode, composite path (Opaque()==false → white flatten), first-frame
	// policy under mutation, and the header-only dims probes.
	f.Add(makeGIF(f))                      // 1×1 opaque GIF: known-good decode seed
	f.Add(makeTransparent1x1GIF(f))        // composite-path seed (transparent palette entry)
	f.Add(makeAnimatedGIF(f))              // 2-frame GIF: first-frame policy under mutation
	f.Add(headerOnlyGIF(f, 65535, 65535))  // ErrImageTooLarge: dims > MaxSourceDim (13 B)
	f.Add(headerOnlyGIF(f, 8192, 8192))    // boundary: ErrUnsupported (no image data)
	gifSeed := makeGIF(f)                  // single build, then slice (F4)
	f.Add(gifSeed[:len(gifSeed)/2])        // mid-stream truncation → ErrUnsupported
	f.Add(makeJPEG(f, 400, 200))           // YCbCr-kernel downscale seed: JPEG → *image.YCbCr → scaleYCbCr (ratio < 1)
	f.Add(makeTransparentGIF(f, 400, 200)) // Paletted-kernel downscale seed: GIF → *image.Paletted (transparent) + white composite

	f.Fuzz(func(t *testing.T, data []byte) {
		out, err := Generate(io.LimitReader(bytes.NewReader(data), 64<<10), 64, 64)
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
