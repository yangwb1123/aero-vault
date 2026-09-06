package thumbnail

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
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
			name:   "truncated IHDR data",
			mutate: func(input []byte) []byte { return input[:20] },
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
				return recomputeIHDRCRC(input)
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

func recomputeIHDRCRC(input []byte) []byte {
	binary.BigEndian.PutUint32(input[29:33], crc32.Checksum(input[12:29], crc32.IEEETable))
	return input
}

func TestPNGIHDRDimensionsValidateStructure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func([]byte) []byte
		valid  bool
	}{
		{name: "valid raw dimensions", valid: true},
		{name: "truncated signature", mutate: func(input []byte) []byte { return input[:7] }},
		{name: "wrong signature", mutate: func(input []byte) []byte { input[0] ^= 1; return input }},
		{name: "wrong length", mutate: func(input []byte) []byte { input[11] = 12; return input }},
		{name: "wrong chunk type", mutate: func(input []byte) []byte { input[12] = 'J'; return input }},
		{name: "zero width", mutate: func(input []byte) []byte {
			binary.BigEndian.PutUint32(input[16:20], 0)
			return recomputeIHDRCRC(input)
		}},
		{name: "zero height", mutate: func(input []byte) []byte {
			binary.BigEndian.PutUint32(input[20:24], 0)
			return recomputeIHDRCRC(input)
		}},
		{name: "invalid bit depth and color", mutate: func(input []byte) []byte {
			input[24], input[25] = 2, 2
			return recomputeIHDRCRC(input)
		}},
		{name: "invalid compression", mutate: func(input []byte) []byte {
			input[26] = 1
			return recomputeIHDRCRC(input)
		}},
		{name: "invalid filter", mutate: func(input []byte) []byte {
			input[27] = 1
			return recomputeIHDRCRC(input)
		}},
		{name: "invalid interlace", mutate: func(input []byte) []byte {
			input[28] = 2
			return recomputeIHDRCRC(input)
		}},
		{name: "invalid checksum", mutate: func(input []byte) []byte { input[32] ^= 0xff; return input }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := headerOnlyPNG(t, 0x80000000, 1, 8, 6)
			if tc.mutate != nil {
				input = tc.mutate(input)
			}
			width, height, ok := pngIHDRDimensions(input)
			if ok != tc.valid {
				t.Fatalf("valid = %v, want %v (dimensions=%d×%d)", ok, tc.valid, width, height)
			}
			if tc.valid && (width != 0x80000000 || height != 1) {
				t.Fatalf("dimensions = %d×%d, want 2147483648×1", width, height)
			}
		})
	}
}

type singleReadTracker struct {
	data  []byte
	calls int
}

func (r *singleReadTracker) Read(p []byte) (int, error) {
	r.calls++
	if r.calls > 1 {
		return 0, errors.New("unexpected read past non-PNG signature")
	}
	return copy(p, r.data), nil
}

func TestPNGPreflightDoesNotReadPastNonPNGSignature(t *testing.T) {
	r := &singleReadTracker{data: []byte("not-png!")}
	prefix := peekPNGHeader(bufio.NewReader(r))
	if !bytes.Equal(prefix, r.data) {
		t.Fatalf("prefix = %q, want %q", prefix, r.data)
	}
	if r.calls != 1 {
		t.Fatalf("reader calls = %d, want 1", r.calls)
	}
}

type headerReadTracker struct {
	header []byte
	reads  int
	bytes  int
}

func (r *headerReadTracker) Read(p []byte) (int, error) {
	r.reads++
	if r.reads > 1 {
		return 0, errors.New("payload read")
	}
	n := copy(p, r.header)
	r.bytes += n
	return n, nil
}

func TestOversizedPNGPreflightReturnsBeforePayloadRead(t *testing.T) {
	r := &headerReadTracker{header: headerOnlyPNG(t, 0x80000000, 1, 8, 6)}
	out, err := Generate(r, 100, 100)
	if !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("err = %v, want ErrImageTooLarge", err)
	}
	if out != nil {
		t.Fatalf("oversized rejection returned %d image bytes", len(out))
	}
	if r.reads != 1 || r.bytes != pngIHDRPrefixLen {
		t.Fatalf("source reads/bytes = %d/%d, want 1/%d", r.reads, r.bytes, pngIHDRPrefixLen)
	}
}

type cancelAfterHeaderReader struct {
	header    []byte
	cancel    context.CancelFunc
	delivered bool
}

func (r *cancelAfterHeaderReader) Read(p []byte) (int, error) {
	if r.delivered {
		return 0, errors.New("unexpected read after cancellation")
	}
	r.delivered = true
	n := copy(p, r.header)
	r.cancel()
	return n, nil
}

func TestOversizedPNGCancellationAfterIHDRWins(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &cancelAfterHeaderReader{header: headerOnlyPNG(t, 0x80000000, 1, 8, 6), cancel: cancel}
	out, err := GenerateContext(ctx, r, 100, 100)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if errors.Is(err, ErrImageTooLarge) || errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, cancellation must take precedence", err)
	}
	if out != nil {
		t.Fatalf("canceled rejection returned %d image bytes", len(out))
	}
}
