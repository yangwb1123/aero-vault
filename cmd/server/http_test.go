package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/storage"
)

func TestRedirectWebUI(t *testing.T) {
	rec := httptest.NewRecorder()
	redirectWebUI(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusFound)
	}
	if location := rec.Header().Get("Location"); location != "/ui/" {
		t.Fatalf("Location=%q want /ui/", location)
	}
}

// stubReadyRepo embeds repository.Repository and only overrides Ping, the sole
// method reachable by readyzHandler (partial-stub idiom, cf. deleteFailStorage).
type stubReadyRepo struct {
	repository.Repository
}

func (s *stubReadyRepo) Ping(context.Context) error { return nil }

// blockingStatStorage emulates a wedged object store: Stat returns only when the
// caller context is done, mimicking backends whose Stat is a live HEAD with no
// internal deadline (S3 HeadObject, OSS/COS Head).
type blockingStatStorage struct {
	storage.Storage
}

func (s *blockingStatStorage) Stat(ctx context.Context, _ string) (storage.ObjectInfo, error) {
	<-ctx.Done()
	return storage.ObjectInfo{}, ctx.Err()
}

// notFoundStatStorage answers the probe key as absent — the healthy-store path.
type notFoundStatStorage struct {
	storage.Storage
}

func (s *notFoundStatStorage) Stat(context.Context, string) (storage.ObjectInfo, error) {
	return storage.ObjectInfo{}, storage.ErrNotFound
}

// errStatStorage fails the probe immediately with a non-NotFound error.
type errStatStorage struct {
	storage.Storage
}

func (s *errStatStorage) Stat(context.Context, string) (storage.ObjectInfo, error) {
	return storage.ObjectInfo{}, errors.New("injected probe failure")
}

// TestReadyzStorageProbeTimeout proves a wedged store yields 503 within ~2s:
// the blocking stub can only return after the probe deadline fires, so the
// response cannot precede it; the upper bound is the "within ~2s" claim made
// timing-robust against loaded CI.
func TestReadyzStorageProbeTimeout(t *testing.T) {
	h := readyzHandler(&stubReadyRepo{}, &blockingStatStorage{}, nil)
	rec := httptest.NewRecorder()
	start := time.Now()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	elapsed := time.Since(start)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if body := rec.Body.String(); !strings.Contains(body, "storage unavailable") {
		t.Fatalf("body=%q want it to contain %q", body, "storage unavailable")
	}
	if elapsed < time.Second {
		t.Fatalf("elapsed=%v: response cannot precede the 2s probe deadline", elapsed)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("elapsed=%v: probe deadline did not bound the blocked Stat", elapsed)
	}
}

// TestReadyzErrNotFoundIsReady guards the only behavioral fork on the probe
// line: an absent probe key (ErrNotFound) must still yield 200 without waiting
// out the deadline.
func TestReadyzErrNotFoundIsReady(t *testing.T) {
	h := readyzHandler(&stubReadyRepo{}, &notFoundStatStorage{}, nil)
	rec := httptest.NewRecorder()
	start := time.Now()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	elapsed := time.Since(start)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusOK)
	}
	if body := rec.Body.String(); body != `{"ok":true}` {
		t.Fatalf("body=%q want %q", body, `{"ok":true}`)
	}
	if elapsed >= time.Second {
		t.Fatalf("elapsed=%v: ErrNotFound must short-circuit, not wait out the deadline", elapsed)
	}
}

// TestReadyzImmediateStorageError pins that the deadline wrap neither delays
// nor swallows non-deadline errors from the probe.
func TestReadyzImmediateStorageError(t *testing.T) {
	h := readyzHandler(&stubReadyRepo{}, &errStatStorage{}, nil)
	rec := httptest.NewRecorder()
	start := time.Now()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	elapsed := time.Since(start)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if body := rec.Body.String(); !strings.Contains(body, "storage unavailable") {
		t.Fatalf("body=%q want it to contain %q", body, "storage unavailable")
	}
	if elapsed >= time.Second {
		t.Fatalf("elapsed=%v: immediate probe error must not be delayed by the wrap", elapsed)
	}
}

// blockingPingRepo is stubReadyRepo's wedge variant: Ping blocks on the
// caller context (a wedged database) — the H2 probe-timeout shape.
type blockingPingRepo struct {
	stubReadyRepo
}

func (s *blockingPingRepo) Ping(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

// TestReadyzPingProbeTimeout (H2) proves a wedged database yields 503
// "database unavailable" within ~2s: the blocking Ping can only return after
// the readyzProbeTimeout bound fires (mirror of TestReadyzStorageProbeTimeout).
func TestReadyzPingProbeTimeout(t *testing.T) {
	h := readyzHandler(&blockingPingRepo{}, &notFoundStatStorage{}, nil)
	rec := httptest.NewRecorder()
	start := time.Now()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	elapsed := time.Since(start)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if body := rec.Body.String(); !strings.Contains(body, "database unavailable") {
		t.Fatalf("body=%q want it to contain %q", body, "database unavailable")
	}
	if elapsed < time.Second {
		t.Fatalf("elapsed=%v: response cannot precede the 2s ping bound", elapsed)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("elapsed=%v: ping bound did not contain the blocked Ping", elapsed)
	}
}

// degradedFakeExtra is a readinessChecker + degradedChecker fake: it answers
// Ready immediately and reports a scripted degraded flag and backlog age.
type degradedFakeExtra struct {
	degraded   bool
	backlogAge time.Duration
}

func (f *degradedFakeExtra) Ready(context.Context) error { return nil }
func (f *degradedFakeExtra) Degraded() bool              { return f.degraded }
func (f *degradedFakeExtra) BacklogAge() time.Duration   { return f.backlogAge }

// TestReadyzDegradedExtraReturns200WithMarker (T2) pins the only new wire
// form: a degraded extra → HTTP 200 with the exact marker body, never 503.
func TestReadyzDegradedExtraReturns200WithMarker(t *testing.T) {
	h := readyzHandler(&stubReadyRepo{}, &notFoundStatStorage{},
		&degradedFakeExtra{degraded: true, backlogAge: 123 * time.Second})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want %d (degraded is a payload, not a 503)", rec.Code, http.StatusOK)
	}
	if body := rec.Body.String(); body != `{"ok":true,"degraded":true,"backlog_age_seconds":123}` {
		t.Fatalf("body=%q want the exact degraded marker body", body)
	}
}

