package rest

import (
	"net/http"
	"strings"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

// etagEquals compares a client-supplied entity-tag token (possibly weak `W/` or
// quoted) against the object's strong ETag value.
func etagEquals(token, etag string) bool {
	token = strings.TrimSpace(token)
	token = strings.TrimPrefix(token, "W/")
	token = strings.Trim(token, `"`)
	return token == etag
}

// etagListMatches reports whether an If-Match / If-None-Match header value
// matches the ETag. "*" matches any existing entity; otherwise the
// comma-separated tokens are checked.
func etagListMatches(header, etag string) bool {
	header = strings.TrimSpace(header)
	if header == "" {
		return false
	}
	if header == "*" {
		return true
	}
	for _, tok := range strings.Split(header, ",") {
		if etagEquals(tok, etag) {
			return true
		}
	}
	return false
}

// notModified evaluates a conditional GET/HEAD. Per RFC 7232, If-None-Match
// takes precedence over If-Modified-Since. Returns true when the client's
// cached copy is still fresh (=> 304 Not Modified).
func notModified(r *http.Request, obj repository.Object) bool {
	if inm := r.Header.Get("If-None-Match"); inm != "" {
		return etagListMatches(inm, obj.ETag)
	}
	if ims := r.Header.Get("If-Modified-Since"); ims != "" {
		if t, err := http.ParseTime(ims); err == nil {
			return !obj.UpdatedAt.Truncate(time.Second).After(t)
		}
	}
	return false
}

// hasConditional reports whether the request carries any precondition or range
// header that requires a Stat before serving.
func hasConditional(r *http.Request) bool {
	h := r.Header
	return h.Get("If-None-Match") != "" || h.Get("If-Modified-Since") != "" || h.Get("Range") != ""
}

// checkWritePreconditions enforces If-Match / If-None-Match on writes (optimistic
// concurrency / create-only). Returns false (and writes a 412) when a
// precondition fails. cur is the current object; exists indicates presence.
func (h *Handler) checkWritePreconditions(w http.ResponseWriter, r *http.Request, cur repository.Object, exists bool) bool {
	if im := r.Header.Get("If-Match"); im != "" {
		if !exists || !etagListMatches(im, cur.ETag) {
			h.writeError(w, r, service.ErrPreconditionFailed)
			return false
		}
	}
	if inm := r.Header.Get("If-None-Match"); inm != "" {
		if inm == "*" {
			if exists {
				h.writeError(w, r, service.ErrPreconditionFailed)
				return false
			}
		} else if exists && etagListMatches(inm, cur.ETag) {
			h.writeError(w, r, service.ErrPreconditionFailed)
			return false
		}
	}
	return true
}
