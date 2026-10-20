package rest

import (
	"net/http"
	"strings"
)

func (h *Handler) getKey(w http.ResponseWriter, r *http.Request) {
	representation, err := fileRepresentation(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	switch representation {
	case "content":
		h.Get(w, r)
	case "thumbnail":
		h.serveFileThumbnail(w, r, keyFromPath(r), r.URL.Query().Get("version"))
	default:
		h.getLegacyFile(w, r)
	}
}

// A non-empty version pin identifies an exact object, including keys that end
// in subresource names. Legacy thumbnail keeps its exact-key/derivation rules.
func (h *Handler) getLegacyFile(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("version") != "" && !strings.HasSuffix(r.URL.Path, "/thumbnail") {
		h.Get(w, r)
		return
	}
	switch {
	case strings.HasSuffix(r.URL.Path, "/tags"):
		h.getFileSubresource(w, r, h.GetTags)
	case strings.HasSuffix(r.URL.Path, "/versions"):
		h.getFileSubresource(w, r, h.ListVersions)
	case strings.HasSuffix(r.URL.Path, "/acl"):
		h.getFileSubresource(w, r, h.GetObjectACLHandler)
	case strings.HasSuffix(r.URL.Path, "/metadata"):
		h.getFileSubresource(w, r, h.GetMetadata)
	case strings.HasSuffix(r.URL.Path, "/thumbnail"):
		h.Thumbnail(w, r)
	default:
		h.Get(w, r)
	}
}

func (h *Handler) getFileSubresource(w http.ResponseWriter, r *http.Request, handle http.HandlerFunc) {
	if h.rejectAnonymousSubresource(w, r) {
		return
	}
	handle(w, r)
}
