package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// AWS SigV4 verification for the S3-compatible endpoint. It is additive: only
// requests that present SigV4 (an AWS4-HMAC-SHA256 Authorization header or
// presigned X-Amz-Signature query) are verified here; bearer/no-auth flows are
// untouched. Credentials (access key → secret + tenant + scopes) come from
// S3_SIGV4_CREDENTIALS.

const (
	sigV4Algorithm  = "AWS4-HMAC-SHA256"
	sigV4Service    = "s3"
	unsignedPayload = "UNSIGNED-PAYLOAD"
	maxHeaderSkew   = 15 * time.Minute
	maxPresignTTL   = 7 * 24 * time.Hour
)

type sigV4Cred struct {
	secret string
	key    Key
}

// SigV4Verifier resolves and verifies SigV4-signed S3 requests.
type SigV4Verifier struct {
	creds map[string]sigV4Cred // accessKey -> cred
}

// ParseSigV4Credentials parses "ak:sk:tenant:scope+scope,..." into a verifier.
// An empty string yields a nil verifier (disabled).
func ParseSigV4Credentials(raw string) (*SigV4Verifier, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	v := &SigV4Verifier{creds: map[string]sigV4Cred{}}
	for _, rec := range strings.Split(raw, ",") {
		rec = strings.TrimSpace(rec)
		if rec == "" {
			continue
		}
		parts := strings.SplitN(rec, ":", 4)
		if len(parts) < 3 {
			return v, errors.New("S3_SIGV4_CREDENTIALS: want accessKey:secretKey:tenant[:scope+scope]")
		}
		if parts[0] == "" || parts[1] == "" || parts[2] == "" {
			return v, errors.New("S3_SIGV4_CREDENTIALS: access key, secret and tenant are required")
		}
		if _, duplicate := v.creds[parts[0]]; duplicate {
			return v, errors.New("S3_SIGV4_CREDENTIALS: duplicate access key " + parts[0])
		}
		k := Key{Token: parts[0], Tenant: parts[2], Scopes: map[Scope]bool{ScopeRead: true, ScopeWrite: true}}
		if len(parts) == 4 && parts[3] != "" {
			k.Scopes = map[Scope]bool{}
			for _, sc := range strings.Split(parts[3], "+") {
				if sc = strings.TrimSpace(sc); sc != "" {
					scope := Scope(sc)
					if !knownScope(scope) {
						return v, errors.New("S3_SIGV4_CREDENTIALS: " + rec + ": unknown scope " + sc)
					}
					k.Scopes[scope] = true
				}
			}
			// An explicit scope segment that parses to nothing (e.g. "+" or
			// whitespace) would silently drop all permissions — reject it rather
			// than leave the credential with no scopes.
			if len(k.Scopes) == 0 {
				return v, errors.New("S3_SIGV4_CREDENTIALS: " + rec + ": scope segment has no valid scope")
			}
		}
		v.creds[parts[0]] = sigV4Cred{secret: parts[1], key: k}
	}
	if len(v.creds) == 0 {
		return v, errors.New("S3_SIGV4_CREDENTIALS: no valid records")
	}
	return v, nil
}

// IsSigned reports whether a request carries SigV4 material.
func IsSigned(r *http.Request) bool {
	if strings.HasPrefix(r.Header.Get("Authorization"), sigV4Algorithm) {
		return true
	}
	return r.URL.Query().Get("X-Amz-Signature") != ""
}

// Verify checks a SigV4-signed request and returns the resolved Key. The payload
// hash used is the client's declared x-amz-content-sha256 (or UNSIGNED-PAYLOAD
// for presigned), so the body need not be read to verify the signature.
func (v *SigV4Verifier) Verify(r *http.Request) (Key, error) {
	if v == nil {
		return Key{}, errors.New("sigv4 not configured")
	}
	if r.URL.Query().Get("X-Amz-Signature") != "" {
		return v.verifyPresigned(r)
	}
	return v.verifyHeader(r)
}

