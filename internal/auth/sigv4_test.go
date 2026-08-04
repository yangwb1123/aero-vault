package auth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

func signedReq(t *testing.T, method, url, body, ak, sk string) *http.Request {
	return signedReqAt(t, method, url, body, ak, sk, time.Now().UTC())
}

func signedReqAt(t *testing.T, method, url, body, ak, sk string, signedAt time.Time) *http.Request {
	t.Helper()
	var r *strings.Reader
	if body != "" {
		r = strings.NewReader(body)
	} else {
		r = strings.NewReader("")
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	payloadHash := hexSHA256([]byte(body))
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	signer := v4.NewSigner()
	if err := signer.SignHTTP(context.Background(),
		aws.Credentials{AccessKeyID: ak, SecretAccessKey: sk},
		req, payloadHash, "s3", "us-east-1", signedAt); err != nil {
		t.Fatalf("sign: %v", err)
	}
	return req
}

func TestSigV4HeaderVerify(t *testing.T) {
	v, err := ParseSigV4Credentials("AKIDEXAMPLE:secretkey123:acme:read+write")
	if err != nil {
		t.Fatalf("parse creds: %v", err)
	}
	req := signedReq(t, "PUT", "http://vault.example.com/s3/bucket/path/to/key.txt", "hello world", "AKIDEXAMPLE", "secretkey123")

	key, err := v.Verify(req)
	if err != nil {
		t.Fatalf("verify valid signature: %v", err)
	}
	if key.Tenant != "acme" || !key.Has(ScopeWrite) {
		t.Fatalf("resolved key wrong: %+v", key)
	}
}

func TestSigV4WithQueryParams(t *testing.T) {
	v, _ := ParseSigV4Credentials("AKIDEXAMPLE:secretkey123:acme")
	// list-type style query must be folded into the canonical request.
	req := signedReq(t, "GET", "http://vault.example.com/s3/bucket/?list-type=2&prefix=a/b", "", "AKIDEXAMPLE", "secretkey123")
	if _, err := v.Verify(req); err != nil {
		t.Fatalf("verify with query: %v", err)
	}
}

func TestSigV4TamperDetected(t *testing.T) {
	v, _ := ParseSigV4Credentials("AKIDEXAMPLE:secretkey123:acme")
	req := signedReq(t, "PUT", "http://vault.example.com/s3/bucket/key.txt", "data", "AKIDEXAMPLE", "secretkey123")
	// Tamper with a signed header after signing.
	req.Header.Set("X-Amz-Content-Sha256", hexSHA256([]byte("different")))
	if _, err := v.Verify(req); err == nil {
		t.Fatal("expected signature mismatch after tampering")
	}
}

func TestSigV4UnknownKey(t *testing.T) {
	v, _ := ParseSigV4Credentials("AKIDEXAMPLE:secretkey123:acme")
	req := signedReq(t, "GET", "http://vault.example.com/s3/bucket/key.txt", "", "WRONGKEY", "secretkey123")
	if _, err := v.Verify(req); err == nil {
		t.Fatal("expected unknown access key error")
	}
}

func TestSigV4Presigned(t *testing.T) {
	v, _ := ParseSigV4Credentials("AKIDEXAMPLE:secretkey123:acme:read")
	req, _ := http.NewRequest("GET", "http://vault.example.com/s3/bucket/key.txt", nil)
	query := req.URL.Query()
	query.Set("X-Amz-Expires", "900")
	req.URL.RawQuery = query.Encode()
	signer := v4.NewSigner()
	// PresignHTTP signs the URL with UNSIGNED-PAYLOAD and returns a signed URL.
	signedURL, _, err := signer.PresignHTTP(context.Background(),
		aws.Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "secretkey123"},
		req, unsignedPayload, "s3", "us-east-1", time.Now().UTC())
	if err != nil {
		t.Fatalf("presign: %v", err)
	}
	preq, _ := http.NewRequest("GET", signedURL, nil)
	preq.Host = "vault.example.com"
	if _, err := v.Verify(preq); err != nil {
		t.Fatalf("verify presigned: %v", err)
	}
}

