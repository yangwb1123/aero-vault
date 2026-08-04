package rest

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/service"
)

// Presign handles POST /v1/files/*key/presign?op=get|put&expires=<seconds>.
func (h *Handler) Presign(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSuffix(keyFromPath(r), "/presign")
	op := r.URL.Query().Get("op")
	if op == "" {
		op = "get"
	}
	secs, _ := strconv.Atoi(r.URL.Query().Get("expires"))
	if secs <= 0 {
		secs = 300
	}
	expiry := time.Duration(secs) * time.Second
	action, err := presignPolicyAction(op)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	if !h.checkBucketPolicy(w, r, action) {
		return
	}
	signedURL, err := h.presignURL(r, key, op, expiry)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, presignResponse{
		URL:     signedURL,
		Expires: time.Now().Add(expiry),
	})
}

func (h *Handler) presignURL(
	r *http.Request,
	key, operation string,
	expiry time.Duration,
) (string, error) {
	tenant := mw.TenantFrom(r.Context())
	switch operation {
	case "get":
		return h.presignGetURL(r, tenant, key, expiry)
	case "put":
		return h.presignPutURL(r, tenant, key, expiry)
	default:
		return "", fmt.Errorf("%w: op must be get|put", service.ErrInvalidArgs)
	}
}

func presignPolicyAction(operation string) (string, error) {
	switch operation {
	case "get":
		return "s3:GetObject", nil
	case "put":
		return "s3:PutObject", nil
	default:
		return "", fmt.Errorf("%w: op must be get|put", service.ErrInvalidArgs)
	}
}

func (h *Handler) presignGetURL(
	r *http.Request,
	tenant, key string,
	expiry time.Duration,
) (string, error) {
	if h.putPresigner == nil {
		return h.svc.PresignGet(
			r.Context(), tenant, service.DefaultBucket, key, expiry,
		)
	}
	if err := h.svc.PreparePresignGet(
		r.Context(), tenant, service.DefaultBucket, key, expiry,
	); err != nil {
		return "", err
	}
	if tenant == "" {
		tenant = "default"
	}
	target, err := h.restObjectTarget(r, key)
	if err != nil {
		return "", fmt.Errorf("%w: %v", service.ErrInvalidArgs, err)
	}
	signedURL, err := h.putPresigner.SignGet(target, tenant, expiry)
	if err != nil {
		return "", fmt.Errorf("%w: %v", service.ErrInvalidArgs, err)
	}
	return signedURL, nil
}

func (h *Handler) presignPutURL(
	r *http.Request,
	tenant, key string,
	expiry time.Duration,
) (string, error) {
	if h.putPresigner == nil {
		return h.svc.PresignPut(
			r.Context(), tenant, service.DefaultBucket, key, expiry,
		)
	}
	if err := h.svc.PreparePresignPut(
		r.Context(), tenant, service.DefaultBucket, key, expiry,
	); err != nil {
		return "", err
	}
	if tenant == "" {
		tenant = "default"
	}
	target, err := h.restObjectTarget(r, key)
	if err != nil {
		return "", fmt.Errorf("%w: %v", service.ErrInvalidArgs, err)
	}
	signedURL, err := h.putPresigner.SignPut(target, tenant, expiry)
	if err != nil {
		return "", fmt.Errorf("%w: %v", service.ErrInvalidArgs, err)
	}
	return signedURL, nil
}

func (h *Handler) restObjectTarget(r *http.Request, key string) (string, error) {
	if h.publicBaseURL != "" {
		base, err := url.Parse(h.publicBaseURL)
		if err != nil || base.Scheme == "" || base.Host == "" {
			return "", fmt.Errorf("public base URL must be absolute")
		}
		base.Path = "/v1/files/" + key
		base.RawPath, base.RawQuery, base.Fragment = "", "", ""
		return base.String(), nil
	}
	if r.Host == "" {
		return "", fmt.Errorf("request host is required")
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	} else if forwarded := forwardedScheme(r.Header.Get("X-Forwarded-Proto")); forwarded != "" {
		scheme = forwarded
	}
	return (&url.URL{
		Scheme: scheme,
		Host:   r.Host,
		Path:   "/v1/files/" + key,
	}).String(), nil
}

func forwardedScheme(value string) string {
	value = strings.ToLower(strings.TrimSpace(strings.Split(value, ",")[0]))
	if value == "http" || value == "https" {
		return value
	}
	return ""
}
