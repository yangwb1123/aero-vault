package s3compat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/go-chi/chi/v5"

	"github.com/aero-vault/aero-vault/internal/auth"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

// newSignedS3Server wires the S3-compat router behind the SigV4-verifying auth
// middleware.
func newSignedS3Server(t *testing.T) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "s.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, _ := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	svc := service.NewFileService(store, repo, nil)

	reg, _ := auth.Parse("")
	sv, err := auth.ParseSigV4Credentials("AKIDEXAMPLE:secretkey123:acme:read+write")
	if err != nil {
		t.Fatalf("creds: %v", err)
	}
	reg.WithSigV4(sv)

	r := chi.NewRouter()
	r.Use(reg.Middleware())
	r.Mount("/", NewRouter(svc, nil))
	srv := httptest.NewServer(r)
	t.Cleanup(func() { srv.Close(); _ = repo.Close() })
	return srv
}

func sign(t *testing.T, req *http.Request, body string) {
	t.Helper()
	payloadHash := "UNSIGNED-PAYLOAD"
	if req.Method == "PUT" {
		payloadHash = sha256Hex(body)
		req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	} else {
		req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	}
	s := v4.NewSigner()
	if err := s.SignHTTP(context.Background(),
		aws.Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "secretkey123"},
		req, payloadHash, "s3", "us-east-1", time.Now().UTC()); err != nil {
		t.Fatalf("sign: %v", err)
	}
}

func TestSigV4S3RoundTrip(t *testing.T) {
	s := newSignedS3Server(t)

	// Unsigned request is rejected.
	if resp, _ := http.Get(s.URL + "/bucket/key.txt"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unsigned GET: status=%d want 401", resp.StatusCode)
	}

	// Signed PUT.
	body := "signed object body"
	putReq, _ := http.NewRequest("PUT", s.URL+"/bucket/key.txt", strings.NewReader(body))
	sign(t, putReq, body)
	resp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("signed PUT: status=%d", resp.StatusCode)
	}

	// Signed GET returns the content.
	getReq, _ := http.NewRequest("GET", s.URL+"/bucket/key.txt", nil)
	sign(t, getReq, "")
	resp, err = http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(got) != body {
		t.Fatalf("signed GET: status=%d body=%q", resp.StatusCode, got)
	}
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
