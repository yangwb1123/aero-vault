package rest

import (
	"io"
	"net/http"

	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
)

// The caller defers reader.Close for early outcomes/panics. Body completion also
// checks the same cached close result, without closing the FileService source
// twice. All paths stay on that source, including If-Range and prefix discard.
func (h *Handler) serveOpenedContent(w http.ResponseWriter, r *http.Request, reader *objectContentReader, obj repository.Object, historical bool) {
	if h.serveContentRange(w, r, reader, obj, historical) {
		return
	}
	writeContentHeaders(w, obj, obj.Size, historical)
	copied, err := io.Copy(w, reader)
	h.finishContentCopy(r, reader, copied, obj.Size, err)
}

// Prefix discard fails before any content headers have been committed. Keep
// the error class/status but never expose a provider's raw stream error text.
func (h *Handler) writeContentSourceError(w http.ResponseWriter, r *http.Request, err error) {
	code, _, status := classify(err)
	writeJSON(w, status, errorBody{Error: errorPayload{
		Code: code, Message: "object content read failed", RequestID: mw.RequestIDFrom(r.Context()),
	}})
}

// Do not let a reported streaming failure end like a successful response, or
// append an error document to file bytes. net/http owns the abort protocol.
// Bytes already delivered (including a complete Content-Length) are not
// retractable; this is not a download-before-verification guarantee.
func (h *Handler) finishContentCopy(r *http.Request, source *objectContentReader, copied, expected int64, err error) {
	if r.Context().Err() != nil {
		return
	}
	reason := "stream_error"
	if err == nil {
		reason = "short_read"
		if copied == expected {
			err = source.Close()
			if err == nil || r.Context().Err() != nil {
				return
			}
			reason = "source_close"
		}
	}
	h.logger.WarnContext(r.Context(), "content response interrupted", "reason", reason,
		"request_id", mw.RequestIDFrom(r.Context()))
	panic(http.ErrAbortHandler)
}
