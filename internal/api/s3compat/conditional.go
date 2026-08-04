package s3compat

import (
	"errors"
	"net/http"
	"strings"
	"time"

	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

func s3ETagEquals(token, etag string) bool {
	token = strings.TrimSpace(token)
	token = strings.TrimPrefix(token, "W/")
	token = strings.Trim(token, `"`)
	return token == etag
}

func s3ETagListMatches(header, etag string) bool {
	header = strings.TrimSpace(header)
	if header == "" {
		return false
	}
	if header == "*" {
		return true
	}
	for _, tok := range strings.Split(header, ",") {
		if s3ETagEquals(tok, etag) {
			return true
		}
	}
	return false
}

func hasS3GetConditional(r *http.Request) bool {
	h := r.Header
	return h.Get("If-Match") != "" || h.Get("If-None-Match") != "" ||
		h.Get("If-Modified-Since") != "" || h.Get("If-Unmodified-Since") != ""
}

func hasS3WriteConditional(r *http.Request) bool {
	return r.Header.Get("If-Match") != "" || r.Header.Get("If-None-Match") != ""
}

func (h *Handler) checkS3WritePreconditions(
	w http.ResponseWriter, r *http.Request, bucket, key string,
) bool {
	if !hasS3WriteConditional(r) {
		return true
	}
	obj, err := h.svc.Stat(
		r.Context(), mw.TenantFrom(r.Context()), bucket, key,
	)
	exists := err == nil
	if err != nil && !errors.Is(err, service.ErrNotFound) {
		writeS3Error(w, r, err)
		return false
	}
	if match := r.Header.Get("If-Match"); match != "" &&
		(!exists || !s3ETagListMatches(match, obj.ETag)) {
		writeS3Error(w, r, service.ErrPreconditionFailed)
		return false
	}
	if none := r.Header.Get("If-None-Match"); none != "" && exists &&
		s3ETagListMatches(none, obj.ETag) {
		writeS3Error(w, r, service.ErrPreconditionFailed)
		return false
	}
	return true
}

func evalIfMatch(etag, header string) bool {
	return s3ETagListMatches(header, etag)
}

func evalIfNoneMatch(etag, header string) bool {
	return !s3ETagListMatches(header, etag)
}

func evalIfModifiedSince(modTime time.Time, header string) bool {
	t, err := http.ParseTime(header)
	if err != nil {
		return true
	}
	return modTime.Truncate(time.Second).After(t)
}

func evalIfUnmodifiedSince(modTime time.Time, header string) bool {
	t, err := http.ParseTime(header)
	if err != nil {
		return true
	}
	return !modTime.Truncate(time.Second).After(t)
}

// evalS3GetPreconditions evaluates RFC 7232 preconditions for a GET/HEAD and
// returns the HTTP status to short-circuit with (304 Not Modified or 412
// Precondition Failed), or 0 to proceed. Precedence per RFC 7232 §6: If-Match,
// then If-Unmodified-Since, then If-None-Match, then If-Modified-Since.
func evalS3GetPreconditions(r *http.Request, obj repository.Object) int {
	if im := r.Header.Get("If-Match"); im != "" {
		if !evalIfMatch(obj.ETag, im) {
			return http.StatusPreconditionFailed
		}
	} else if ius := r.Header.Get("If-Unmodified-Since"); ius != "" {
		if !evalIfUnmodifiedSince(obj.UpdatedAt, ius) {
			return http.StatusPreconditionFailed
		}
	}
	if inm := r.Header.Get("If-None-Match"); inm != "" {
		if !evalIfNoneMatch(obj.ETag, inm) {
			return http.StatusNotModified
		}
	} else if ims := r.Header.Get("If-Modified-Since"); ims != "" {
		if !evalIfModifiedSince(obj.UpdatedAt, ims) {
			return http.StatusNotModified
		}
	}
	return 0
}
