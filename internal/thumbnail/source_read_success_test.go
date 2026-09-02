package thumbnail

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

// completePayloadErrorReader returns a complete image and a source failure in
// one Read. A later read is recorded so the regression test can ensure the
// retained error is inspected rather than discovered by draining the source.
type completePayloadErrorReader struct {
	payload []byte
	err     error
	reads   int
}

func (r *completePayloadErrorReader) Read(p []byte) (int, error) {
	r.reads++
	if r.reads > 1 {
		return 0, io.EOF
	}
	if len(p) < len(r.payload) {
		return 0, io.ErrShortBuffer
	}
	copy(p, r.payload)
	return len(r.payload), r.err
}

func TestGenerateRejectsCompletePayloadWithSourceError(t *testing.T) {
	injected := errors.New("source failed after producing the image")
	source := &completePayloadErrorReader{
		payload: makeJPEG(t, 8, 8),
		err:     injected,
	}

	img, err := GenerateContext(context.Background(), source, 32, 32)
	assertSourceReadFailure(t, img, err, injected)
	if source.reads != 1 {
		t.Fatalf("source reads = %d, want exactly one read", source.reads)
	}
}

func TestGenerateRejectsCompletePayloadSourceErrorForEveryCodec(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload func(testing.TB) []byte
	}{
		{name: "JPEG", payload: func(t testing.TB) []byte { return makeJPEG(t, 8, 8) }},
		{name: "PNG", payload: func(t testing.TB) []byte { return makePNG(t, 8, 8) }},
		{name: "GIF", payload: func(t testing.TB) []byte { return makeGIF(t) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			injected := errors.New("source failed after complete payload")
			source := &completePayloadErrorReader{payload: tc.payload(t), err: injected}
			img, err := GenerateContext(context.Background(), source, 32, 32)
			assertSourceReadFailure(t, img, err, injected)
		})
	}
}

// stagedPayloadErrorReader keeps the DecodeConfig prefix separate from the
// remaining complete JPEG. DecodeConfig succeeds after the first read; the
// payload decoder then receives the rest of the image and a source error in
// its one Read, exercising the post-Decode retained-error veto.
type stagedPayloadErrorReader struct {
	prefix []byte
	rest   []byte
	err    error
	reads  int
}

func (r *stagedPayloadErrorReader) Read(p []byte) (int, error) {
	r.reads++
	var data []byte
	if r.reads == 1 {
		data = r.prefix
	} else if r.reads == 2 {
		data = r.rest
	} else {
		return 0, io.EOF
	}
	if len(p) < len(data) {
		return 0, io.ErrShortBuffer
	}
	copy(p, data)
	if r.reads == 2 {
		return len(data), r.err
	}
	return len(data), nil
}

func TestGenerateRejectsCompletePayloadErrorAfterConfig(t *testing.T) {
	jpg := makeJPEG(t, 64, 64)
	sos := bytes.Index(jpg, []byte{0xff, 0xda})
	if sos < 0 || sos+4 > len(jpg) {
		t.Fatal("JPEG fixture has no valid SOS marker")
	}
	segmentLength := int(binary.BigEndian.Uint16(jpg[sos+2 : sos+4]))
	prefixLength := sos + 2 + segmentLength + 3
	if prefixLength >= len(jpg) {
		t.Fatal("JPEG fixture has no payload after the staged prefix")
	}
	injected := errors.New("source failed during payload read")
	source := &stagedPayloadErrorReader{
		prefix: jpg[:prefixLength], rest: jpg[prefixLength:], err: injected,
	}

	img, err := GenerateContext(context.Background(), source, 32, 32)
	assertSourceReadFailure(t, img, err, injected)
	if source.reads != 2 {
		t.Fatalf("source reads = %d, want two reads with no post-error drain", source.reads)
	}
}

func assertSourceReadFailure(t *testing.T, img []byte, err, injected error) {
	t.Helper()
	var sourceErr *SourceReadError
	if !errors.As(err, &sourceErr) {
		t.Fatalf("err = %v (%T), want *SourceReadError", err, err)
	}
	if sourceErr.Err != injected {
		t.Fatalf("wrapped error = %v, want exact injected error %v", sourceErr.Err, injected)
	}
	if !errors.Is(err, injected) {
		t.Fatalf("errors.Is(err, injected) = false")
	}
	if errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, must not be ErrUnsupported", err)
	}
	if len(img) != 0 {
		t.Fatalf("image length = %d, want zero", len(img))
	}
}
