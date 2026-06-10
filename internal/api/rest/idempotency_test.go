package rest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func idemTestRepo(t *testing.T) repository.Repository {
	t.Helper()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(t.TempDir(), "idem.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return repo
}

func idemSilentLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// TestIdempotency_ReplaysOnRetry: a second write with the same key replays the
// original response and does not re-run the handler.
func TestIdempotency_ReplaysOnRetry(t *testing.T) {
	repo := idemTestRepo(t)
	calls := 0
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"version":%d}`, calls)
	})
	mwh := idempotency(repo, idemSilentLogger(), false)(h)

	do := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/v1/files/a.txt", strings.NewReader("body"))
		req.Header.Set("Idempotency-Key", "key-1")
		rr := httptest.NewRecorder()
		mwh.ServeHTTP(rr, req)
		return rr
	}

	rr1 := do()
	rr2 := do()

	if calls != 1 {
		t.Fatalf("handler should run exactly once, ran %d", calls)
	}
	if rr1.Code != rr2.Code {
		t.Fatalf("replay status mismatch: %d vs %d", rr1.Code, rr2.Code)
	}
	if rr1.Body.String() != rr2.Body.String() {
		t.Fatalf("replay body mismatch: %q vs %q", rr1.Body.String(), rr2.Body.String())
	}
	if rr1.Header().Get("Idempotency-Replayed") != "" {
		t.Fatal("first response must not be marked replayed")
	}
	if rr2.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatal("replayed response must carry Idempotency-Replayed: true")
	}
	if rr2.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("replay must restore Content-Type, got %q", rr2.Header().Get("Content-Type"))
	}
}

// TestIdempotency_NoHeaderPassThrough: without the header the middleware is
// inert and the handler runs every time.
func TestIdempotency_NoHeaderPassThrough(t *testing.T) {
	repo := idemTestRepo(t)
	calls := 0
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusCreated)
	})
	mwh := idempotency(repo, idemSilentLogger(), false)(h)

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPut, "/v1/files/a.txt", strings.NewReader("body"))
		rr := httptest.NewRecorder()
		mwh.ServeHTTP(rr, req)
	}
	if calls != 2 {
		t.Fatalf("no header → handler should run each time, ran %d", calls)
	}
}

// TestIdempotency_DifferentRequestConflict: reusing a key for a different
// path is rejected with 409.
func TestIdempotency_DifferentRequestConflict(t *testing.T) {
	repo := idemTestRepo(t)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	mwh := idempotency(repo, idemSilentLogger(), false)(h)

	req1 := httptest.NewRequest(http.MethodPut, "/v1/files/a.txt", strings.NewReader("x"))
	req1.Header.Set("Idempotency-Key", "shared")
	rr1 := httptest.NewRecorder()
	mwh.ServeHTTP(rr1, req1)

	req2 := httptest.NewRequest(http.MethodPut, "/v1/files/b.txt", strings.NewReader("x"))
	req2.Header.Set("Idempotency-Key", "shared")
	rr2 := httptest.NewRecorder()
	mwh.ServeHTTP(rr2, req2)

	if rr2.Code != http.StatusConflict {
		t.Fatalf("expected 409 for key reused on a different path, got %d", rr2.Code)
	}
}

// TestIdempotency_5xxReleasesClaim: a 5xx response is not memoized; the claim
// is released so a retry re-executes the handler.
func TestIdempotency_5xxReleasesClaim(t *testing.T) {
	repo := idemTestRepo(t)
	calls := 0
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, "boom")
			return
		}
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, "ok")
	})
	mwh := idempotency(repo, idemSilentLogger(), false)(h)

	do := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/v1/files/a.txt", strings.NewReader("body"))
		req.Header.Set("Idempotency-Key", "key-5xx")
		rr := httptest.NewRecorder()
		mwh.ServeHTTP(rr, req)
		return rr
	}

	rr1 := do()
	rr2 := do()

	if rr1.Code != http.StatusInternalServerError {
		t.Fatalf("first call should be 500, got %d", rr1.Code)
	}
	if calls != 2 {
		t.Fatalf("5xx must release the claim so retry re-runs; calls=%d", calls)
	}
	if rr2.Code != http.StatusCreated {
		t.Fatalf("retry after 5xx should re-execute and return 201, got %d", rr2.Code)
	}
	if rr2.Header().Get("Idempotency-Replayed") == "true" {
		t.Fatal("a released claim must not replay")
	}
}

// TestIdempotency_InProgressConflict: a key already claimed (in progress)
// rejects a concurrent duplicate with 409 and never runs the handler.
func TestIdempotency_InProgressConflict(t *testing.T) {
	repo := idemTestRepo(t)

	// Pre-claim the key with the exact fingerprint the request will produce.
	probe := httptest.NewRequest(http.MethodPut, "/v1/files/a.txt", nil)
	fp := fingerprint(probe)
	_, claimed, err := repo.ClaimIdempotencyKey(context.Background(), "default", "k-inflight", fp, "req0")
	if err != nil || !claimed {
		t.Fatalf("setup claim failed: claimed=%v err=%v", claimed, err)
	}

	calls := 0
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusCreated)
	})
	mwh := idempotency(repo, idemSilentLogger(), false)(h)

	req := httptest.NewRequest(http.MethodPut, "/v1/files/a.txt", nil)
	req.Header.Set("Idempotency-Key", "k-inflight")
	rr := httptest.NewRecorder()
	mwh.ServeHTTP(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("expected 409 for in-progress key, got %d", rr.Code)
	}
	if calls != 0 {
		t.Fatalf("handler must not run while a claim is in progress; calls=%d", calls)
	}
}

// TestIdempotencyHashBody_SameBodyReplays: with body hashing enabled, a retry
// carrying identical bytes replays the stored response.
func TestIdempotencyHashBody_SameBodyReplays(t *testing.T) {
	repo := idemTestRepo(t)
	calls := 0
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"version":%d}`, calls)
	})
	mwh := idempotency(repo, idemSilentLogger(), true)(h)

	do := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/v1/files/a.txt", strings.NewReader(body))
		req.Header.Set("Idempotency-Key", "key-hash")
		rr := httptest.NewRecorder()
		mwh.ServeHTTP(rr, req)
		return rr
	}

	rr1 := do("payload")
	rr2 := do("payload")

	if calls != 1 {
		t.Fatalf("handler should run exactly once, ran %d", calls)
	}
	if rr2.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatal("identical retry must replay")
	}
	if rr1.Body.String() != rr2.Body.String() {
		t.Fatalf("replay body mismatch: %q vs %q", rr1.Body.String(), rr2.Body.String())
	}
}

