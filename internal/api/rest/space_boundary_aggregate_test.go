package rest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/access"
	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/spaces"
	"github.com/aero-vault/aero-vault/internal/storage"
	"github.com/go-chi/chi/v5"
)

type aggregateCoverage func(context.Context, spaces.CoverageScope) (spaces.CoverageResult, error)

func (f aggregateCoverage) ProbeSpaceCoverage(ctx context.Context, scope spaces.CoverageScope) (spaces.CoverageResult, error) {
	return f(ctx, scope)
}

type aggregateJobReader func(context.Context, int64) (repository.JobLookup, error)

func (f aggregateJobReader) ReadJob(ctx context.Context, id int64) (repository.JobLookup, error) {
	return f(ctx, id)
}

type aggregateRepository struct {
	repository.Repository
	trace           []string
	after           func(string)
	retries, audits int
}

func (p *aggregateRepository) mark(stage string) {
	p.trace = append(p.trace, stage)
	if p.after != nil {
		p.after(stage)
	}
}

func (p *aggregateRepository) GetTenantQuota(ctx context.Context, tenant string) (repository.TenantQuota, error) {
	row, err := p.Repository.GetTenantQuota(ctx, tenant)
	p.mark("quota")
	return row, err
}

func (p *aggregateRepository) ListBuckets(ctx context.Context, tenant string) ([]string, error) {
	rows, err := p.Repository.ListBuckets(ctx, tenant)
	p.mark("buckets")
	return rows, err
}

func (p *aggregateRepository) GetTenant(ctx context.Context, tenant string) (repository.TenantRecord, bool, error) {
	row, known, err := p.Repository.GetTenant(ctx, tenant)
	p.mark("tenant")
	return row, known, err
}

func (p *aggregateRepository) ListAudit(ctx context.Context, limit int) ([]repository.AuditEntry, error) {
	rows, err := p.Repository.ListAudit(ctx, limit)
	p.mark(fmt.Sprintf("audit:%d", limit))
	return rows, err
}

func (p *aggregateRepository) ListWebhookFailures(ctx context.Context, limit int) ([]repository.WebhookFailure, error) {
	rows, err := p.Repository.ListWebhookFailures(ctx, limit)
	p.mark(fmt.Sprintf("failures:%d", limit))
	return rows, err
}

func (p *aggregateRepository) ListJobs(ctx context.Context, status, kind string, limit int) ([]repository.Job, error) {
	rows, err := p.Repository.ListJobs(ctx, status, kind, limit)
	p.mark(fmt.Sprintf("jobs:%s:%s:%d", status, kind, limit))
	return rows, err
}

func (p *aggregateRepository) JobStats(ctx context.Context) (map[string]int64, error) {
	rows, err := p.Repository.JobStats(ctx)
	p.mark("stats")
	return rows, err
}

func (p *aggregateRepository) RecordAudit(ctx context.Context, entry repository.AuditEntry) error {
	p.audits++
	p.mark("write-audit")
	return p.Repository.RecordAudit(ctx, entry)
}

func (p *aggregateRepository) RetryJob(ctx context.Context, id int64, reason string, after time.Time) error {
	p.retries++
	return p.Repository.RetryJob(ctx, id, reason, after)
}

