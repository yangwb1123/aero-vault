package antivirus

// Tests for the >32 MiB truncation fix (signature-scanner-32mib-truncation).
// SignatureScanner.Scan now streams the whole object through a sliding window
// (Clean only after EOF), and the worker drains the remainder only for the
// HTTPScanner. Every acceptance check in the spec maps to a named test here;
// helpers setupSvc/quietLogger come from antivirus_test.go.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

// ── AC-1: tail beyond 32 MiB is scanned, never reported clean ──────────────

func TestSignatureScannerTailBeyond32MiB(t *testing.T) {
	s := NewSignatureScanner(nil)
	var n int64
	stream := &countingReader{
		r: io.MultiReader(bytes.NewReader(make([]byte, 32<<20)), strings.NewReader(EICAR)),
		n: &n,
	}
	res, err := s.Scan(context.Background(), stream)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if res.Clean || res.Signature != "EICAR-Test-File" {
		t.Fatalf("EICAR beyond 32 MiB not detected: %+v", res)
	}
	if n != int64(32<<20+len(EICAR)) {
		t.Fatalf("stream consumed %d bytes, want the full %d", n, 32<<20+len(EICAR))
	}
}

// ── AC-2: ScanObjectByID on a >32 MiB tail-infected object ──────────────────

// TestScanObjectByIDTailEICARNeverClean_QuarantineOn pins the security
// property: tail malware is quarantined exactly like a small infected object
// (soft-delete + quota released), never tagged clean.
func TestScanObjectByIDTailEICARNeverClean_QuarantineOn(t *testing.T) {
	ctx := context.Background()
	repo, svc := setupSvc(t)
	size := int64(32<<20 + len(EICAR))
	body := io.MultiReader(bytes.NewReader(make([]byte, 32<<20)), strings.NewReader(EICAR))
	obj, err := svc.Put(ctx, "default", "default", "tail-bad.txt", body, size, service.PutOptions{})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	w := NewWorker(repo, svc.Storage(), NewSignatureScanner(nil), nil, true, quietLogger()).
		WithObjectController(svc)
	if err := w.ScanObjectByID(ctx, obj.ID); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if _, err := repo.GetObject(ctx, "default", "default", "tail-bad.txt"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("tail-infected object should be quarantined, got err=%v", err)
	}
	quota, err := repo.GetTenantQuota(ctx, "default")
	if err != nil {
		t.Fatalf("quota: %v", err)
	}
	if quota.UsedBytes != 0 || quota.UsedObjects != 0 {
		t.Fatalf("quarantine left usage at %d bytes/%d objects", quota.UsedBytes, quota.UsedObjects)
	}
}

// TestScanObjectByIDTailEICARNeverClean_NoQuarantine pins the fail-closed
// verdict: without quarantine the object stays but is tagged infected with the
// signature — never av_status=clean.
func TestScanObjectByIDTailEICARNeverClean_NoQuarantine(t *testing.T) {
	ctx := context.Background()
	repo, svc := setupSvc(t)
	size := int64(32<<20 + len(EICAR))
	body := io.MultiReader(bytes.NewReader(make([]byte, 32<<20)), strings.NewReader(EICAR))
	obj, err := svc.Put(ctx, "default", "default", "tail-bad2.txt", body, size, service.PutOptions{})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	w := NewWorker(repo, svc.Storage(), NewSignatureScanner(nil), nil, false, quietLogger()).
		WithObjectController(svc)
	if err := w.ScanObjectByID(ctx, obj.ID); err != nil {
		t.Fatalf("scan: %v", err)
	}
	got, err := repo.GetObject(ctx, "default", "default", "tail-bad2.txt")
	if err != nil {
		t.Fatalf("object should remain (no quarantine): %v", err)
	}
	if got.Tags[TagStatus] != "infected" || got.Tags[TagSignature] != "EICAR-Test-File" {
		t.Fatalf("expected infected tags, got %v", got.Tags)
	}
}

// ── AC-3b: no drain on the signature path ───────────────────────────────────

// TestScanObjectByIDNoDrainForNonHTTPScanner pins that a scanner which returns
// before EOF (here: reads exactly 1 KiB) causes no extra storage reads — the
// unconditional drain previously re-read the whole remainder for nothing.
func TestScanObjectByIDNoDrainForNonHTTPScanner(t *testing.T) {
	ctx := context.Background()
	repo, svc := setupSvc(t)
	const size = 4096
	obj, err := svc.Put(ctx, "default", "default", "head.txt", bytes.NewReader(make([]byte, size)), size, service.PutOptions{})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	store := &countingStore{Storage: svc.Storage()}
	w := NewWorker(repo, store, &headOnlyScanner{n: 1024}, nil, false, quietLogger()).
		WithObjectController(svc)
	if err := w.ScanObjectByID(ctx, obj.ID); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if got := store.reads.Load(); got != 1024 {
		t.Fatalf("storage bytes read = %d, want 1024 (scanner consumption only, no drain)", got)
	}
	got, err := repo.GetObject(ctx, "default", "default", "head.txt")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Tags[TagStatus] != "clean" {
		t.Fatalf("expected av_status=clean, got %v", got.Tags)
	}
}