func TestVerifiedStreamingBody(t *testing.T) {
	const (
		amzDate = "20260728T120000Z"
		scope   = "20260728/us-east-1/s3/aws4_request"
		seed    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)
	key := deriveSigningKey("secretkey123", "20260728", "us-east-1")
	first := streamingChunkSignature(key, amzDate, scope, seed, []byte("Hello"))
	second := streamingChunkSignature(key, amzDate, scope, first, []byte(" world"))
	final := streamingChunkSignature(key, amzDate, scope, second, nil)
	wire := "5;chunk-signature=" + first + "\r\nHello\r\n" +
		"6;chunk-signature=" + second + "\r\n world\r\n" +
		"0;chunk-signature=" + final + "\r\n\r\n"
	req, _ := http.NewRequest("PUT", "http://h/s3/b/k", strings.NewReader(wire))
	req.Header.Set("X-Amz-Content-Sha256", streamingPayload)
	req.Header.Set("X-Amz-Decoded-Content-Length", "11")
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("Authorization", sigV4Algorithm+" Credential=AKIDEXAMPLE/"+scope+
		", SignedHeaders=host;x-amz-date, Signature="+seed)
	verifier, _ := ParseSigV4Credentials("AKIDEXAMPLE:secretkey123:acme")
	if err := verifier.PrepareBody(req); err != nil {
		t.Fatalf("prepare body: %v", err)
	}
	got, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read decoded body: %v", err)
	}
	if string(got) != "Hello world" {
		t.Fatalf("decoded=%q want %q", got, "Hello world")
	}
	if req.ContentLength != 11 {
		t.Fatalf("content-length=%d want 11", req.ContentLength)
	}
}

func TestSigV4PayloadTamperDetectedWhileReading(t *testing.T) {
	verifier, _ := ParseSigV4Credentials("AKIDEXAMPLE:secretkey123:acme")
	req := signedReq(t, "PUT", "http://h/s3/b/k", "original", "AKIDEXAMPLE", "secretkey123")
	req.Body = io.NopCloser(strings.NewReader("tampered"))
	req.ContentLength = int64(len("tampered"))
	if _, err := verifier.Verify(req); err != nil {
		t.Fatalf("seed signature should still verify: %v", err)
	}
	if err := verifier.PrepareBody(req); err != nil {
		t.Fatalf("prepare body: %v", err)
	}
	if _, err := io.ReadAll(req.Body); err == nil || !strings.Contains(err.Error(), "payload hash mismatch") {
		t.Fatalf("expected payload mismatch, got %v", err)
	}
}

func TestSigV4RejectsStaleHeader(t *testing.T) {
	verifier, _ := ParseSigV4Credentials("AKIDEXAMPLE:secretkey123:acme")
	req := signedReqAt(
		t, "GET", "http://h/s3/b/k", "", "AKIDEXAMPLE", "secretkey123",
		time.Now().UTC().Add(-time.Hour),
	)
	if _, err := verifier.Verify(req); err == nil || !strings.Contains(err.Error(), "allowed skew") {
		t.Fatalf("expected stale request rejection, got %v", err)
	}
}

func TestSigV4RejectsMalformedCredentialWithoutPanic(t *testing.T) {
	verifier, _ := ParseSigV4Credentials("AKIDEXAMPLE:secretkey123:acme")
	req := signedReq(t, "GET", "http://h/s3/b/k", "", "AKIDEXAMPLE", "secretkey123")
	authHeader := req.Header.Get("Authorization")
	start := strings.Index(authHeader, "Credential=")
	end := strings.Index(authHeader[start:], ",")
	req.Header.Set("Authorization", authHeader[:start]+"Credential=AKIDEXAMPLE/bad"+authHeader[start+end:])
	if _, err := verifier.Verify(req); err == nil {
		t.Fatal("malformed credential must be rejected")
	}
}

