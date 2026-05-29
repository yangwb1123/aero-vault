// Package ai bundles the content-understanding pipeline: extract → chunk →
// embed → index. Each component is an interface so callers (the indexer
// worker, the search service) can swap in richer implementations later.
package ai

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"
)

// Extractor pulls plain text out of a stored object given its bytes and
// content-type. Returns ErrUnsupported when the type isn't handled.
type Extractor interface {
	Extract(ctx context.Context, contentType string, r io.Reader) (string, error)
}

var ErrUnsupported = errors.New("extractor: content type unsupported")

// DefaultExtractor handles text/*, application/json and application/xml by
// streaming bytes through verbatim, capped at maxBytes to keep memory bounded.
// Binary types (PDF, Word, images) are rejected — wire in Tika/Unstructured
// later by composing this with a remote extractor.
type DefaultExtractor struct {
	MaxBytes int64
}

func NewDefaultExtractor() *DefaultExtractor {
	return &DefaultExtractor{MaxBytes: 4 << 20} // 4MB
}

func (e *DefaultExtractor) Extract(_ context.Context, contentType string, r io.Reader) (string, error) {
	ct := strings.ToLower(contentType)
	switch {
	case strings.HasPrefix(ct, "text/"),
		strings.HasPrefix(ct, "application/json"),
		strings.HasPrefix(ct, "application/xml"),
		strings.HasPrefix(ct, "application/yaml"),
		strings.HasPrefix(ct, "application/x-yaml"),
		strings.Contains(ct, "+xml"),
		strings.Contains(ct, "+json"),
		ct == "":
	default:
		return "", ErrUnsupported
	}
	limit := e.MaxBytes
	if limit <= 0 {
		limit = 4 << 20
	}
	lr := io.LimitReader(r, limit)
	var b strings.Builder
	br := bufio.NewReader(lr)
	if _, err := io.Copy(&strBuilderWriter{b: &b}, br); err != nil {
		return "", err
	}
	return b.String(), nil
}

type strBuilderWriter struct{ b *strings.Builder }

func (w *strBuilderWriter) Write(p []byte) (int, error) {
	return w.b.Write(p)
}
