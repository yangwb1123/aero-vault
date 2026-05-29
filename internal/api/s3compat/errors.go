package s3compat

import (
	"encoding/xml"
	"errors"
	"net/http"

	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

func writeS3Error(w http.ResponseWriter, r *http.Request, err error) {
	code, msg, status := classify(err)
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	body := s3Error{
		Code:      code,
		Message:   msg,
		Resource:  r.URL.Path,
		RequestID: mw.RequestIDFrom(r.Context()),
	}
	_, _ = w.Write([]byte(xml.Header))
	_ = xml.NewEncoder(w).Encode(body)
}

func classify(err error) (string, string, int) {
	switch {
	case errors.Is(err, service.ErrNotFound), errors.Is(err, repository.ErrNotFound):
		return "NoSuchKey", "The specified key does not exist.", http.StatusNotFound
	case errors.Is(err, service.ErrInvalidArgs):
		return "InvalidArgument", err.Error(), http.StatusBadRequest
	case errors.Is(err, service.ErrRangeNotSatisfiable):
		return "InvalidRange", "The requested range is not satisfiable", http.StatusRequestedRangeNotSatisfiable
	default:
		return "InternalError", err.Error(), http.StatusInternalServerError
	}
}