func (v *SigV4Verifier) verifyHeader(r *http.Request) (Key, error) {
	auth := r.Header.Get("Authorization")
	cred, signedHeaders, providedSig, err := parseAuthHeader(auth)
	if err != nil {
		return Key{}, err
	}
	accessKey, scope, err := splitCredential(cred)
	if err != nil {
		return Key{}, err
	}
	c, ok := v.creds[accessKey]
	if !ok {
		return Key{}, errors.New("sigv4: unknown access key")
	}
	amzDate := r.Header.Get("X-Amz-Date")
	if amzDate == "" {
		return Key{}, errors.New("sigv4: missing X-Amz-Date")
	}
	if err := validateCredentialScope(scope, amzDate); err != nil {
		return Key{}, err
	}
	if err := validateHeaderTime(amzDate, time.Now().UTC()); err != nil {
		return Key{}, err
	}
	if err := validateSignedHeaders(signedHeaders); err != nil {
		return Key{}, err
	}
	payloadHash := r.Header.Get("X-Amz-Content-Sha256")
	if payloadHash == "" {
		payloadHash = unsignedPayload
	}
	if err := validatePayloadHash(payloadHash); err != nil {
		return Key{}, err
	}
	sig := v.sign(r, scope, signedHeaders, payloadHash, amzDate, c.secret)
	if !hmac.Equal([]byte(sig), []byte(providedSig)) {
		return Key{}, errors.New("sigv4: signature mismatch")
	}
	return c.key, nil
}

func (v *SigV4Verifier) verifyPresigned(r *http.Request) (Key, error) {
	q, accessKey, err := parsePresignedURL(r)
	if err != nil {
		return Key{}, err
	}
	c, ok := v.creds[accessKey]
	if !ok {
		return Key{}, errors.New("sigv4: unknown access key")
	}
	if !v.verifyPresignedSig(r, q, c.secret) {
		return Key{}, errors.New("sigv4: presigned signature mismatch")
	}
	return c.key, nil
}

func parsePresignedURL(r *http.Request) (url.Values, string, error) {
	q := r.URL.Query()
	if q.Get("X-Amz-Algorithm") != sigV4Algorithm {
		return nil, "", errors.New("sigv4: bad presign algorithm")
	}
	accessKey, scope, err := splitCredential(q.Get("X-Amz-Credential"))
	if err != nil {
		return nil, "", err
	}
	amzDate := q.Get("X-Amz-Date")
	if amzDate == "" {
		return nil, "", errors.New("sigv4: missing X-Amz-Date")
	}
	signedAt, err := time.Parse("20060102T150405Z", amzDate)
	if err != nil {
		return nil, "", errors.New("sigv4: invalid X-Amz-Date")
	}
	if err := validateCredentialScope(scope, amzDate); err != nil {
		return nil, "", err
	}
	if err := validateSignedHeaders(strings.Split(q.Get("X-Amz-SignedHeaders"), ";")); err != nil {
		return nil, "", err
	}
	secs, err := strconv.Atoi(q.Get("X-Amz-Expires"))
	if err != nil || secs <= 0 || time.Duration(secs)*time.Second > maxPresignTTL {
		return nil, "", errors.New("sigv4: invalid X-Amz-Expires")
	}
	now := time.Now().UTC()
	if signedAt.After(now.Add(maxHeaderSkew)) {
		return nil, "", errors.New("sigv4: request date is in the future")
	}
	if now.After(signedAt.Add(time.Duration(secs) * time.Second)) {
		return nil, "", errors.New("sigv4: presigned URL expired")
	}
	return q, accessKey, nil
}

func (v *SigV4Verifier) verifyPresignedSig(r *http.Request, params url.Values, secret string) bool {
	_, scope, _ := splitCredential(params.Get("X-Amz-Credential"))
	signedHeaders := strings.Split(params.Get("X-Amz-SignedHeaders"), ";")
	amzDate := params.Get("X-Amz-Date")
	providedSig := params.Get("X-Amz-Signature")
	params.Del("X-Amz-Signature")
	sig := v.signWith(r.Method, r.URL.EscapedPath(), encodeQuery(params), r, scope, signedHeaders, unsignedPayload, amzDate, secret)
	return hmac.Equal([]byte(sig), []byte(providedSig))
}

// sign builds the signature for a header-authenticated request.
func (v *SigV4Verifier) sign(r *http.Request, scope string, signedHeaders []string, payloadHash, amzDate, secret string) string {
	return v.signWith(r.Method, r.URL.EscapedPath(), encodeQuery(r.URL.Query()), r, scope, signedHeaders, payloadHash, amzDate, secret)
}