func aggregateFixture(t *testing.T) (repository.Repository, storage.Storage) {
	t.Helper()
	dir := t.TempDir()
	repo, err := repository.Open(t.Context(), "sqlite", "file:"+filepath.Join(dir, "aggregate.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatal(err)
	}
	return repo, store
}

func aggregateBind(t *testing.T, repo repository.Repository, tenant string) spaces.Space {
	t.Helper()
	if err := repo.UpsertTenant(t.Context(), repository.TenantRecord{TenantID: tenant, Status: "active"}); err != nil {
		t.Fatal(err)
	}
	space, err := repo.(spaces.Store).CreateSpace(t.Context(), spaces.CreateCommand{
		TenantID: tenant, ID: "94efb08b-813a-4e63-b766-207b46578e38", Kind: spaces.Team,
		Name: "private", CommandID: "4a3a8959-b738-427c-9f29-87efe2d9a516",
		Actor: spaces.SubjectRef{Source: "test", Kind: spaces.User, ID: "owner"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return space
}

func aggregateHandler(p *aggregateRepository, raw repository.Repository, store storage.Storage) *AdminHandler {
	readers := discoverAdminResourceReaders(raw)
	coverage := readers.Coverage
	readers.Coverage = aggregateCoverage(func(ctx context.Context, scope spaces.CoverageScope) (spaces.CoverageResult, error) {
		p.mark("coverage")
		return coverage.ProbeSpaceCoverage(ctx, scope)
	})
	return NewAdminHandler(service.NewFileService(store, p, nil), p, nil, readers)
}

var aggregateStages = map[string][]string{
	"usage": {"quota"}, "audit": {"audit:7"}, "failures": {"failures:7"},
	"jobs": {"jobs:failed:index_object:7", "stats"}, "summary": {"buckets", "quota", "tenant"},
}

func aggregateRequest(h *AdminHandler, surface string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/?account_id=account&limit=7&status=failed&type=index_object", nil)
	r.Header.Set(mw.TenantHeader, "alpha")
	r.Header.Set("X-Aero-Canonical-UID", "canonical")
	r = r.WithContext(access.WithPrincipal(r.Context(), access.Principal{
		Kind: access.PrincipalService, TenantID: "alpha", SubjectID: "source", Scopes: []string{"admin"},
	}))
	methods := map[string]http.HandlerFunc{
		"usage": h.Usage, "audit": h.ListAudit, "failures": h.ListWebhookFailures,
		"jobs": h.ListJobs, "summary": h.AccountSummary,
	}
	w := httptest.NewRecorder()
	mw.Tenant(methods[surface]).ServeHTTP(w, r)
	return w
}

func TestSpaceAggregateEmptyMaterializationAndFreshness(t *testing.T) {
	for surface, stages := range aggregateStages {
		t.Run(surface, func(t *testing.T) {
			raw, store := aggregateFixture(t)
			p := &aggregateRepository{Repository: raw}
			h := aggregateHandler(p, raw, store)
			for range 2 {
				p.trace = nil
				w := aggregateRequest(h, surface)
				if w.Code != http.StatusOK {
					t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
				}
				want := append(append([]string{"coverage"}, stages...), "coverage")
				if surface == "summary" {
					want = append(want, "write-audit")
					if strings.Contains(w.Body.String(), `"status":"active"`) || !strings.Contains(w.Body.String(), `"complete":true`) {
						t.Fatalf("unknown tenant fabricated active or incomplete: %s", w.Body.String())
					}
				}
				if !reflect.DeepEqual(p.trace, want) {
					t.Fatalf("trace=%v want=%v", p.trace, want)
				}
			}
		})
	}
}

func TestSpaceAggregateRealBindingRaces(t *testing.T) {
	for surface, stages := range aggregateStages {
		for _, before := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/before=%t", surface, before), func(t *testing.T) {
				raw, store := aggregateFixture(t)
				p := &aggregateRepository{Repository: raw}
				h := aggregateHandler(p, raw, store)
				if before {
					aggregateBind(t, raw, "alpha")
				} else {
					p.after = func(stage string) {
						if stage == stages[len(stages)-1] {
							aggregateBind(t, raw, "alpha")
						}
					}
				}
				w := aggregateRequest(h, surface)
				if w.Code != http.StatusForbidden || p.audits != 0 || strings.Contains(w.Body.String(), `"complete"`) {
					t.Fatalf("status=%d audits=%d body=%s", w.Code, p.audits, w.Body.String())
				}
				want := []string{"coverage"}
				if !before {
					want = append(append(want, stages...), "coverage")
				}
				if !reflect.DeepEqual(p.trace, want) {
					t.Fatalf("trace=%v want=%v", p.trace, want)
				}
			})
		}
	}
}

func TestSpaceAggregateFaultsNeverBecomeEmptySuccess(t *testing.T) {
	raw, store := aggregateFixture(t)
	for surface := range aggregateStages {
		for _, mode := range []string{"missing", "typed-nil", "fault-p0", "fault-p1", "unknown", "echo", "deadline"} {
			t.Run(surface+"/"+mode, func(t *testing.T) {
				p := &aggregateRepository{Repository: raw}
				readers := discoverAdminResourceReaders(raw)
				calls := 0
				readers.Coverage = aggregateCoverage(func(ctx context.Context, scope spaces.CoverageScope) (spaces.CoverageResult, error) {
					calls++
					if calls == 2 || mode == "fault-p0" {
						return aggregateFaultObservation(scope, mode)
					}
					return raw.(spaces.CoverageReader).ProbeSpaceCoverage(ctx, scope)
				})
				if mode == "missing" {
					readers.Coverage = nil
				} else if mode == "typed-nil" {
					readers.Coverage = aggregateCoverage(nil)
				}
				h := NewAdminHandler(service.NewFileService(store, p, nil), p, nil, readers)
				w := aggregateRequest(h, surface)
				want := http.StatusInternalServerError
				if mode == "deadline" {
					want = http.StatusGatewayTimeout
				}
				aggregateCheckFailure(t, w, want, p.audits)
			})
		}
	}
}

func aggregateCheckFailure(t *testing.T, w *httptest.ResponseRecorder, want, audits int) {
	t.Helper()
	if w.Code != want || audits != 0 || strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("status=%d audits=%d body=%s", w.Code, audits, w.Body.String())
	}
}

