package rest

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

// Keep the already-authorized object stream context-bound, including prefix
// discard where no ResponseWriter write can detect a client disconnect. An
// in-progress source Read may finish; its bytes are not delivered after cancel.
// One request owns this reader; it is not a concurrent Read/Close wrapper.
// Cache Close's result so body completion and deferred cleanup share one close.
type objectContentReader struct {
	ctx      context.Context
	source   io.Reader
	closed   bool
	closeErr error
}

func (r *objectContentReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.source.Read(p)
	if cancelled := r.ctx.Err(); cancelled != nil {
		return 0, cancelled
	}
	// CopyN treats enough bytes plus an error as success. Do not hand it
	// bytes from a Read that also reported failure; normal EOF data is valid.
	if err != nil && err != io.EOF {
		return 0, err
	}
	return n, err
}

func (r *objectContentReader) Close() error {
	if !r.closed {
		r.closed = true
		if closer, ok := r.source.(io.Closer); ok {
			r.closeErr = closer.Close()
		}
	}
	return r.closeErr
}

// Slice the already-opened, authorized source using its returned metadata.
// Never reopen through GetRange or use an earlier Stat's size/validators.
func (h *Handler) serveContentRange(w http.ResponseWriter, r *http.Request, rc *objectContentReader, obj repository.Object, historical bool) bool {
	if !contentRangeAllowed(r, obj) {
		return false
	}
	offset, length, ok, unsatisfiable := service.ParseByteRange(singleReadCondition(r, "Range"), obj.Size)
	if unsatisfiable {
		w.Header().Set("Content-Range", "bytes */"+strconv.FormatInt(obj.Size, 10))
		h.writeError(w, r, service.ErrRangeNotSatisfiable)
		return true
	}
	if !ok {
		return false
	}
	if offset > 0 {
		if _, err := io.CopyN(io.Discard, rc, offset); err != nil {
			h.writeContentSourceError(w, r, err)
			return true
		}
	}
	writeContentHeaders(w, obj, length, historical)
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, offset+length-1, obj.Size))
	w.WriteHeader(http.StatusPartialContent)
	copied, err := io.CopyN(w, rc, length)
	h.finishContentCopy(r, rc, copied, length, err)
	return true
}

func contentRangeAllowed(r *http.Request, obj repository.Object) bool {
	fields := r.Header.Values("If-Range")
	if len(fields) == 0 {
		return true
	}
	if len(fields) != 1 {
		return false
	}
	value := fields[0]
	if date, err := http.ParseTime(value); err == nil {
		return obj.UpdatedAt.Truncate(time.Second).Equal(date)
	}
	tags, valid := parseEntityTagList(value)
	return valid && len(tags) == 1 && !tags[0].weak && etagListMatchesStrong(value, obj.ETag)
}
