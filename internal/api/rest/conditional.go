package rest

import (
	"net/http"
	"strings"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

type parsedETag struct {
	weak  bool
	value string
}

func parseEntityTagList(header string) ([]parsedETag, bool) {
	pos := 0
	pos = skipOWS(header, pos)
	if pos == len(header) {
		return nil, false
	}
	if header[pos] == '*' {
		pos++
		return nil, skipOWS(header, pos) == len(header)
	}
	var tags []parsedETag
	for {
		tag, next, ok := parseEntityTag(header, pos)
		if !ok {
			return nil, false
		}
		tags = append(tags, tag)
		pos = skipOWS(header, next)
		if pos == len(header) {
			return tags, true
		}
		if header[pos] != ',' {
			return nil, false
		}
		pos = skipOWS(header, pos+1)
		if pos == len(header) || header[pos] == '*' {
			return nil, false
		}
	}
}

func parseEntityTag(value string, pos int) (parsedETag, int, bool) {
	pos = skipOWS(value, pos)
	weak := false
	if pos+2 <= len(value) && value[pos] == 'W' && value[pos+1] == '/' {
		weak = true
		pos += 2
	}
	if pos >= len(value) || value[pos] != '"' {
		return parsedETag{}, pos, false
	}
	pos++
	start := pos
	for pos < len(value) {
		c := value[pos]
		if c == '"' {
			return parsedETag{weak: weak, value: value[start:pos]}, pos + 1, true
		}
		if !validETagByte(c) {
			return parsedETag{}, pos, false
		}
		pos++
	}
	return parsedETag{}, pos, false
}

func validETagByte(c byte) bool {
	return c == 0x21 || c >= 0x80 || (c >= 0x23 && c <= 0x7e)
}

func skipOWS(value string, pos int) int {
	for pos < len(value) && (value[pos] == ' ' || value[pos] == '\t') {
		pos++
	}
	return pos
}

// etagEquals retains the historical weak-comparison helper for callers that
// implement If-None-Match semantics.
func etagEquals(token, etag string) bool {
	return etagListMatches(token, etag)
}

// etagListMatches uses weak comparison, as required by If-None-Match.
func etagListMatches(header, etag string) bool {
	return etagListMatchesMode(header, etag, false)
}

func etagListMatchesStrong(header, etag string) bool {
	return etagListMatchesMode(header, etag, true)
}

func etagListMatchesMode(header, etag string, strong bool) bool {
	if strings.Trim(header, " \t") == "*" {
		return true
	}
	tags, valid := parseEntityTagList(header)
	if !valid {
		return false
	}
	current, currentStrong := currentETagValue(etag)
	if !currentStrong && strong {
		return false
	}
	for _, tag := range tags {
		if (strong && tag.weak) || tag.value != current {
			continue
		}
		return true
	}
	return false
}

func currentETagValue(etag string) (string, bool) {
	etag = strings.Trim(etag, " \t")
	if strings.HasPrefix(etag, "W/") {
		parsed, next, ok := parseEntityTag(etag, 2)
		if !ok || skipOWS(etag, next) != len(etag) {
			return "", false
		}
		return parsed.value, false
	}
	if strings.HasPrefix(etag, `"`) {
		parsed, next, ok := parseEntityTag(etag, 0)
		return parsed.value, ok && skipOWS(etag, next) == len(etag)
	}
	if etag == "" {
		return "", false
	}
	for i := 0; i < len(etag); i++ {
		if !validETagByte(etag[i]) {
			return "", false
		}
	}
	return etag, true
}

// notModified evaluates a conditional GET/HEAD. If-None-Match takes
// precedence over If-Modified-Since.
func notModified(r *http.Request, obj repository.Object) bool {
	if inm := r.Header.Get("If-None-Match"); inm != "" {
		return etagListMatches(inm, obj.ETag)
	}
	return dateNotModified(r.Header.Get("If-Modified-Since"), obj.UpdatedAt)
}

// readPreconditionFailed evaluates raw-object GET/HEAD preconditions.
func readPreconditionFailed(r *http.Request, obj repository.Object) bool {
	return readPreconditionFailedForETag(r, obj, obj.ETag)
}

func readPreconditionFailedForETag(r *http.Request, obj repository.Object, etag string) bool {
	if match := r.Header.Get("If-Match"); match != "" {
		return !etagListMatchesStrong(match, etag)
	}
	if unmodified := r.Header.Get("If-Unmodified-Since"); unmodified != "" {
		if t, err := http.ParseTime(unmodified); err == nil {
			return obj.UpdatedAt.Truncate(time.Second).After(t)
		}
	}
	return false
}

func dateNotModified(value string, updated time.Time) bool {
	if value == "" {
		return false
	}
	t, err := http.ParseTime(value)
	return err == nil && !updated.Truncate(time.Second).After(t)
}

func hasConditional(r *http.Request) bool {
	h := r.Header
	return h.Get("If-Match") != "" ||
		h.Get("If-Unmodified-Since") != "" ||
		h.Get("If-None-Match") != "" ||
		h.Get("If-Modified-Since") != "" ||
		h.Get("Range") != ""
}

// checkWritePreconditions enforces strong If-Match and weak If-None-Match.
func (h *Handler) checkWritePreconditions(w http.ResponseWriter, r *http.Request, cur repository.Object, exists bool) bool {
	if im := r.Header.Get("If-Match"); im != "" {
		if !exists || !etagListMatchesStrong(im, cur.ETag) {
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