func aggregateFaultObservation(scope spaces.CoverageScope, mode string) (spaces.CoverageResult, error) {
	switch mode {
	case "unknown":
		return spaces.CoverageResult{Scope: scope, State: spaces.CoverageUnknown}, nil
	case "echo":
		return spaces.CoverageResult{Scope: spaces.CoverageScope{Kind: spaces.CoverageTenant, TenantID: "other"}, State: spaces.CoverageUnbound, Complete: true}, nil
	case "deadline":
		return spaces.CoverageResult{}, errors.Join(context.DeadlineExceeded, repository.ErrNotFound, errors.New("secret"))
	default:
		return spaces.CoverageResult{}, errors.Join(repository.ErrNotFound, access.ErrDenied, errors.New("secret"))
	}
}

func TestSpaceAggregateMaterializationFault(t *testing.T) {
	raw, store := aggregateFixture(t)
	p := &aggregateRepository{Repository: raw}
	p.after = func(stage string) {
		if stage == "buckets" {
			if err := raw.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
	w := aggregateRequest(aggregateHandler(p, raw, store), "summary")
	aggregateCheckFailure(t, w, http.StatusInternalServerError, p.audits)
	if !reflect.DeepEqual(p.trace, []string{"coverage", "buckets", "quota"}) || strings.Contains(w.Body.String(), "closed") {
		t.Fatalf("partial materialization escaped: trace=%v body=%s", p.trace, w.Body.String())
	}
}

func TestSpaceAggregateConstructorDiscoveryAndIsolation(t *testing.T) {
	raw, store := aggregateFixture(t)
	aggregateBind(t, raw, "beta")
	h := NewAdminHandler(service.NewFileService(store, raw, nil), raw, nil)
	if w := aggregateRequest(h, "usage"); w.Code != http.StatusOK {
		t.Fatalf("other tenant's binding denied usage: %d %s", w.Code, w.Body.String())
	}
	if w := aggregateRequest(h, "audit"); w.Code != http.StatusForbidden {
		t.Fatalf("global scope missed beta binding: %d %s", w.Code, w.Body.String())
	}
	wrapped := &aggregateRepository{Repository: raw}
	if w := aggregateRequest(NewAdminHandler(nil, wrapped, nil), "audit"); w.Code != http.StatusInternalServerError || len(wrapped.trace) != 0 {
		t.Fatalf("missing wrapper port fell back: %d trace=%v", w.Code, wrapped.trace)
	}
	if w := aggregateRequest(NewAdminHandler(nil, raw, nil, AdminResourceReaders{}), "audit"); w.Code != http.StatusInternalServerError {
		t.Fatalf("explicit nil fell back to discovery: %d", w.Code)
	}
	if err := raw.UpsertTenant(t.Context(), repository.TenantRecord{TenantID: "alpha", Status: "disabled"}); err != nil {
		t.Fatal(err)
	}
	for _, surface := range []string{"usage", "summary"} {
		if w := aggregateRequest(h, surface); w.Code != http.StatusForbidden {
			t.Fatalf("disabled tenant %s status=%d body=%s", surface, w.Code, w.Body.String())
		}
	}
}

func aggregateRetry(h *AdminHandler, id int64) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/retry", strings.NewReader(`{"object_id":999,"bucket":"default"}`))
	route := chi.NewRouteContext()
	route.URLParams.Add("id", fmt.Sprint(id))
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, route))
	w := httptest.NewRecorder()
	h.RetryJob(w, r)
	return w
}