// TestReadyzHealthyExtraReturns200Unchanged pins the healthy wire contract:
// the same fake with Degraded=false yields the byte-identical {"ok":true}
// (http_test.go:103 idiom preserved).
func TestReadyzHealthyExtraReturns200Unchanged(t *testing.T) {
	h := readyzHandler(&stubReadyRepo{}, &notFoundStatStorage{},
		&degradedFakeExtra{degraded: false, backlogAge: 123 * time.Second})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusOK)
	}
	if body := rec.Body.String(); body != `{"ok":true}` {
		t.Fatalf("body=%q want %q (byte-identical healthy body)", body, `{"ok":true}`)
	}
}

// TestReadyzGroupDegradedComposition (T2-add) pins the readinessGroup
// composition: Degraded is OR over degradedChecker members (non-implementers
// contribute false) and BacklogAge is the max over implementing members.
func TestReadyzGroupDegradedComposition(t *testing.T) {
	group := readinessGroup{
		&degradedFakeExtra{degraded: true, backlogAge: 123 * time.Second},
		&okReadyChecker{},
		&degradedFakeExtra{degraded: false, backlogAge: 30 * time.Second},
	}
	if !group.Degraded() {
		t.Fatal("group.Degraded()=false, want OR over members = true")
	}
	if got := group.BacklogAge(); got != 123*time.Second {
		t.Fatalf("group.BacklogAge()=%v want max 123s", got)
	}

	allHealthy := readinessGroup{
		&degradedFakeExtra{degraded: false, backlogAge: 0},
		&okReadyChecker{},
	}
	if allHealthy.Degraded() {
		t.Fatal("all-healthy group must not report degraded")
	}
	if got := allHealthy.BacklogAge(); got != 0 {
		t.Fatalf("all-healthy group BacklogAge()=%v want 0", got)
	}
}

// TestReadyzGroupReadyFailPropagates (T2-add) pins that group Ready stays
// AND: a member hard-error still 503s "runtime dependency unavailable".
func TestReadyzGroupReadyFailPropagates(t *testing.T) {
	h := readyzHandler(&stubReadyRepo{}, &notFoundStatStorage{}, readinessGroup{
		&degradedFakeExtra{degraded: true, backlogAge: 123 * time.Second},
		&errorReadyChecker{},
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want %d (member hard-error still 503s)", rec.Code, http.StatusServiceUnavailable)
	}
	if body := rec.Body.String(); !strings.Contains(body, "runtime dependency unavailable") {
		t.Fatalf("body=%q want it to contain %q", body, "runtime dependency unavailable")
	}
}

// TestReadyzGroupEmpty (T2-add) pins the empty group: Degraded false,
// BacklogAge 0, and the seam answers the healthy byte-identical body.
func TestReadyzGroupEmpty(t *testing.T) {
	group := readinessGroup{}
	if group.Degraded() {
		t.Fatal("empty group must not report degraded")
	}
	if got := group.BacklogAge(); got != 0 {
		t.Fatalf("empty group BacklogAge()=%v want 0", got)
	}
	h := readyzHandler(&stubReadyRepo{}, &notFoundStatStorage{}, group)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != `{"ok":true}` {
		t.Fatalf("empty group: code=%d body=%q want 200 %q",
			rec.Code, rec.Body.String(), `{"ok":true}`)
	}
}

// TestHelmReadinessProbeTimeoutSeconds (T8, H1) string-pins the helm
// readinessProbe block: it must carry timeoutSeconds: 10 (the degraded-path
// worst case is ping 2s + storage 2s + audit probes 2s = 6s < 10s) and must
// NOT carry a failureThreshold key — a future failureThreshold: 1 would
// silently re-enable fast eviction (NotReady after one failure ≈ 10s) and
// defeat H1. The file is Go-templated, so the pin is textual, scoped to the
// readinessProbe block (the startupProbe legitimately has failureThreshold).
func TestHelmReadinessProbeTimeoutSeconds(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "deploy", "helm", "aero-vault", "templates", "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	const marker = "readinessProbe:"
	idx := strings.Index(string(data), marker)
	if idx < 0 {
		t.Fatalf("deployment.yaml missing %q", marker)
	}
	block := string(data)[idx:]
	if next := strings.Index(block, "startupProbe:"); next >= 0 {
		block = block[:next]
	}
	if !strings.Contains(block, "timeoutSeconds: 10") {
		t.Fatalf("readinessProbe block missing timeoutSeconds: 10:\n%s", block)
	}
	if strings.Contains(block, "failureThreshold") {
		t.Fatalf("readinessProbe block must not carry a failureThreshold key:\n%s", block)
	}
}
