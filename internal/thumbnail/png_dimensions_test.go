package thumbnail

import (
	"bytes"
	"errors"
	"testing"
)

func TestPNGRawDimensionsRemainArchitectureIndependent(t *testing.T) {
	for _, tc := range []struct {
		name       string
		width      uint32
		height     uint32
		wantTooBig bool
	}{
		{name: "large pixel product", width: 100000, height: 100000, wantTooBig: true},
		{name: "width above signed int32", width: 0x80000000, height: 1, wantTooBig: true},
		{name: "height above signed int32", width: 1, height: 0x80000000, wantTooBig: true},
		{name: "exact boundary", width: MaxSourceDim, height: MaxSourceDim},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := headerOnlyPNG(t, tc.width, tc.height, 8, 6)
			_, err := Generate(bytes.NewReader(input), 100, 100)
			if tc.wantTooBig {
				if !errors.Is(err, ErrImageTooLarge) {
					t.Fatalf("err = %v, want ErrImageTooLarge", err)
				}
				return
			}
			if errors.Is(err, ErrImageTooLarge) {
				t.Fatalf("err = %v, exact boundary must pass the dimension gate", err)
			}
			if !errors.Is(err, ErrUnsupported) {
				t.Fatalf("err = %v, want ErrUnsupported for header without payload", err)
			}
		})
	}
}

func TestPNGRawDimensionsRequireAValidIHDR(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{
			name:   "truncated IHDR",
			mutate: func(input []byte) []byte { return input[:len(input)-1] },
		},
		{
			name: "invalid checksum",
			mutate: func(input []byte) []byte {
				input[len(input)-1] ^= 0xff
				return input
			},
		},
		{
			name: "invalid bit depth and color",
			mutate: func(input []byte) []byte {
				input[24], input[25] = 2, 6
				return input
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			input := tc.mutate(headerOnlyPNG(t, 100000, 100000, 8, 6))
			_, err := Generate(bytes.NewReader(input), 100, 100)
			if errors.Is(err, ErrImageTooLarge) {
				t.Fatalf("err = %v, malformed IHDR must not be classified as oversized", err)
			}
			if !errors.Is(err, ErrUnsupported) {
				t.Fatalf("err = %v, want ErrUnsupported", err)
			}
		})
	}
}
