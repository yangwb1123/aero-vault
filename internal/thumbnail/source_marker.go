package thumbnail

import (
	"context"
	"errors"
	"io"
)

// SourceReadError wraps an error returned by the caller-supplied source
// stream while reading or closing. It lets callers distinguish storage/
// verification failures (local FS I/O, S3/OSS/COS network errors, an on-read
// or close-time ETagVerifier checksum mismatch) from codec-synthesized decode
// errors. Decode-site context errors stay unwrapped; close-time context errors
// use this wrapper while preserving errors.Is.
type SourceReadError struct{ Err error }

func (e *SourceReadError) Error() string {
	return "thumbnail: reading image source stream: " + e.Err.Error()
}

func (e *SourceReadError) Unwrap() error { return e.Err }

// sourceReadMarker marks source errors and retains the first marked error seen
// during one generation. A decoder may consume all payload bytes while
// returning a non-EOF error alongside them; retaining the marker state lets
// generateLocked veto that otherwise successful decode without another read.
type sourceReadMarker struct {
	r        io.Reader
	firstErr error
}

func (m *sourceReadMarker) Read(p []byte) (int, error) {
	n, err := m.r.Read(p)
	if err == nil || errors.Is(err, io.EOF) {
		return n, err
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return n, err
	}
	if !hasSourceReadError(err) {
		err = &SourceReadError{Err: err}
	}
	if m.firstErr == nil {
		m.firstErr = err
	}
	return n, err
}

func (m *sourceReadMarker) firstSourceError() error {
	return m.firstErr
}

func hasSourceReadError(err error) bool {
	var sourceErr *SourceReadError
	return errors.As(err, &sourceErr)
}
