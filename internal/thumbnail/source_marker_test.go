package thumbnail

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
)

func TestSourceReadMarkerRetainsFirstError(t *testing.T) {
	first := errors.New("first source failure")
	second := errors.New("second source failure")
	marker := &sourceReadMarker{r: &stubReader{results: []stubReadResult{
		{n: 2, err: first}, {n: 1, err: second},
	}}}

	_, gotFirst := marker.Read(make([]byte, 4))
	var firstMarked *SourceReadError
	if !errors.As(gotFirst, &firstMarked) || firstMarked.Err != first {
		t.Fatalf("first read error = %v, want SourceReadError wrapping first error", gotFirst)
	}
	if marker.firstSourceError() != gotFirst {
		t.Fatalf("retained error = %v, want first returned wrapper %v", marker.firstSourceError(), gotFirst)
	}

	_, gotSecond := marker.Read(make([]byte, 4))
	if gotSecond == nil || !errors.Is(gotSecond, second) {
		t.Fatalf("second read error = %v, want second source failure", gotSecond)
	}
	if marker.firstSourceError() != gotFirst {
		t.Fatalf("retained error replaced: got %v, want %v", marker.firstSourceError(), gotFirst)
	}
}

func TestSourceReadMarkerDoesNotDoubleWrap(t *testing.T) {
	existing := &SourceReadError{Err: errors.New("already marked")}
	marker := &sourceReadMarker{r: &stubReader{results: []stubReadResult{{err: existing}}}}

	_, got := marker.Read(make([]byte, 1))
	if got != existing {
		t.Fatalf("error = %v (%T), want the existing SourceReadError instance", got, got)
	}
	if marker.firstSourceError() != existing {
		t.Fatalf("retained error = %v, want existing SourceReadError", marker.firstSourceError())
	}
}

func TestSourceReadMarkerDoesNotRetainContextOrEOF(t *testing.T) {
	wrappedContext := fmt.Errorf("wrapped: %w", context.Canceled)
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "EOF", err: io.EOF},
		{name: "context", err: wrappedContext},
	} {
		t.Run(tc.name, func(t *testing.T) {
			marker := &sourceReadMarker{r: &stubReader{results: []stubReadResult{{err: tc.err}}}}
			_, got := marker.Read(make([]byte, 1))
			if got != tc.err {
				t.Fatalf("error = %v, want original error %v", got, tc.err)
			}
			if marker.firstSourceError() != nil {
				t.Fatalf("retained error = %v, want nil", marker.firstSourceError())
			}
		})
	}
}
