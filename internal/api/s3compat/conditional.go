package s3compat

import (
	"net/http"
	"strings"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
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

// evalS3GetPreconditions evaluates RFC 7232 preconditions for a GET/HEAD and
// returns the HTTP status to short-circuit with (304 Not Modified or 412
// Precondition Failed), or 0 to proceed. Precedence per RFC 7232 §6: If-Match,
// then If-Unmodified-Since, then If-None-Match, then If-Modified-Since.
func evalS3GetPreconditions(r *http.Request, obj repository.Object) int {
	if im := r.Header.Get("If-Match"); im != "" {
		if !s3ETagListMatches(im, obj.ETag) {
			return http.StatusPreconditionFailed
		}
	} else if ius := r.Header.Get("If-Unmodified-Since"); ius != "" {
		if t, err := http.ParseTime(ius); err == nil && obj.UpdatedAt.Truncate(time.Second).After(t) {
			return http.StatusPreconditionFailed
		}
	}
	if inm := r.Header.Get("If-None-Match"); inm != "" {
		if s3ETagListMatches(inm, obj.ETag) {
			return http.StatusNotModified
		}
	} else if ims := r.Header.Get("If-Modified-Since"); ims != "" {
		if t, err := http.ParseTime(ims); err == nil && !obj.UpdatedAt.Truncate(time.Second).After(t) {
			return http.StatusNotModified
		}
	}
	return 0
}
