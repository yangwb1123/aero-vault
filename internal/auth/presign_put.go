package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	presignPutExpiresKey   = "x-aero-presign-expires"
	presignPutTenantKey    = "x-aero-presign-tenant"
	presignPutSignatureKey = "x-aero-presign-signature"
	presignOperationKey    = "x-aero-presign-operation"
	maxPutPresignTTL       = 7 * 24 * time.Hour
)

// PutPresigner creates capability URLs that terminate at REST object routes.
// Keeping transfers inside Aero Vault preserves authorization, tenant status,
// quota, versioning, integrity, event, and indexing invariants.
type PutPresigner struct {
	secret []byte
}

// NewPutPresigner returns a process-local signer when secret is empty. Cluster
// deployments should configure the same AUTH_PRESIGN_SECRET on every replica.
func NewPutPresigner(secret string) *PutPresigner {
	if secret != "" {
		return &PutPresigner{secret: []byte(secret)}
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(fmt.Sprintf("initialize presign secret: %v", err))
	}
	return &PutPresigner{secret: key}
}

// SignPut signs an absolute REST object URL for one tenant and expiry.
func (p *PutPresigner) SignPut(rawURL, tenant string, expiry time.Duration) (string, error) {
	return p.sign(rawURL, tenant, expiry, http.MethodPut, "")
}

// SignGet signs an absolute REST object URL for GET and HEAD use.
func (p *PutPresigner) SignGet(rawURL, tenant string, expiry time.Duration) (string, error) {
	return p.sign(rawURL, tenant, expiry, http.MethodGet, "get")
}

// VerifyPut validates a signed PUT request and returns the scoped caller.
func (p *PutPresigner) VerifyPut(r *http.Request) (Key, error) {
	if p == nil || len(p.secret) == 0 {
		return Key{}, errors.New("presigned PUT signer is not configured")
	}
	if r.Method != http.MethodPut {
		return Key{}, errors.New("presigned PUT URL requires PUT")
	}
	q := r.URL.Query()
	if err := validatePresignQuery(q, "put"); err != nil {
		return Key{}, err
	}
	tenant := q.Get(presignPutTenantKey)
	expires, err := strconv.ParseInt(q.Get(presignPutExpiresKey), 10, 64)
	if err != nil || tenant == "" {
		return Key{}, errors.New("invalid presigned PUT parameters")
	}
	now := time.Now().Unix()
	if expires < now || expires-now > int64(maxPutPresignTTL/time.Second) {
		return Key{}, errors.New("presigned PUT URL expired or exceeds maximum lifetime")
	}
	want := p.signatureFor(http.MethodPut, r.URL.EscapedPath(), tenant, expires)
	if !hmac.Equal([]byte(want), []byte(q.Get(presignPutSignatureKey))) {
		return Key{}, errors.New("presigned PUT signature mismatch")
	}
	return Key{
		Token:  "presigned-put",
		Tenant: tenant,
		Scopes: map[Scope]bool{ScopeWrite: true},
	}, nil
}

// VerifyGet validates a signed GET/HEAD request and returns the scoped caller.
func (p *PutPresigner) VerifyGet(r *http.Request) (Key, error) {
	if p == nil || len(p.secret) == 0 {
		return Key{}, errors.New("presigned GET signer is not configured")
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return Key{}, errors.New("presigned GET URL requires GET or HEAD")
	}
	q := r.URL.Query()
	if err := validatePresignQuery(q, "get"); err != nil {
		return Key{}, err
	}
	tenant := q.Get(presignPutTenantKey)
	expires, err := strconv.ParseInt(q.Get(presignPutExpiresKey), 10, 64)
	if err != nil || tenant == "" {
		return Key{}, errors.New("invalid presigned GET parameters")
	}
	now := time.Now().Unix()
	if expires < now || expires-now > int64(maxPutPresignTTL/time.Second) {
		return Key{}, errors.New("presigned GET URL expired or exceeds maximum lifetime")
	}
	want := p.signatureFor(http.MethodGet, r.URL.EscapedPath(), tenant, expires)
	if !hmac.Equal([]byte(want), []byte(q.Get(presignPutSignatureKey))) {
		return Key{}, errors.New("presigned GET signature mismatch")
	}
	return Key{
		Token:  "presigned-get",
		Tenant: tenant,
		Scopes: map[Scope]bool{ScopeRead: true},
	}, nil
}

// IsPresignedPut reports whether a request presents an Aero PUT capability.
func IsPresignedPut(r *http.Request) bool {
	operation := strings.TrimSpace(r.URL.Query().Get(presignOperationKey))
	return r.Method == http.MethodPut && operation != "get" &&
		strings.TrimSpace(r.URL.Query().Get(presignPutSignatureKey)) != ""
}

// IsPresignedGet reports whether a request presents an Aero GET capability.
func IsPresignedGet(r *http.Request) bool {
	operation := strings.TrimSpace(r.URL.Query().Get(presignOperationKey))
	return (r.Method == http.MethodGet || r.Method == http.MethodHead) && operation == "get" &&
		strings.TrimSpace(r.URL.Query().Get(presignPutSignatureKey)) != ""
}

func (p *PutPresigner) signature(path, tenant string, expires int64) string {
	return p.signatureFor(http.MethodPut, path, tenant, expires)
}

func (p *PutPresigner) sign(
	rawURL, tenant string,
	expiry time.Duration,
	method, operation string,
) (string, error) {
	if p == nil || len(p.secret) == 0 {
		return "", errors.New("presigned REST signer is not configured")
	}
	if tenant == "" {
		return "", errors.New("presigned REST tenant is required")
	}
	if expiry <= 0 || expiry > maxPutPresignTTL {
		return "", errors.New("presigned REST expiry must be between 1 second and 7 days")
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" || u.RawQuery != "" {
		return "", errors.New("presigned REST target must be an absolute URL without query parameters")
	}
	expires := time.Now().Add(expiry).Unix()
	q := u.Query()
	if operation != "" {
		q.Set(presignOperationKey, operation)
	}
	q.Set(presignPutExpiresKey, strconv.FormatInt(expires, 10))
	q.Set(presignPutTenantKey, tenant)
	q.Set(presignPutSignatureKey, p.signatureFor(method, u.EscapedPath(), tenant, expires))
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func validatePresignQuery(q url.Values, operation string) error {
	allowed := map[string]bool{
		presignPutExpiresKey: true, presignPutTenantKey: true,
		presignPutSignatureKey: true, presignOperationKey: true,
	}
	for key, values := range q {
		if !allowed[key] || len(values) != 1 {
			return errors.New("invalid presigned REST query parameters")
		}
	}
	got := q.Get(presignOperationKey)
	if operation == "get" && got != "get" || operation == "put" && got != "" && got != "put" {
		return errors.New("invalid presigned REST operation")
	}
	return nil
}

func (p *PutPresigner) signatureFor(method, path, tenant string, expires int64) string {
	if path == "" {
		path = "/"
	}
	payload := strings.Join([]string{
		method,
		path,
		tenant,
		strconv.FormatInt(expires, 10),
	}, "\n")
	mac := hmac.New(sha256.New, p.secret)
	_, _ = mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