// TestIdempotencyHashBody_DifferentBodyConflict: same key + different bytes is
// rejected instead of replaying a response that belongs to other content.
func TestIdempotencyHashBody_DifferentBodyConflict(t *testing.T) {
	repo := idemTestRepo(t)
	calls := 0
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusCreated)
	})
	mwh := idempotency(repo, idemSilentLogger(), true)(h)

	do := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/v1/files/a.txt", strings.NewReader(body))
		req.Header.Set("Idempotency-Key", "key-bytes")
		rr := httptest.NewRecorder()
		mwh.ServeHTTP(rr, req)
		return rr
	}

	rr1 := do("payload-a")
	rr2 := do("payload-b")

	if rr1.Code != http.StatusCreated {
		t.Fatalf("first request should succeed, got %d", rr1.Code)
	}
	if rr2.Code != http.StatusConflict {
		t.Fatalf("expected 409 for same key with different bytes, got %d", rr2.Code)
	}
	if !strings.Contains(rr2.Body.String(), "IdempotencyConflict") {
		t.Fatalf("conflict body should carry IdempotencyConflict, got %q", rr2.Body.String())
	}
	if calls != 1 {
		t.Fatalf("conflicting retry must not re-run the handler; calls=%d", calls)
	}
}

// TestIdempotencyHashBody_HandlerSeesBody: hashing must not consume the body —
// the downstream handler reads exactly the bytes the client sent.
func TestIdempotencyHashBody_HandlerSeesBody(t *testing.T) {
	repo := idemTestRepo(t)
	sent := "the exact bytes the client sent"
	var got string
	var gotLen int64
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("handler body read: %v", err)
		}
		got = string(b)
		gotLen = r.ContentLength
		w.WriteHeader(http.StatusCreated)
	})
	mwh := idempotency(repo, idemSilentLogger(), true)(h)

	req := httptest.NewRequest(http.MethodPut, "/v1/files/a.txt", strings.NewReader(sent))
	req.Header.Set("Idempotency-Key", "key-stream")
	rr := httptest.NewRecorder()
	mwh.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rr.Code)
	}
	if got != sent {
		t.Fatalf("handler saw %q, client sent %q", got, sent)
	}
	if gotLen != int64(len(sent)) {
		t.Fatalf("ContentLength not preserved: got %d, want %d", gotLen, len(sent))
	}
}

// TestIdempotencyHashBody_LargeBodySpills: a body past the in-memory bound
// round-trips through the temp-file spill path and still dedupes.
func TestIdempotencyHashBody_LargeBodySpills(t *testing.T) {
	repo := idemTestRepo(t)
	big := make([]byte, 9<<20)
	for i := range big {
		big[i] = byte(i % 251)
	}
	calls := 0
	var got []byte
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("handler body read: %v", err)
		}
		got = b
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, "stored")
	})
	mwh := idempotency(repo, idemSilentLogger(), true)(h)

	do := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/v1/files/big.bin", bytes.NewReader(big))
		req.Header.Set("Idempotency-Key", "key-big")
		rr := httptest.NewRecorder()
		mwh.ServeHTTP(rr, req)
		return rr
	}

	rr1 := do()
	rr2 := do()

	if rr1.Code != http.StatusCreated {
		t.Fatalf("first request should succeed, got %d", rr1.Code)
	}
	if calls != 1 {
		t.Fatalf("handler should run exactly once, ran %d", calls)
	}
	if !bytes.Equal(got, big) {
		t.Fatalf("handler saw %d bytes, client sent %d (or content differs)", len(got), len(big))
	}
	if rr2.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatal("identical large retry must replay")
	}
	if rr2.Body.String() != "stored" {
		t.Fatalf("replay body mismatch: %q", rr2.Body.String())
	}
}

