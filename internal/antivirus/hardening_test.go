package antivirus

// Tests closing the tracked follow-up gaps of the 32 MiB truncation fix:
//   - security F2: the HTTPScanner path warns when the remote engine responded
//     before receiving the full object (the deployment-relevant residual of the
//     fix — the built-in scanner is EICAR-only; production AV runs over HTTP);
//   - QA F3: HTTPScanner error branches (non-2xx / malformed / oversized /
//     transport / bad endpoint);
//   - QA F4: the Worker.Run event→job bridge (enqueue, skips, error, ctx);
//   - QA F8: the defensive maxSigLen==0 branch of the streaming matcher;
//   - QA F9: ScanObjectByID fail-closed error paths (controller missing,
//     unknown object, storage error, tag error, quarantine error).
//
// stdlib-only (I6); helpers reuse setupSvc/quietLogger from antivirus_test.go.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

// ── security F2: remote-side early response is operator-visible ────────────

// TestScanObjectByIDHTTPScannerWarnsOnEarlyResponse pins the F2 hardening: a
// remote engine that answers without reading the request body stops the
// transport, which closes the (closable) storage reader early — fewer bytes
// than obj.Size are consumed, and the worker must emit a warning instead of
// silently tagging the object clean. The object is 32 MiB so the transport's
// socket-buffer read-ahead (observed ≤ ~2 MiB) can never reach EOF first.
func TestScanObjectByIDHTTPScannerWarnsOnEarlyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"clean":true}`)) // respond without reading the body
	}))
	defer srv.Close()

	ctx := context.Background()
	repo, svc := setupSvc(t)
	const size = 32 << 20
	obj, err := svc.Put(ctx, "default", "default", "early.txt", bytes.NewReader(make([]byte, size)), size, service.PutOptions{})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	logs := &captureHandler{}
	w := NewWorker(repo, svc.Storage(), NewHTTPScanner(srv.URL, ""), nil, false, slog.New(logs)).
		WithObjectController(svc)
	if err := w.ScanObjectByID(ctx, obj.ID); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if got := logs.messages(); !contains(got, "antivirus: remote scanner responded before receiving the full object") {
		t.Fatalf("expected early-response warning, logged: %v", got)
	}
}

// TestScanObjectByIDHTTPScannerNoWarnWhenFullBodyConsumed pins the no-false-
// positive direction of F2: when the remote engine reads the whole body before
// answering, every byte is consumed and no warning is emitted (the verdict
// genuinely covered the full object).
func TestScanObjectByIDHTTPScannerNoWarnWhenFullBodyConsumed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte(`{"clean":true}`))
	}))
	defer srv.Close()

	ctx := context.Background()
	repo, svc := setupSvc(t)
	const size = 8 << 20
	obj, err := svc.Put(ctx, "default", "default", "full.txt", bytes.NewReader(make([]byte, size)), size, service.PutOptions{})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	logs := &captureHandler{}
	w := NewWorker(repo, svc.Storage(), NewHTTPScanner(srv.URL, ""), nil, false, slog.New(logs)).
		WithObjectController(svc)
	if err := w.ScanObjectByID(ctx, obj.ID); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if got := logs.messages(); contains(got, "antivirus: remote scanner responded before receiving the full object") {
		t.Fatalf("full-body consumption must not warn, logged: %v", got)
	}
	got, err := repo.GetObject(ctx, "default", "default", "full.txt")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Tags[TagStatus] != "clean" {
		t.Fatalf("expected av_status=clean, got %v", got.Tags)
	}
}

// ── QA F3: HTTPScanner error branches ───────────────────────────────────────

func TestHTTPScannerNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()
	sc := NewHTTPScanner(srv.URL, "")
	if _, err := sc.Scan(context.Background(), strings.NewReader("x")); err == nil ||
		!strings.Contains(err.Error(), "scanner returned 500") {
		t.Fatalf("expected 500 error, got %v", err)
	}
}

func TestHTTPScannerMalformedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()
	sc := NewHTTPScanner(srv.URL, "")
	if _, err := sc.Scan(context.Background(), strings.NewReader("x")); err == nil ||
		!strings.Contains(err.Error(), "malformed scanner response") {
		t.Fatalf("expected malformed-response error, got %v", err)
	}
}

// TestHTTPScannerOversizedResponse pins the 1 MiB response bound (security F4
// fold-in): a hostile endpoint streaming >1 MiB of JSON is cut off by the
// LimitReader, so the decode fails and the scan fails closed — no unbounded
// allocation, no clean verdict from a garbage body.
func TestHTTPScannerOversizedResponse(t *testing.T) {
	blob := `{"clean":false,"signature":"` + strings.Repeat("A", 2<<20) + `"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(blob))
	}))
	defer srv.Close()
	sc := NewHTTPScanner(srv.URL, "")
	if _, err := sc.Scan(context.Background(), strings.NewReader("x")); err == nil ||
		!strings.Contains(err.Error(), "malformed scanner response") {
		t.Fatalf("expected oversized response to fail decode, got %v", err)
	}
}

