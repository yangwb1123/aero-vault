package s3compat

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

func legalHoldFromHeader(header http.Header) (bool, error) {
	switch strings.ToUpper(strings.TrimSpace(header.Get("x-amz-object-lock-legal-hold"))) {
	case "", "OFF":
		return false, nil
	case "ON":
		return true, nil
	default:
		return false, fmt.Errorf("%w: invalid legal hold status", service.ErrInvalidArgs)
	}
}

func (h *Handler) getObjectLegalHold(w http.ResponseWriter, r *http.Request, bucket, key string) {
	tenant := mw.TenantFrom(r.Context())
	versionID := r.URL.Query().Get("versionId")
	if _, err := h.svc.GetObjectRetention(r.Context(), tenant, bucket, key, versionID); err != nil {
		writeS3Error(w, r, err)
		return
	}
	status := "OFF"
	if _, err := h.svc.GetLegalHold(r.Context(), tenant, bucket, key, versionID); err == nil {
		status = "ON"
	} else if !errors.Is(err, service.ErrNotFound) && !errors.Is(err, repository.ErrNotFound) {
		writeS3Error(w, r, err)
		return
	}
	writeXML(w, http.StatusOK, objectLegalHold{Xmlns: s3Namespace, Status: status})
}

func (h *Handler) putObjectLegalHold(w http.ResponseWriter, r *http.Request, bucket, key string) {
	tenant := mw.TenantFrom(r.Context())
	versionID := r.URL.Query().Get("versionId")
	status := r.Header.Get("x-amz-object-lock-legal-hold")
	if status == "" {
		var input objectLegalHold
		if err := decodeXMLBody(r.Body, DefaultXMLMaxBytes, &input); err != nil {
			writeS3Error(w, r, errMalformedXML)
			return
		}
		status = input.Status
	}
	var err error
	switch {
	case strings.EqualFold(status, "ON"):
		err = h.svc.PutLegalHold(r.Context(), tenant, bucket, key, versionID, "s3 api", tenant)
	case strings.EqualFold(status, "OFF"):
		err = h.svc.RemoveLegalHold(r.Context(), tenant, bucket, key, versionID)
	default:
		err = service.ErrInvalidArgs
	}
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) getObjectRetention(w http.ResponseWriter, r *http.Request, bucket, key string) {
	obj, err := h.svc.GetObjectRetention(
		r.Context(), mw.TenantFrom(r.Context()), bucket, key, r.URL.Query().Get("versionId"),
	)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	retainUntil := ""
	if obj.LockedUntil != nil {
		retainUntil = obj.LockedUntil.UTC().Format(time.RFC3339Nano)
	}
	writeXML(w, http.StatusOK, objectRetention{
		Xmlns: s3Namespace, Mode: service.ObjectRetentionMode(obj), RetainUntilDate: retainUntil,
	})
}

func (h *Handler) putObjectRetention(w http.ResponseWriter, r *http.Request, bucket, key string) {
	var input objectRetention
	if err := decodeXMLBody(r.Body, DefaultXMLMaxBytes, &input); err != nil {
		writeS3Error(w, r, errMalformedXML)
		return
	}
	until, err := time.Parse(time.RFC3339Nano, input.RetainUntilDate)
	if err != nil {
		writeS3Error(w, r, service.ErrInvalidArgs)
		return
	}
	_, err = h.svc.SetObjectRetention(
		r.Context(), mw.TenantFrom(r.Context()), bucket, key,
		r.URL.Query().Get("versionId"), input.Mode, until,
	)
	if err != nil {
		writeS3Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}