// TestIdemSpool_CloseRemovesTempFile: past the threshold the spool backs onto a
// temp file, the replayed body matches byte-for-byte, and Close removes the
// file (and is safe to call twice).
func TestIdemSpool_CloseRemovesTempFile(t *testing.T) {
	big := bytes.Repeat([]byte("x"), idemSpoolThreshold+1)
	req := httptest.NewRequest(http.MethodPut, "/v1/files/a.bin", bytes.NewReader(big))

	sp, bodyHash, err := spoolBody(req)
	if err != nil {
		t.Fatalf("spoolBody: %v", err)
	}
	if sp.file == nil {
		t.Fatal("body past the threshold must spill to a temp file")
	}
	name := sp.file.Name()

	b, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read spooled body: %v", err)
	}
	if !bytes.Equal(b, big) {
		t.Fatalf("spooled body differs: got %d bytes, want %d", len(b), len(big))
	}
	want := sha256.Sum256(big)
	if bodyHash != hex.EncodeToString(want[:]) {
		t.Fatalf("body hash mismatch: got %s", bodyHash)
	}

	if err := sp.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := os.Stat(name); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("temp file must be removed on Close; stat err=%v", err)
	}
	if err := sp.Close(); err != nil {
		t.Fatalf("second close must be a no-op, got %v", err)
	}
}

type idemErrReader struct{}

func (idemErrReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

// TestIdempotencyHashBody_SpoolErrorFailsClosed: a body that cannot be read
// fails closed with 500 before any claim is taken, so a later retry with a
// readable body executes normally.
func TestIdempotencyHashBody_SpoolErrorFailsClosed(t *testing.T) {
	repo := idemTestRepo(t)
	calls := 0
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusCreated)
	})
	mwh := idempotency(repo, idemSilentLogger(), true)(h)

	req := httptest.NewRequest(http.MethodPut, "/v1/files/a.txt", idemErrReader{})
	req.Header.Set("Idempotency-Key", "key-torn")
	rr := httptest.NewRecorder()
	mwh.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("spool read error must fail closed with 500, got %d", rr.Code)
	}
	if calls != 0 {
		t.Fatalf("handler must not run when the body could not be spooled; calls=%d", calls)
	}

	retry := httptest.NewRequest(http.MethodPut, "/v1/files/a.txt", strings.NewReader("ok"))
	retry.Header.Set("Idempotency-Key", "key-torn")
	rr2 := httptest.NewRecorder()
	mwh.ServeHTTP(rr2, retry)

	if rr2.Code != http.StatusCreated || calls != 1 {
		t.Fatalf("retry after spool failure should run fresh: code=%d calls=%d", rr2.Code, calls)
	}
}

// TestIdempotencyHashBody_NoBody: a request without a body hashes the empty
// byte string — same formula, deterministic, so retries still dedupe.
func TestIdempotencyHashBody_NoBody(t *testing.T) {
	repo := idemTestRepo(t)
	calls := 0
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusNoContent)
	})
	mwh := idempotency(repo, idemSilentLogger(), true)(h)

	do := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodDelete, "/v1/files/a.txt", nil)
		req.Header.Set("Idempotency-Key", "key-empty")
		rr := httptest.NewRecorder()
		mwh.ServeHTTP(rr, req)
		return rr
	}

	do()
	rr2 := do()

	if calls != 1 {
		t.Fatalf("handler should run exactly once, ran %d", calls)
	}
	if rr2.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatal("bodyless retry must replay")
	}
}

// TestIdempotency_HashBodyDisabled_DifferentBodyReplays documents v1 semantics:
// with hashing off the fingerprint ignores the body, so the same key with
// different bytes still replays the stored response.
func TestIdempotency_HashBodyDisabled_DifferentBodyReplays(t *testing.T) {
	repo := idemTestRepo(t)
	calls := 0
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusCreated)
	})
	mwh := idempotency(repo, idemSilentLogger(), false)(h)

	do := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/v1/files/a.txt", strings.NewReader(body))
		req.Header.Set("Idempotency-Key", "key-v1")
		rr := httptest.NewRecorder()
		mwh.ServeHTTP(rr, req)
		return rr
	}

	do("payload-a")
	rr2 := do("payload-b")

	if calls != 1 {
		t.Fatalf("v1 semantics: handler should run exactly once, ran %d", calls)
	}
	if rr2.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatal("v1 semantics: different bytes with the same key still replay")
	}
}
