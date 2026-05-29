package service

import (
	"context"
	"io"
	"strconv"
	"strings"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// ParseByteRange parses a single HTTP Range header ("bytes=...") against the
// object size.
//
//   - ok=false, unsatisfiable=false → no usable range; serve the full object (200).
//   - unsatisfiable=true            → range lies outside the object; reply 416.
//   - ok=true                       → serve [offset, offset+length) as 206.
//
// Only the first range of a multi-range header is honoured (sufficient for the
// common video-seek / resumable-download cases).
func ParseByteRange(header string, size int64) (offset, length int64, ok, unsatisfiable bool) {
	const p = "bytes="
	if size < 0 || !strings.HasPrefix(header, p) {
		return 0, 0, false, false
	}
	spec := strings.TrimSpace(header[len(p):])
	if i := strings.IndexByte(spec, ','); i >= 0 {
		spec = strings.TrimSpace(spec[:i]) // first range only
	}
	dash := strings.IndexByte(spec, '-')
	if dash < 0 {
		return 0, 0, false, false
	}
	startStr := strings.TrimSpace(spec[:dash])
	endStr := strings.TrimSpace(spec[dash+1:])

	// Suffix form: bytes=-N → final N bytes.
	if startStr == "" {
		n, err := strconv.ParseInt(endStr, 10, 64)
		if err != nil || n <= 0 {
			return 0, 0, false, false
		}
		if size == 0 {
			return 0, 0, false, true
		}
		if n > size {
			n = size
		}
		return size - n, n, true, false
	}

	start, err := strconv.ParseInt(startStr, 10, 64)
	if err != nil || start < 0 {
		return 0, 0, false, false
	}
	if start >= size {
		return 0, 0, false, true // unsatisfiable
	}
	if endStr == "" {
		return start, size - start, true, false
	}
	end, err := strconv.ParseInt(endStr, 10, 64)
	if err != nil {
		return 0, 0, false, false
	}
	if end >= size {
		end = size - 1
	}
	if end < start {
		return 0, 0, false, true
	}
	return start, end - start + 1, true, false
}

// GetRange streams a byte range of an object. length<0 means "to the end".
// It works across every storage backend by slicing the (already-decrypted)
// stream from Get, so SSE-encrypted and remote objects are handled correctly.
func (s *FileService) GetRange(ctx context.Context, tenant, bucket, key string, offset, length int64) (io.ReadCloser, repository.Object, error) {
	rc, obj, err := s.Get(ctx, tenant, bucket, key)
	if err != nil {
		return nil, obj, err
	}
	if offset > 0 {
		if _, err := io.CopyN(io.Discard, rc, offset); err != nil {
			_ = rc.Close()
			return nil, obj, err
		}
	}
	if length < 0 {
		return rc, obj, nil
	}
	return &limitReadCloser{r: io.LimitReader(rc, length), c: rc}, obj, nil
}

// limitReadCloser bounds a reader to N bytes while still closing the source.
type limitReadCloser struct {
	r io.Reader
	c io.Closer
}

func (l *limitReadCloser) Read(p []byte) (int, error) { return l.r.Read(p) }
func (l *limitReadCloser) Close() error               { return l.c.Close() }
