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
	wrappedCanceled := fmt.Errorf("wrapped: %w", context.Canceled)
	wrappedDeadline := fmt.Errorf("wrapped: %w", context.DeadlineExceeded)
	for _, tc := range []struct {
		name  string
		input stubReadResult
	}{
		{name: "partial EOF", input: stubReadResult{n: 2, err: io.EOF}},
		{name: "partial canceled", input: stubReadResult{n: 3, err: wrappedCanceled}},
		{name: "partial deadline", input: stubReadResult{n: 4, err: wrappedDeadline}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			marker := &sourceReadMarker{r: &stubReader{results: []stubReadResult{tc.input}}}
			n, got := marker.Read(make([]byte, 8))
			if n != tc.input.n {
				t.Fatalf("n = %d, want %d", n, tc.input.n)
			}
			if got != tc.input.err {
				t.Fatalf("error = %v, want original error %v", got, tc.input.err)
			}
			if marker.firstSourceError() != nil {
				t.Fatalf("retained error = %v, want nil", marker.firstSourceError())
			}
		})
	}
}
