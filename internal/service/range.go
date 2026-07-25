package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/aero-vault/aero-vault/internal/repository"
)

var errUnsatisfiable = errors.New("unsatisfiable")

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

	start, end, err := parseRangeSpec(spec)
	if err != nil {
		return 0, 0, false, false
	}

	offset, length, err = clampRange(start, end, size)
	if errors.Is(err, errUnsatisfiable) {
		return 0, 0, false, true
	}
	return offset, length, true, false
}

// parseRangeSpec parses a single range spec ("start-end", "start-", or "-suffix").
// It returns (start, end, nil) where end < 0 means "to end" and start < 0 means
// suffix form (value is -N).
func parseRangeSpec(spec string) (start, end int64, err error) {
	dash := strings.IndexByte(spec, '-')
	if dash < 0 {
		return 0, 0, fmt.Errorf("no dash in range")
	}
	startStr := strings.TrimSpace(spec[:dash])
	endStr := strings.TrimSpace(spec[dash+1:])

	if startStr == "" {
		n, parseErr := strconv.ParseInt(endStr, 10, 64)
		if parseErr != nil || n <= 0 {
			return 0, 0, fmt.Errorf("invalid suffix range")
		}
		return -n, 0, nil
	}

	start, parseErr := strconv.ParseInt(startStr, 10, 64)
	if parseErr != nil || start < 0 {
		return 0, 0, fmt.Errorf("invalid start")
	}

	if endStr == "" {
		return start, -1, nil
	}

	end, parseErr = strconv.ParseInt(endStr, 10, 64)
	if parseErr != nil {
		return 0, 0, fmt.Errorf("invalid end")
	}
	return start, end, nil
}

// clampRange validates and clamps a parsed range against the object size.
// start < 0 indicates suffix form, end < 0 means "to end of object".
func clampRange(start, end, size int64) (offset, length int64, err error) {
	if start < 0 {
		n := -start
		if size == 0 {
			return 0, 0, errUnsatisfiable
		}
		if n > size {
			n = size
		}
		return size - n, n, nil
	}

	if start >= size {
		return 0, 0, errUnsatisfiable
	}

	if end < 0 {
		return start, size - start, nil
	}

	if end >= size {
		end = size - 1
	}
	if end < start {
		return 0, 0, errUnsatisfiable
	}
	return start, end - start + 1, nil
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