func TestSpaceRetryExactJobAndMissingNoop(t *testing.T) {
	raw, store := aggregateFixture(t)
	obj, err := raw.InsertObjectVersion(t.Context(), repository.Object{TenantID: "alpha", Bucket: "docs", Key: "historical", VersionID: "v1", StorageKey: "alpha/docs/historical@vv1", Backend: "local"})
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := raw.EnqueueJob(t.Context(), repository.Job{TenantID: "alpha", Type: "index_object", Payload: fmt.Sprintf(`{"object_id":%d}`, obj.ID)})
	if err != nil {
		t.Fatal(err)
	}
	p := &aggregateRepository{Repository: raw}
	readers := discoverAdminResourceReaders(raw)
	h := NewAdminHandler(service.NewFileService(store, p, nil), p, nil, readers)
	// Replacing the option value cannot retarget already captured readers.
	readers.Jobs = aggregateJobReader(nil)
	for _, requested := range []int64{id, id + 10000} {
		if w := aggregateRetry(h, requested); w.Code != http.StatusOK {
			t.Fatalf("id=%d status=%d body=%s", requested, w.Code, w.Body.String())
		}
	}
	if p.retries != 1 || len(p.trace) != 0 {
		t.Fatalf("retries=%d trace=%v; missing must not mutate or scan lists", p.retries, p.trace)
	}
	bound := aggregateBind(t, raw, "alpha")
	bad := repository.Job{ID: id, TenantID: "alpha", Type: "delete_chunks", Payload: fmt.Sprintf(`{"schema_version":2,"object_id":999,"tenant_id":"alpha","bucket":%q,"key":"gone","event_id":999}`, bound.BackingBucket)}
	readers = discoverAdminResourceReaders(raw)
	readers.Jobs = aggregateJobReader(func(context.Context, int64) (repository.JobLookup, error) {
		return repository.JobLookup{RequestedID: id, Present: true, Job: bad}, nil
	})
	if w := aggregateRetry(NewAdminHandler(nil, p, nil, readers), id); w.Code != http.StatusForbidden || p.retries != 1 {
		t.Fatalf("bound durable retry status=%d retries=%d body=%s", w.Code, p.retries, w.Body.String())
	}
}

func TestSpaceRetryMalformedAndFaultReaders(t *testing.T) {
	raw, _ := aggregateFixture(t)
	base := repository.Job{ID: 17, TenantID: "alpha", Type: "delete_chunks", Payload: `{"schema_version":2,"object_id":999,"tenant_id":"alpha","bucket":"docs","key":"gone","event_id":99}`}
	for _, mode := range []string{"allow-pruned", "missing-port", "typed-nil", "binding-port", "resources-port", "echo", "both", "neither", "missing-row", "fault", "tenant", "schema", "legacy-unresolved", "duplicate", "unknown-kind"} {
		t.Run(mode, func(t *testing.T) {
			job := aggregateRetryJobVariant(base, mode)
			p := &aggregateRepository{Repository: raw}
			readers := discoverAdminResourceReaders(raw)
			readers.Jobs = aggregateJobReader(func(context.Context, int64) (repository.JobLookup, error) {
				return aggregateRetryLookup(job, mode)
			})
			if mode == "missing-port" {
				readers.Jobs = nil
			}
			if mode == "typed-nil" {
				readers.Jobs = aggregateJobReader(nil)
			}
			if mode == "binding-port" {
				readers.Binding = nil
			}
			if mode == "resources-port" {
				readers.Resources = (*aggregateRepository)(nil)
			}
			w := aggregateRetry(NewAdminHandler(nil, p, nil, readers), 17)
			want, retries := http.StatusInternalServerError, 0
			if mode == "allow-pruned" {
				want, retries = http.StatusOK, 1
			}
			if w.Code != want || p.retries != retries || strings.Contains(w.Body.String(), "secret") {
				t.Fatalf("status=%d retries=%d body=%s", w.Code, p.retries, w.Body.String())
			}
		})
	}
}

