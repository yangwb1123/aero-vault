package thumbnail

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
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