func TestSigV4Middleware(t *testing.T) {
	reg, _ := Parse("") // no static keys; sigv4-only
	sv, _ := ParseSigV4Credentials("AKIDEXAMPLE:secretkey123:acme:read+write")
	reg.WithSigV4(sv)

	var gotTenant, gotBody string
	h := reg.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k, _ := FromContext(r.Context())
		gotTenant = k.Tenant
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))

	// Signed PUT passes and resolves the tenant; body is readable.
	req := signedReq(t, "PUT", "http://h/s3/b/k.txt", "payload-bytes", "AKIDEXAMPLE", "secretkey123")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 200 || gotTenant != "acme" || gotBody != "payload-bytes" {
		t.Fatalf("signed PUT: code=%d tenant=%q body=%q", rr.Code, gotTenant, gotBody)
	}

	// Unsigned request is rejected (auth is active).
	req2, _ := http.NewRequest("GET", "http://h/s3/b/k.txt", nil)
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned: code=%d want 401", rr2.Code)
	}

	// Bad signature → 403.
	req3 := signedReq(t, "GET", "http://h/s3/b/k.txt", "", "AKIDEXAMPLE", "secretkey123")
	req3.Header.Set("X-Amz-Date", "20200101T000000Z") // break the signature
	rr3 := httptest.NewRecorder()
	h.ServeHTTP(rr3, req3)
	if rr3.Code != http.StatusForbidden {
		t.Fatalf("tampered: code=%d want 403", rr3.Code)
	}

	// A tenant-bound SigV4 credential cannot override a conflicting tenant.
	req4 := signedReq(t, "GET", "http://h/s3/b/k.txt", "", "AKIDEXAMPLE", "secretkey123")
	req4.Header.Set("X-Aero-Tenant", "other")
	rr4 := httptest.NewRecorder()
	h.ServeHTTP(rr4, req4)
	if rr4.Code != http.StatusForbidden {
		t.Fatalf("tenant mismatch: code=%d want 403", rr4.Code)
	}
}

// A non-empty scope segment that parses to nothing ("+", "++") would silently
// strip all permissions; ParseSigV4Credentials must reject it. (A bare trailing
// colon / whitespace-only segment is trimmed away at the record level and means
// "no explicit scopes" → defaults, not an error — covered by the default path
// below.) The default-scopes path (no 4th segment) must keep {read,write}.
func TestParseSigV4Credentials_ScopeHandling(t *testing.T) {
	// Explicit, non-empty scope segment that parses to zero scopes → error.
	for _, raw := range []string{
		"AK:SK:acme:+",  // separator only
		"AK:SK:acme:++", // separators only
	} {
		if _, err := ParseSigV4Credentials(raw); err == nil {
			t.Fatalf("ParseSigV4Credentials(%q): expected an error for a scope segment with no valid scope", raw)
		}
	}

	// A bare trailing colon (empty 4th segment after trimming) is NOT an error —
	// it is treated as "no explicit scopes" and keeps the {read,write} defaults.
	if v, err := ParseSigV4Credentials("AK:SK:acme:"); err != nil {
		t.Fatalf("bare trailing colon should default scopes, got error: %v", err)
	} else if c := v.creds["AK"]; !c.key.Has(ScopeRead) || !c.key.Has(ScopeWrite) {
		t.Fatalf("bare trailing colon should keep read+write defaults, got %+v", c.key.Scopes)
	}

	// No 4th segment → default {read,write} preserved.
	v, err := ParseSigV4Credentials("AK:SK:acme")
	if err != nil {
		t.Fatalf("ParseSigV4Credentials default-scopes path: %v", err)
	}
	c, ok := v.creds["AK"]
	if !ok {
		t.Fatal("AK should be registered")
	}
	if !c.key.Has(ScopeRead) || !c.key.Has(ScopeWrite) {
		t.Fatalf("default scopes should be read+write, got %+v", c.key.Scopes)
	}

	// Explicit non-empty scope → exactly that scope.
	v2, err := ParseSigV4Credentials("AK:SK:acme:read")
	if err != nil {
		t.Fatalf("ParseSigV4Credentials explicit read: %v", err)
	}
	c2 := v2.creds["AK"]
	if !c2.key.Has(ScopeRead) || c2.key.Has(ScopeWrite) {
		t.Fatalf("explicit read-only scope wrong: %+v", c2.key.Scopes)
	}
}

func TestIsSigned(t *testing.T) {
	r1, _ := http.NewRequest("GET", "http://x/s3/b/k", nil)
	if IsSigned(r1) {
		t.Fatal("plain request should not be signed")
	}
	r1.Header.Set("Authorization", sigV4Algorithm+" Credential=...")
	if !IsSigned(r1) {
		t.Fatal("AWS4 header should be detected")
	}
	r2, _ := http.NewRequest("GET", "http://x/s3/b/k?X-Amz-Signature=abc", nil)
	if !IsSigned(r2) {
		t.Fatal("presigned query should be detected")
	}
}