func aggregateRetryJobVariant(job repository.Job, mode string) repository.Job {
	switch mode {
	case "tenant":
		job.TenantID = "beta"
	case "schema":
		job.Payload = strings.Replace(job.Payload, `"schema_version":2`, `"schema_version":3`, 1)
	case "legacy-unresolved":
		job.Payload = `{"object_id":999}`
	case "duplicate":
		job.Payload = `{"object_id":999,"object_id":17}`
	case "unknown-kind":
		job.Type = "other"
	}
	return job
}

func aggregateRetryLookup(job repository.Job, mode string) (repository.JobLookup, error) {
	lookup := repository.JobLookup{RequestedID: 17, Present: true, Job: job}
	switch mode {
	case "echo":
		lookup.RequestedID++
	case "both":
		lookup.Missing = true
	case "neither":
		lookup.Present = false
	case "missing-row":
		lookup.Present, lookup.Missing = false, true
	case "fault":
		return lookup, errors.Join(repository.ErrNotFound, errors.New("secret"))
	}
	return lookup, nil
}

func TestSpaceRetryConflictingHistoricalFacts(t *testing.T) {
	payload := adminRetryPayload{SchemaVersion: json.RawMessage("2"), ObjectID: 17, TenantID: "alpha", Bucket: "docs", Key: "old", EventID: 23}
	job := repository.Job{TenantID: "alpha"}
	for _, obj := range []repository.Object{
		{ID: 18, TenantID: "alpha", Bucket: "docs", Key: "old"},
		{ID: 17, TenantID: "beta", Bucket: "docs", Key: "old"},
		{ID: 17, TenantID: "alpha", Bucket: "docs", Key: "latest"},
	} {
		if _, err := adminRetryObjectRef(job, payload, obj); err == nil {
			t.Fatalf("accepted competing object/version: %+v", obj)
		}
	}
	row := repository.Chunk{ID: 3, ObjectID: 17, TenantID: "alpha", Bucket: "docs", ObjectKey: "old"}
	if ref, err := adminRetryChunkRef(access.ChunkRef{TenantID: "alpha", ObjectID: 17}, []repository.Chunk{row}); err != nil || ref != payload.ref() {
		t.Fatalf("consistent historical chunks: ref=%+v err=%v", ref, err)
	}
	other := row
	other.ID, other.TenantID = 4, "beta"
	if _, err := adminRetryChunkRef(payload.ref(), []repository.Chunk{row, other}); err == nil {
		t.Fatal("accepted mixed-tenant chunks")
	}
	raw, _ := aggregateFixture(t)
	id := int64(17)
	eventID, err := raw.InsertEvent(t.Context(), repository.Event{TenantID: "alpha", Bucket: "docs", Key: "old", ObjectID: &id, Type: repository.EventCreated})
	if err != nil {
		t.Fatal(err)
	}
	payload.EventID = eventID
	h := NewAdminHandler(nil, raw, nil)
	if err := h.checkRetryEvent(t.Context(), payload); err == nil {
		t.Fatal("accepted a non-deleted exact event as durable delete proof")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := h.checkRetryEvent(ctx, payload); !errors.Is(err, context.Canceled) {
		t.Fatalf("event fault became pruning: %v", err)
	}
}

func TestSpaceBoundaryClassificationPrecedenceAndRedaction(t *testing.T) {
	cause := &json.SyntaxError{Offset: 23}
	fault := adminBoundaryFault(errors.Join(cause, repository.ErrNotFound, access.ErrDenied, service.ErrForbidden, errors.New("secret")))
	var syntax *json.SyntaxError
	if !errors.Is(fault, repository.ErrNotFound) || !errors.As(fault, &syntax) || syntax != cause {
		t.Fatal("boundary fault lost Is/As cause")
	}
	for _, tc := range []struct {
		err    error
		status int
	}{
		{fault, 500}, {errors.Join(fault, context.Canceled), 500},
		{errors.Join(fault, context.DeadlineExceeded), 504}, {errors.Join(fault, service.ErrTimeout), 504},
		{errors.Join(access.ErrSpaceBound, access.ErrDenied, repository.ErrNotFound, errors.New("secret")), 403},
	} {
		w := httptest.NewRecorder()
		(&Handler{}).writeError(w, httptest.NewRequest(http.MethodGet, "/", nil), tc.err)
		if w.Code != tc.status || strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), "object not found") {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
	}
}
