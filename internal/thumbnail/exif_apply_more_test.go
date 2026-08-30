package thumbnail

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
)

// TestApplyOrientationLiteralMatrices pins qa F4: orientations 2–8 use
// literal expected matrices (independent of orientIndex) on a 2×3 labeled
// source, so a regression in the index table itself cannot pass the pin.
// Source pixels are labeled 1..6 row-major (w=2, h=3); orientations 5–8
// swap the output axes to 3×2:
//
//	1 2      (o2) 2 1        (o3) 6 5        (o4) 5 6
//	3 4      =    4 3        =    4 3        =    3 4
//	5 6           6 5             2 1             1 2
//
//	(o5) 1 3 5   (o6) 5 3 1   (o7) 6 4 2   (o8) 2 4 6
//	=    2 4 6   =    6 4 2   =    5 3 1   =    1 3 5
func TestApplyOrientationLiteralMatrices(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 2, 3)) // w=2, h=3
	for r := 0; r < 3; r++ {
		for c := 0; c < 2; c++ {
			v := uint8(r*2 + c + 1) // 1..6 row-major
			src.SetNRGBA(c, r, color.NRGBA{R: v, G: 0, B: 0, A: 255})
		}
	}
	type matrix [][]int
	rows := func(rs ...[]int) matrix { return rs }
	expect := map[int]struct {
		out matrix
	}{
		2: {rows([]int{2, 1}, []int{4, 3}, []int{6, 5})},
		3: {rows([]int{6, 5}, []int{4, 3}, []int{2, 1})},
		4: {rows([]int{5, 6}, []int{3, 4}, []int{1, 2})},
		5: {rows([]int{1, 3, 5}, []int{2, 4, 6})},
		6: {rows([]int{5, 3, 1}, []int{6, 4, 2})},
		7: {rows([]int{6, 4, 2}, []int{5, 3, 1})},
		8: {rows([]int{2, 4, 6}, []int{1, 3, 5})},
	}
	for o, want := range expect {
		t.Run("o"+string(rune('0'+o)), func(t *testing.T) {
			got, err := applyOrientation(context.Background(), src, o)
			if err != nil {
				t.Fatalf("orientation %d: %v", o, err)
			}
			if got.Bounds().Dx() != len(want.out[0]) || got.Bounds().Dy() != len(want.out) {
				t.Fatalf("o%d out dims %dx%d want %dx%d", o, got.Bounds().Dx(), got.Bounds().Dy(), len(want.out[0]), len(want.out))
			}
			for r, row := range want.out {
				for c, v := range row {
					gotV := got.At(c, r).(color.RGBA).R
					if gotV != uint8(v) {
						t.Fatalf("o%d at (%d,%d) = %d want %d", o, r, c, gotV, v)
					}
				}
			}
		})
	}
	// o1 identity: same dims, byte-identical buffer reference (no copy).
	if got, err := applyOrientation(context.Background(), src, 1); err != nil {
		t.Fatalf("orientation 1: %v", err)
	} else if got != image.Image(src) {
		t.Fatal("o1 must return the source unchanged")
	}
	if got, err := applyOrientation(context.Background(), src, 0); err != nil {
		t.Fatalf("orientation 0: %v", err)
	} else if got != image.Image(src) {
		t.Fatal("out-of-range orientation must return the source unchanged")
	}
}

// TestGenerateOrientationRatioGe1YCbCr pins qa F5: a real JPEG source (YCbCr)
// no larger than the requested box takes the ratio ≥ 1 path (scale returns the
// source unchanged) and still rotates per EXIF orientation 6 — the upright
// 75×100 output with the blue/red quadrants preserved.
func TestGenerateOrientationRatioGe1YCbCr(t *testing.T) {
	// 50×75 JPEG, right-half red / left-half blue (decodes to *image.YCbCr).
	img := image.NewRGBA(image.Rect(0, 0, 50, 75))
	for y := 0; y < 75; y++ {
		for x := 0; x < 50; x++ {
			if x < 25 {
				img.Set(x, y, color.RGBA{0, 0, 255, 255})
			} else {
				img.Set(x, y, color.RGBA{255, 0, 0, 255})
			}
		}
	}
	var jpg bytes.Buffer
	if err := jpeg.Encode(&jpg, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	// Splice the EXIF orientation-6 APP1 between SOI and the rest.
	body := jpg.Bytes()[2:] // past SOI
	var out bytes.Buffer
	out.Write([]byte{0xFF, 0xD8})
	payload := exifPayload(6, binary.LittleEndian)
	var seg [4]byte
	seg[0], seg[1] = 0xFF, 0xE1
	binary.BigEndian.PutUint16(seg[2:4], uint16(len(payload)+2))
	out.Write(seg[:])
	out.Write(payload)
	out.Write(body)

	// Box 256×256 ≥ source 50×75: ratio ≥ 1 → scale returns src unchanged,
	// then orientation 6 rotates 90° CW → 75×50... wait, orientation 5–8
	// swap the box: box becomes 256×256 for 50×75? No — boxW,boxH = maxH,maxW
	// for o≥5 → (256,256). scale(50×75 → 256×256) is ratio < 1 in one axis?
	// 50→256 is upscale (ratio ≥ 1 → unchanged), 75→256 likewise. Both axes
	// ratio ≥ 1 → source returned unchanged (50×75 YCbCr). Then o6 rotates
	// to 75×50, upright (blue top, red bottom).
	got, err := Generate(bytes.NewReader(out.Bytes()), 256, 256)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	dec, format, err := image.Decode(bytes.NewReader(got))
	if err != nil || format != "jpeg" {
		t.Fatalf("decode: %v fmt=%q", err, format)
	}
	if dec.Bounds().Dx() != 75 || dec.Bounds().Dy() != 50 {
		t.Fatalf("out dims %dx%d want 75x50 (rotated upright)", dec.Bounds().Dx(), dec.Bounds().Dy())
	}
	// After 90° CW: the source's left (blue) half becomes the top.
	top := dec.At(37, 5)
	bot := dec.At(37, 45)
	tr, _, tb, _ := top.RGBA()
	br, _, bb, _ := bot.RGBA()
	if tb <= tr || bb >= br {
		t.Fatalf("top=(%d,%d) bottom=(%d,%d): want top blue-dominant, bottom red-dominant", tr, tb, br, bb)
	}
}