// ── AC-3c: drain preserved on the HTTPScanner path ──────────────────────────

// TestScanObjectByIDHTTPScannerConsumesWholeStream pins the HTTPScanner-path
// drain: the remote service responds without reading the request body, so the
// transport stops consuming early, and the worker's drain must read the
// remainder — the whole object is consumed from storage either way.
//
// The store must hand out no-op-close readers: Go's HTTP transport closes an
// unread request body when the remote responds early, so with a real closable
// (file-backed) reader the drain would read nothing — making "drain present"
// indistinguishable from "no drain". A no-op-close body keeps the remainder
// readable, so the drain deterministically consumes it (design §1 empirical
// harness shape; in production the drain is client-side hygiene only).
func TestScanObjectByIDHTTPScannerConsumesWholeStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"clean":true}`)) // respond without reading the body
	}))
	defer srv.Close()

	ctx := context.Background()
	repo, svc := setupSvc(t)
	const size = 8 << 20
	obj, err := svc.Put(ctx, "default", "default", "http.txt", bytes.NewReader(make([]byte, size)), size, service.PutOptions{})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	store := &noCloseCountingStore{Storage: svc.Storage()}
	w := NewWorker(repo, store, NewHTTPScanner(srv.URL, ""), nil, false, quietLogger()).
		WithObjectController(svc)
	if err := w.ScanObjectByID(ctx, obj.ID); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if got := store.reads.Load(); got != size {
		t.Fatalf("storage bytes read = %d, want %d (whole stream consumed on the HTTP path)", got, size)
	}
}

// ── Design-pinning unit tests (guard the matcher algorithm) ─────────────────

// TestSignatureScannerSplitAcrossChunkBoundaries pins that a signature
// straddling read boundaries is still detected: the 3-byte prefix misaligns
// EICAR against every 7-byte read.
func TestSignatureScannerSplitAcrossChunkBoundaries(t *testing.T) {
	s := NewSignatureScanner(nil)
	stream := io.MultiReader(strings.NewReader("xxx"), strings.NewReader(EICAR))
	res, err := s.Scan(context.Background(), &tinyReader{r: stream, n: 7})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if res.Clean || res.Signature != "EICAR-Test-File" {
		t.Fatalf("EICAR across chunk boundaries not detected: %+v", res)
	}
}

// TestSignatureScannerCleanLargeFullyConsumed pins that a large clean object is
// reported clean only after the whole stream is consumed (100% read).
func TestSignatureScannerCleanLargeFullyConsumed(t *testing.T) {
	s := NewSignatureScanner(nil)
	var n int64
	stream := &countingReader{r: bytes.NewReader(make([]byte, 33<<20)), n: &n}
	res, err := s.Scan(context.Background(), stream)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !res.Clean {
		t.Fatalf("clean 33 MiB stream flagged: %+v", res)
	}
	if n != int64(33<<20) {
		t.Fatalf("stream consumed %d bytes, want %d (clean only after EOF)", n, int64(33<<20))
	}
}

func TestSignatureScannerEmptyStream(t *testing.T) {
	s := NewSignatureScanner(nil)
	res, err := s.Scan(context.Background(), strings.NewReader(""))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !res.Clean {
		t.Fatalf("empty stream flagged: %+v", res)
	}
}

func TestSignatureScannerCanceledContext(t *testing.T) {
	s := NewSignatureScanner(nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Scan(ctx, strings.NewReader("x")); err == nil {
		t.Fatal("expected error for canceled context, got nil")
	}
}

func TestSignatureScannerCustomSignatureInTail(t *testing.T) {
	s := NewSignatureScanner(map[string]string{"Custom-Tail": "TAIL-MARKER"})
	res, err := s.Scan(context.Background(),
		io.MultiReader(bytes.NewReader(make([]byte, 32<<20)), strings.NewReader("TAIL-MARKER")))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if res.Clean || res.Signature != "Custom-Tail" {
		t.Fatalf("custom tail signature not detected: %+v", res)
	}
}

// ── QA must-fix F1/F2: fail-closed error paths ───────────────────────────────

// errorScanner fails every scan — models an engine outage (QA finding F1: the
// worker scan-error branch must propagate the error and write no tag; without
// this pin, a regression here would silently certify half-scanned objects).
type errorScanner struct{ err error }

func (e *errorScanner) Scan(ctx context.Context, r io.Reader) (Result, error) {
	return Result{}, e.err
}

func (e *errorScanner) Name() string { return "error" }

// TestScanObjectByIDScanErrorWritesNoTag pins the fail-closed property on the
// worker path: when the scanner errors, ScanObjectByID propagates the error and
// must not write av_status/av_signature — an object that was never fully
// scanned is never certified clean or infected.
func TestScanObjectByIDScanErrorWritesNoTag(t *testing.T) {
	ctx := context.Background()
	repo, svc := setupSvc(t)
	obj, err := svc.Put(ctx, "default", "default", "scan-err.txt", strings.NewReader("payload"), 7, service.PutOptions{})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	boom := errors.New("engine down")
	w := NewWorker(repo, svc.Storage(), &errorScanner{err: boom}, nil, false, quietLogger()).
		WithObjectController(svc)
	if err := w.ScanObjectByID(ctx, obj.ID); !errors.Is(err, boom) {
		t.Fatalf("scan error must propagate, got %v", err)
	}
	got, err := repo.GetObject(ctx, "default", "default", "scan-err.txt")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if _, ok := got.Tags[TagStatus]; ok {
		t.Fatalf("scan error must not write av_status, got %v", got.Tags)
	}
	if _, ok := got.Tags[TagSignature]; ok {
		t.Fatalf("scan error must not write av_signature, got %v", got.Tags)
	}
}

// errReader yields chunk once, then a non-EOF error — a reader that fails
// mid-stream (QA finding F2: the only matcher path that could still produce a
// false clean if it swallowed the error).
type errReader struct {
	chunk []byte
	err   error
	sent  bool
}

func (r *errReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, r.chunk), nil
	}
	return 0, r.err
}

// TestSignatureScannerReadErrorMidStream pins fail-closed at the matcher: a
// mid-stream read error propagates (so the worker writes no tag), never a clean
// verdict.
func TestSignatureScannerReadErrorMidStream(t *testing.T) {
	s := NewSignatureScanner(nil)
	boom := errors.New("storage read failed")
	stream := &errReader{chunk: make([]byte, 64<<10), err: boom}
	if _, err := s.Scan(context.Background(), stream); !errors.Is(err, boom) {
		t.Fatalf("mid-stream read error must propagate, got %v", err)
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────

// countingReader counts every byte pulled from the wrapped reader.
type countingReader struct {
	r io.Reader
	n *int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	*c.n += int64(n)
	return n, err
}

// tinyReader limits each read to n bytes, forcing chunk-boundary splits.
type tinyReader struct {
	r io.Reader
	n int
}

func (t *tinyReader) Read(p []byte) (int, error) {
	if len(p) > t.n {
		p = p[:t.n]
	}
	return t.r.Read(p)
}

// countingStore wraps a Storage and counts every byte read through Get.
type countingStore struct {
	storage.Storage
	reads atomic.Int64
}

func (c *countingStore) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectInfo, error) {
	rc, info, err := c.Storage.Get(ctx, key)
	if err != nil {
		return nil, info, err
	}
	return &countingReadCloser{ReadCloser: rc, n: &c.reads}, info, nil
}

// noCloseCountingStore is countingStore whose Get readers have a no-op Close
// (see TestScanObjectByIDHTTPScannerConsumesWholeStream for why).
type noCloseCountingStore struct {
	storage.Storage
	reads atomic.Int64
}

func (c *noCloseCountingStore) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectInfo, error) {
	rc, info, err := c.Storage.Get(ctx, key)
	if err != nil {
		return nil, info, err
	}
	return &countingReadCloser{ReadCloser: io.NopCloser(rc), n: &c.reads}, info, nil
}

type countingReadCloser struct {
	io.ReadCloser
	n *atomic.Int64
}

func (c *countingReadCloser) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// headOnlyScanner consumes exactly n bytes and reports clean — models any
// scanner that returns before reaching EOF (only the signature scanner's
// sibling HTTP path may rely on a worker-side drain).
type headOnlyScanner struct{ n int }

func (h *headOnlyScanner) Scan(ctx context.Context, r io.Reader) (Result, error) {
	buf := make([]byte, h.n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return Result{}, err
	}
	return Result{Clean: true}, nil
}

func (h *headOnlyScanner) Name() string { return "head-only" }
