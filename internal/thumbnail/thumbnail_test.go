package thumbnail

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"io"
	"testing"
)

func makePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x % 256), uint8(y % 256), 128, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// headerOnlyPNG builds a PNG containing only the signature + IHDR chunk
// (valid CRC) — no IDAT/IEND — so no pixel buffer is ever allocated. The
// declared dimensions are attacker-controlled.
func headerOnlyPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.Write([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}) // signature
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], uint32(w))
	binary.BigEndian.PutUint32(ihdr[4:8], uint32(h))
	// depth 8, color type 6 (RGBA), compression 0, filter 0, interlace 0.
	ihdr[8], ihdr[9], ihdr[10], ihdr[11], ihdr[12] = 8, 6, 0, 0, 0
	var chunk bytes.Buffer
	chunk.WriteString("IHDR")
	chunk.Write(ihdr)
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], 13)
	buf.Write(l[:])
	buf.Write(chunk.Bytes())
	crc := crc32.NewIEEE()
	_, _ = crc.Write(chunk.Bytes())
	binary.BigEndian.PutUint32(l[:], crc.Sum32())
	buf.Write(l[:])
	return buf.Bytes()
}

func TestGenerateRejectsOversizedImage(t *testing.T) {
	bomb := headerOnlyPNG(t, 100000, 100000) // 10¹⁰ declared pixels
	if len(bomb) > 60 {
		t.Fatalf("bomb fixture unexpectedly large: %d bytes", len(bomb))
	}
	if _, err := Generate(bytes.NewReader(bomb), 100, 100); !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("expected ErrImageTooLarge, got %v", err)
	}
	// Dimension-driven, not format-driven: a header-only PNG has no IDAT, so
	// the in-cap 8×8 case must fail as unsupported, NOT as too large — and a
	// complete in-cap PNG must decode cleanly.
	if _, err := Generate(bytes.NewReader(headerOnlyPNG(t, 8, 8)), 100, 100); errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("in-cap dims must not trigger ErrImageTooLarge")
	}
	if _, err := Generate(bytes.NewReader(makePNG(t, 8, 8)), 100, 100); err != nil {
		t.Fatalf("in-cap image must decode: %v", err)
	}
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// endlessZeros never returns EOF: an unbounded caller-side stream.
type endlessZeros struct{}

func (endlessZeros) Read(p []byte) (int, error) { return len(p), nil }

func TestGenerateTruncatedInput(t *testing.T) {
	// (a) Valid header, payload truncated mid-IDAT → ErrUnsupported.
	png := makePNG(t, 100, 100)
	if _, err := Generate(io.LimitReader(bytes.NewReader(png), int64(len(png)/2)), 100, 100); err != ErrUnsupported {
		t.Fatalf("truncated input: expected ErrUnsupported, got %v", err)
	}
	// (b) Endless stream, externally capped: prompt ErrUnsupported, bounded reads.
	cnt := &countingReader{r: endlessZeros{}}
	if _, err := Generate(io.LimitReader(cnt, 4096), 100, 100); err != ErrUnsupported {
		t.Fatalf("endless input: expected ErrUnsupported, got %v", err)
	}
	if cnt.n > 4096 {
		t.Fatalf("read %d bytes, want ≤ 4096", cnt.n)
	}
}

func TestGenerateDownscalesPreservingAspect(t *testing.T) {
	src := makePNG(t, 400, 200) // 2:1
	out, err := Generate(bytes.NewReader(src), 100, 100)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	img, format, err := image.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode thumbnail: %v", err)
	}
	if format != "jpeg" {
		t.Fatalf("expected jpeg, got %s", format)
	}
	b := img.Bounds()
	// 400x200 capped to 100x100 box, aspect preserved → 100x50.
	if b.Dx() != 100 || b.Dy() != 50 {
		t.Fatalf("expected 100x50, got %dx%d", b.Dx(), b.Dy())
	}
}

func TestGenerateNeverUpscales(t *testing.T) {
	src := makePNG(t, 50, 40)
	out, err := Generate(bytes.NewReader(src), 500, 500)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	img, _, _ := image.Decode(bytes.NewReader(out))
	if img.Bounds().Dx() != 50 || img.Bounds().Dy() != 40 {
		t.Fatalf("should not upscale: got %dx%d", img.Bounds().Dx(), img.Bounds().Dy())
	}
}

func TestGenerateDefaults(t *testing.T) {
	src := makePNG(t, 1000, 1000)
	out, err := Generate(bytes.NewReader(src), 0, 0) // default 256
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	img, _, _ := image.Decode(bytes.NewReader(out))
	if img.Bounds().Dx() != DefaultMax || img.Bounds().Dy() != DefaultMax {
		t.Fatalf("expected %dx%d, got %dx%d", DefaultMax, DefaultMax, img.Bounds().Dx(), img.Bounds().Dy())
	}
}

func TestGenerateRejectsNonImage(t *testing.T) {
	if _, err := Generate(bytes.NewReader([]byte("not an image")), 100, 100); err != ErrUnsupported {
		t.Fatalf("expected ErrUnsupported, got %v", err)
	}
}