func TestHTTPScannerTransportError(t *testing.T) {
	// A listener that is closed before the client connects: connection refused.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	sc := NewHTTPScanner("http://"+addr, "")
	if _, err := sc.Scan(context.Background(), strings.NewReader("x")); err == nil {
		t.Fatal("expected transport error, got nil")
	}
}

func TestHTTPScannerBadEndpoint(t *testing.T) {
	// ":" is not a valid URL — NewRequestWithContext fails before any I/O.
	sc := NewHTTPScanner(":", "")
	if _, err := sc.Scan(context.Background(), strings.NewReader("x")); err == nil {
		t.Fatal("expected request-construction error, got nil")
	}
}

// TestHTTPScannerSendsAPIKey pins that a configured key is carried as a Bearer
// header on the scan request (the apiKey branch of Scan).
func TestHTTPScannerSendsAPIKey(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"clean":true}`))
	}))
	defer srv.Close()
	sc := NewHTTPScanner(srv.URL, "sekret")
	if _, err := sc.Scan(context.Background(), strings.NewReader("x")); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if got != "Bearer sekret" {
		t.Fatalf("Authorization = %q, want %q", got, "Bearer sekret")
	}
}

// ── QA F4: Worker.Run event→job bridge ──────────────────────────────────────

// fakeQueue records enqueued jobs (or fails with err).
type fakeQueue struct {
	mu   sync.Mutex
	jobs []repository.Job
	err  error
}

func (q *fakeQueue) Enqueue(ctx context.Context, j repository.Job) (int64, bool, error) {
	if q.err != nil {
		return 0, false, q.err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.jobs = append(q.jobs, j)
	return int64(len(q.jobs)), true, nil
}

func (q *fakeQueue) taken() []repository.Job {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]repository.Job(nil), q.jobs...)
}

func TestWorkerRunBridgesCreatedEvents(t *testing.T) {
	id1, id2 := int64(11), int64(22)
	sub := make(chan repository.Event, 4)
	sub <- repository.Event{Type: repository.EventCreated, TenantID: "t1", ObjectID: &id1}
	sub <- repository.Event{Type: repository.EventDeleted, TenantID: "t1", ObjectID: &id1} // skipped
	sub <- repository.Event{Type: repository.EventCreated, TenantID: "t1"}                 // skipped (nil ObjectID)
	sub <- repository.Event{Type: repository.EventCreated, TenantID: "t2", ObjectID: &id2}
	close(sub)

	q := &fakeQueue{}
	w := NewWorker(nil, nil, nil, q, false, quietLogger())
	w.Run(context.Background(), sub)

	jobs := q.taken()
	if len(jobs) != 2 {
		t.Fatalf("enqueued %d jobs, want 2", len(jobs))
	}
	if jobs[0].TenantID != "t1" || jobs[0].Type != JobScan || jobs[0].DedupeKey != "virus_scan:11" {
		t.Fatalf("job 0 = %+v", jobs[0])
	}
	if got, err := DecodeObjectID(jobs[0].Payload); err != nil || got != id1 {
		t.Fatalf("job 0 payload = %q (%v), want object 11", jobs[0].Payload, err)
	}
	if jobs[1].TenantID != "t2" || jobs[1].Type != JobScan {
		t.Fatalf("job 1 = %+v", jobs[1])
	}
}

func TestWorkerRunCanceledContextReturns(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := NewWorker(nil, nil, nil, &fakeQueue{}, false, quietLogger())
	w.Run(ctx, make(chan repository.Event)) // must return promptly, not block
}

func TestWorkerRunEnqueueErrorWarns(t *testing.T) {
	id := int64(7)
	sub := make(chan repository.Event, 1)
	sub <- repository.Event{Type: repository.EventCreated, TenantID: "t1", ObjectID: &id}
	close(sub)

	logs := &captureHandler{}
	q := &fakeQueue{err: errors.New("queue full")}
	w := NewWorker(nil, nil, nil, q, false, slog.New(logs))
	w.Run(context.Background(), sub)
	if got := logs.messages(); !contains(got, "antivirus: enqueue scan") {
		t.Fatalf("expected enqueue warning, logged: %v", got)
	}
}

// ── QA F8: defensive no-signatures branch of the matcher ────────────────────

// TestSignatureScannerNoSignaturesCertifiesCleanWithoutReading pins the
// maxSigLen==0 guard: a scanner with no signatures (only reachable via the
// zero value; NewSignatureScanner always injects EICAR) certifies clean
// without touching the stream — documented defensive behavior, not a
// truncation path.
func TestSignatureScannerNoSignaturesCertifiesCleanWithoutReading(t *testing.T) {
	s := &SignatureScanner{} // zero value: empty signature map
	var n int64
	stream := &countingReader{r: strings.NewReader("payload"), n: &n}
	res, err := s.Scan(context.Background(), stream)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !res.Clean {
		t.Fatalf("no signatures must certify clean: %+v", res)
	}
	if n != 0 {
		t.Fatalf("stream consumed %d bytes, want 0", n)
	}
}

// ── QA F9: ScanObjectByID fail-closed error paths ───────────────────────────

func TestScanObjectByIDRequiresController(t *testing.T) {
	ctx := context.Background()
	repo, svc := setupSvc(t)
	// nil logger also exercises the NewWorker default-logger branch.
	w := NewWorker(repo, svc.Storage(), NewSignatureScanner(nil), nil, false, nil) // no WithObjectController
	if err := w.ScanObjectByID(ctx, 1); err == nil || !strings.Contains(err.Error(), "object controller is required") {
		t.Fatalf("expected controller-required error, got %v", err)
	}
}

func TestScanObjectByIDUnknownObject(t *testing.T) {
	ctx := context.Background()
	repo, svc := setupSvc(t)
	w := NewWorker(repo, svc.Storage(), NewSignatureScanner(nil), nil, false, quietLogger()).
		WithObjectController(svc)
	if err := w.ScanObjectByID(ctx, 424242); err == nil || !strings.Contains(err.Error(), "get object") {
		t.Fatalf("expected get-object error, got %v", err)
	}
}

// failStore fails every Get.
type failStore struct {
	storage.Storage
	err error
}

func (f *failStore) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectInfo, error) {
	return nil, storage.ObjectInfo{}, f.err
}

func TestScanObjectByIDStorageGetError(t *testing.T) {
	ctx := context.Background()
	repo, svc := setupSvc(t)
	obj, err := svc.Put(ctx, "default", "default", "storerr.txt", strings.NewReader("x"), 1, service.PutOptions{})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	boom := errors.New("disk on fire")
	w := NewWorker(repo, &failStore{Storage: svc.Storage(), err: boom}, NewSignatureScanner(nil), nil, false, quietLogger()).
		WithObjectController(svc)
	if err := w.ScanObjectByID(ctx, obj.ID); !errors.Is(err, boom) {
		t.Fatalf("storage error must propagate, got %v", err)
	}
}

// failController delegates to FileService but fails the configured step.
type failController struct {
	*service.FileService
	tagErr  error
	quarErr error
}

func (f *failController) SetObjectTagsByID(ctx context.Context, objectID int64, tags map[string]string) error {
	if f.tagErr != nil {
		return f.tagErr
	}
	return f.FileService.SetObjectTagsByID(ctx, objectID, tags)
}

func (f *failController) QuarantineObjectByID(ctx context.Context, objectID int64, signature string) error {
	if f.quarErr != nil {
		return f.quarErr
	}
	return f.FileService.QuarantineObjectByID(ctx, objectID, signature)
}

func TestScanObjectByIDTagWriteError(t *testing.T) {
	ctx := context.Background()
	repo, svc := setupSvc(t)
	obj, err := svc.Put(ctx, "default", "default", "tagerr.txt", strings.NewReader("x"), 1, service.PutOptions{})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	boom := errors.New("tag write denied")
	w := NewWorker(repo, svc.Storage(), NewSignatureScanner(nil), nil, false, quietLogger()).
		WithObjectController(&failController{FileService: svc, tagErr: boom})
	if err := w.ScanObjectByID(ctx, obj.ID); !errors.Is(err, boom) {
		t.Fatalf("tag error must propagate, got %v", err)
	}
}

func TestScanObjectByIDQuarantineError(t *testing.T) {
	ctx := context.Background()
	repo, svc := setupSvc(t)
	obj, err := svc.Put(ctx, "default", "default", "quarerr.txt", strings.NewReader(EICAR), int64(len(EICAR)), service.PutOptions{})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	boom := errors.New("quarantine failed")
	w := NewWorker(repo, svc.Storage(), NewSignatureScanner(nil), nil, true, quietLogger()).
		WithObjectController(&failController{FileService: svc, quarErr: boom})
	if err := w.ScanObjectByID(ctx, obj.ID); !errors.Is(err, boom) {
		t.Fatalf("quarantine error must propagate, got %v", err)
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────

// captureHandler records slog records in memory.
type captureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *captureHandler) Enabled(ctx context.Context, l slog.Level) bool { return true }

func (h *captureHandler) Handle(ctx context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *captureHandler) WithAttrs(attrs []slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(name string) slog.Handler       { return h }

func (h *captureHandler) messages() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.records))
	for _, r := range h.records {
		out = append(out, r.Message)
	}
	return out
}

func contains(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}
