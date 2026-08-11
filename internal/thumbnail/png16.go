package thumbnail

// Max16BitSourceDim caps each declared source dimension (pixels) for depth-16
// PNG sources, below MaxSourceDim. image/png decodes 16-bit sources (color
// types 2/4/6) to *image.RGBA64/*image.NRGBA64 at 8 B/px, so a depth-16
// source at MaxSourceDim allocates 512 MiB per decode — four concurrent
// decodes would breach the 2 GiB aggregate ceiling pinned in semaphore_test.go.
// Capping at 4096 bounds decode allocation to 4096²×8 B = 128 MiB and the
// aggregate to ≈ 4 × 128 MiB ≈ 512 MiB, back inside the ceiling. The cap is
// format-class (bit depth 16 regardless of color type), mirroring
// MaxProgressiveSourceDim: gray-only 16-bit sources (2 B/px) — and gray16
// sources carrying a tRNS chunk (which decode to *image.NRGBA64) — are
// conservatively over-rejected too. Declared here, not in the DefaultMax/
// HardMax const block in thumbnail.go, because that file sits at the 500-line
// hard gate (make check) and the depth-16 guard is a coherent unit with
// pngBitDepth. GenerateContext rejects a PNG whose DecodeConfig-reported
// dims exceed this cap on either side with ErrImageTooLarge before any pixel
// buffer is allocated.
const Max16BitSourceDim = 4096

// pngBitDepth returns the PNG IHDR bit-depth byte (file offset 24: 8
// signature + 4 chunk length + 4 "IHDR" + 4 width + 4 height; color type at
// 25) from a budget-capped head, or 8 when the buffer is shorter than 25
// bytes. A successful PNG DecodeConfig consumed exactly 33 bytes (signature +
// IHDR chunk including CRC), so head is ≥ 33 by construction; the guard is
// defensive and must never add a rejection.
func pngBitDepth(head []byte) uint8 {
	if len(head) < 25 {
		return 8
	}
	return head[24]
}