func (v *SigV4Verifier) signWith(method, canonicalURI, canonicalQuery string, r *http.Request, scope string, signedHeaders []string, payloadHash, amzDate, secret string) string {
	if canonicalURI == "" {
		canonicalURI = "/"
	}
	// Canonical headers + signed-headers list.
	lower := make([]string, len(signedHeaders))
	for i, h := range signedHeaders {
		lower[i] = strings.ToLower(strings.TrimSpace(h))
	}
	sort.Strings(lower)
	var ch strings.Builder
	for _, h := range lower {
		ch.WriteString(h)
		ch.WriteByte(':')
		ch.WriteString(canonicalHeaderValue(r, h))
		ch.WriteByte('\n')
	}
	signedHeaderList := strings.Join(lower, ";")

	canonicalRequest := strings.Join([]string{
		method, canonicalURI, canonicalQuery, ch.String(), signedHeaderList, payloadHash,
	}, "\n")

	hashedCanonical := hexSHA256([]byte(canonicalRequest))
	stringToSign := strings.Join([]string{sigV4Algorithm, amzDate, scope, hashedCanonical}, "\n")

	// scope = date/region/service/aws4_request
	parts := strings.Split(scope, "/")
	signingKey := deriveSigningKey(secret, parts[0], parts[1])
	return hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))
}

func canonicalHeaderValue(r *http.Request, name string) string {
	switch name {
	case "host":
		return strings.TrimSpace(r.Host)
	case "content-length":
		// Go exposes Content-Length via the dedicated field, not the header map.
		if vals := r.Header["Content-Length"]; len(vals) > 0 {
			return strings.TrimSpace(vals[0])
		}
		if r.ContentLength >= 0 {
			return strconv.FormatInt(r.ContentLength, 10)
		}
		return "0"
	}
	vals := r.Header[http.CanonicalHeaderKey(name)]
	trimmed := make([]string, len(vals))
	for i := range vals {
		trimmed[i] = strings.Join(strings.Fields(vals[i]), " ")
	}
	return strings.Join(trimmed, ",")
}

// deriveSigningKey runs the SigV4 HMAC chain: secret→date→region→s3→aws4_request.
func deriveSigningKey(secret, date, region string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(date))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(sigV4Service))
	return hmacSHA256(kService, []byte("aws4_request"))
}

func hmacSHA256(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

func hexSHA256(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// parseAuthHeader extracts Credential, SignedHeaders, Signature from an
// "AWS4-HMAC-SHA256 Credential=..., SignedHeaders=..., Signature=..." header.
func parseAuthHeader(h string) (credential string, signedHeaders []string, signature string, err error) {
	if !strings.HasPrefix(h, sigV4Algorithm+" ") {
		return "", nil, "", errors.New("sigv4: malformed Authorization header")
	}
	h = strings.TrimSpace(strings.TrimPrefix(h, sigV4Algorithm))
	for _, part := range strings.Split(h, ",") {
		part = strings.TrimSpace(part)
		switch {
		case strings.HasPrefix(part, "Credential="):
			credential = strings.TrimPrefix(part, "Credential=")
		case strings.HasPrefix(part, "SignedHeaders="):
			signedHeaders = strings.Split(strings.TrimPrefix(part, "SignedHeaders="), ";")
		case strings.HasPrefix(part, "Signature="):
			signature = strings.TrimPrefix(part, "Signature=")
		}
	}
	if credential == "" || len(signedHeaders) == 0 || signature == "" {
		return "", nil, "", errors.New("sigv4: malformed Authorization header")
	}
	return credential, signedHeaders, signature, nil
}

// splitCredential splits "AK/date/region/s3/aws4_request" into the access key
// and the credential scope "date/region/s3/aws4_request".
func splitCredential(cred string) (accessKey, scope string, err error) {
	parts := strings.Split(cred, "/")
	if len(parts) != 5 {
		return "", "", errors.New("sigv4: malformed credential")
	}
	for _, part := range parts {
		if part == "" {
			return "", "", errors.New("sigv4: malformed credential")
		}
	}
	return parts[0], strings.Join(parts[1:], "/"), nil
}

// encodeQuery builds the SigV4 canonical query string: params sorted by key,
// each key and value URI-encoded (RFC 3986), joined by '&'.
func encodeQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	first := true
	for _, k := range keys {
		vals := append([]string(nil), q[k]...)
		sort.Strings(vals)
		for _, val := range vals {
			if !first {
				b.WriteByte('&')
			}
			first = false
			b.WriteString(uriEncode(k, true))
			b.WriteByte('=')
			b.WriteString(uriEncode(val, true))
		}
	}
	return b.String()
}

// uriEncode implements AWS's URI encoding (RFC 3986). When encodeSlash is false,
// '/' is left as-is (used for canonical URIs, not query values).
func uriEncode(s string, encodeSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == '/' && !encodeSlash:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			const hexd = "0123456789ABCDEF"
			b.WriteByte(hexd[c>>4])
			b.WriteByte(hexd[c&0xf])
		}
	}
	return b.String()
}
