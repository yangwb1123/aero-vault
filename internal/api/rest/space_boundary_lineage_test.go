package rest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/ai"
	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/spaces"
	"github.com/aero-vault/aero-vault/internal/storage"
	"github.com/go-chi/chi/v5"
)

type lineageBinding func(context.Context, string, string) (spaces.BindingResult, error)

func (f lineageBinding) ProbeSpaceBinding(ctx context.Context, tenant, bucket string) (spaces.BindingResult, error) {
	return f(ctx, tenant, bucket)
}

type lineageProbe struct {
	repository.Repository
	trace  []string
	after  func(string)
	usage  func([]repository.Usage) ([]repository.Usage, error)
	object func(int64, repository.Object, error) (repository.Object, error)
	chunks func(int64, []repository.Chunk) ([]repository.Chunk, error)
	byKey  int
}

func (p *lineageProbe) mark(stage string) {
	p.trace = append(p.trace, stage)
	if p.after != nil {
		p.after(stage)
	}
}

func (p *lineageProbe) ListUsageForObject(ctx context.Context, tenant string, id int64, limit int) ([]repository.Usage, error) {
	rows, err := p.Repository.ListUsageForObject(ctx, tenant, id, limit)
	p.mark("usage")
	if err == nil && p.usage != nil {
		return p.usage(rows)
	}
	return rows, err
}

func (p *lineageProbe) GetObjectByID(ctx context.Context, id int64) (repository.Object, error) {
	obj, err := p.Repository.GetObjectByID(ctx, id)
	p.mark(fmt.Sprintf("object:%d", id))
	if p.object != nil {
		return p.object(id, obj, err)
	}
	return obj, err
}

func (p *lineageProbe) ListChunksForObject(ctx context.Context, id int64) ([]repository.Chunk, error) {
	rows, err := p.Repository.ListChunksForObject(ctx, id)
	p.mark(fmt.Sprintf("chunks:%d", id))
	if err == nil && p.chunks != nil {
		return p.chunks(id, rows)
	}
	return rows, err
}

func (p *lineageProbe) GetObject(context.Context, string, string, string) (repository.Object, error) {
	p.byKey++
	return repository.Object{}, errors.New("latest-key lookup forbidden")
}

type lineageFixture struct {
	repo   repository.Repository
	dsn    string
	objs   []repository.Object
	chunks []repository.Chunk
}

func lineageMust(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func newLineageFixture(t *testing.T) lineageFixture {
	t.Helper()
	dir := t.TempDir()
	f := lineageFixture{dsn: "file:" + filepath.Join(dir, "lineage.db")}
	var err error
	f.repo, err = repository.Open(t.Context(), "sqlite", f.dsn)
	lineageMust(t, err)
	t.Cleanup(func() { _ = f.repo.Close() })
	lineageMust(t, f.repo.Migrate(t.Context()))
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	lineageMust(t, err)
	svc := service.NewFileService(store, f.repo, nil)
	for _, bucket := range []string{"one", "two", "three"} {
		obj, err := svc.Put(t.Context(), "alpha", bucket, "doc", strings.NewReader("data"), 4, service.PutOptions{})
		lineageMust(t, err)
		f.objs = append(f.objs, obj)
		lineageMust(t, f.repo.InsertChunks(t.Context(), []repository.Chunk{{ObjectID: obj.ID, TenantID: "alpha", Bucket: bucket, ObjectKey: obj.Key}}))
		rows, err := f.repo.ListChunksForObject(t.Context(), obj.ID)
		if err != nil || len(rows) != 1 {
			t.Fatalf("chunks=%v err=%v", rows, err)
		}
		f.chunks = append(f.chunks, rows[0])
	}
	for _, i := range []int{1, 2} {
		lineageMust(t, f.repo.RecordUsage(t.Context(), repository.Usage{TenantID: "alpha", Caller: "rest:chat", Query: "private history", ObjectIDs: []int64{f.objs[0].ID, f.objs[i].ID}, ChunkIDs: []int64{f.chunks[0].ID, f.chunks[i].ID}, Model: "model", TotalTokens: 17}))
	}
	return f
}

func lineageTestHandler(p *lineageProbe, raw repository.Repository) *AIHandler {
	readers := discoverLineageReaders(raw)
	binding, coverage := readers.Binding, readers.Coverage
	readers.Binding = lineageBinding(func(ctx context.Context, tenant, bucket string) (spaces.BindingResult, error) {
		p.mark("binding:" + bucket)
		return binding.ProbeSpaceBinding(ctx, tenant, bucket)
	})
	readers.Coverage = aggregateCoverage(func(ctx context.Context, scope spaces.CoverageScope) (spaces.CoverageResult, error) {
		result, err := coverage.ProbeSpaceCoverage(ctx, scope)
		p.mark("coverage")
		return result, err
	})
	readers.Resources = p
	return NewAIHandler(p, nil, nil, nil, nil, false).WithLineageReaders(readers)
}

func lineageRequest(h *AIHandler, id string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/?limit=7", nil)
	r.Header.Set(mw.TenantHeader, "alpha")
	route := chi.NewRouteContext()
	route.URLParams.Add("id", id)
	ctx := context.WithValue(r.Context(), chi.RouteCtxKey, route)
	ctx = access.WithPrincipal(ctx, access.Principal{Kind: access.PrincipalService, TenantID: "alpha", SubjectID: "operator", Scopes: []string{"admin", "*"}})
	w := httptest.NewRecorder()
	mw.Tenant(http.HandlerFunc(h.Lineage)).ServeHTTP(w, r.WithContext(ctx))
	return w
}

func lineageCheckFailure(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status || strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), "private history") || strings.Contains(w.Body.String(), `"entries"`) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestSpaceLineageResolvesAllPersistedReferences(t *testing.T) {
	f := newLineageFixture(t)
	p := &lineageProbe{Repository: f.repo}
	h := lineageTestHandler(p, f.repo)
	for range 2 {
		p.trace = nil
		w := lineageRequest(h, fmt.Sprint(f.objs[0].ID))
		var out lineageResponse
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out.Entries) != 2 {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		lineageCheckMaterialization(t, p, out, f)
	}
}

func lineageCheckMaterialization(t *testing.T, p *lineageProbe, out lineageResponse, f lineageFixture) {
	t.Helper()
	for _, obj := range f.objs {
		for _, kind := range []string{"object", "chunks"} {
			if !slices.Contains(p.trace, fmt.Sprintf("%s:%d", kind, obj.ID)) {
				t.Fatalf("unresolved usage reference: %v", p.trace)
			}
		}
	}
	if p.trace[0] != "coverage" || p.trace[len(p.trace)-1] != "coverage" || p.byKey != 0 {
		t.Fatalf("not fresh P0/D/P1: %v byKey=%d", p.trace, p.byKey)
	}
	if out.Entries[0].Model != "model" || out.Entries[0].TotalTokens != 17 || !slices.Equal(out.Entries[0].ObjectIDs, []int64{f.objs[0].ID, f.objs[2].ID}) {
		t.Fatalf("historical DTO changed: %+v", out)
	}
}

func TestSpaceLineageDeletedIDsNeverUseReplacementObjects(t *testing.T) {
	f := newLineageFixture(t)
	old := f.objs[2]
	lineageMust(t, f.repo.DeleteChunksForObject(t.Context(), old.ID))
	lineageMust(t, f.repo.HardDeleteObjectByID(t.Context(), old.ID))
	replacement, err := f.repo.InsertObjectVersion(t.Context(), repository.Object{TenantID: old.TenantID, Bucket: old.Bucket, Key: old.Key, Backend: "local", StorageKey: "replacement"})
	if err != nil || replacement.ID == old.ID {
		t.Fatalf("replacement=%+v err=%v", replacement, err)
	}
	p := &lineageProbe{Repository: f.repo}
	w := lineageRequest(lineageTestHandler(p, f.repo), fmt.Sprint(old.ID))
	if w.Code != 200 || p.byKey != 0 || slices.Contains(p.trace, fmt.Sprintf("object:%d", replacement.ID)) || !strings.Contains(w.Body.String(), fmt.Sprintf(`"object_id":%d`, old.ID)) {
		t.Fatalf("history retargeted: status=%d trace=%v body=%s", w.Code, p.trace, w.Body.String())
	}
}

func TestSpaceLineageContradictionsCannotUseCoverageFallback(t *testing.T) {
	f := newLineageFixture(t)
	for _, mode := range []string{"object-tenant", "object-id", "object-empty", "chunk-tenant", "chunk-key", "chunk-id", "chunk-object", "chunk-duplicate", "usage-chunk-object"} {
		t.Run(mode, func(t *testing.T) {
			p := &lineageProbe{Repository: f.repo}
			p.object = func(id int64, obj repository.Object, err error) (repository.Object, error) {
				if id == f.objs[2].ID {
					obj = lineageConflictingObject(mode, obj)
				}
				return obj, err
			}
			p.chunks = func(id int64, rows []repository.Chunk) ([]repository.Chunk, error) {
				if id == f.objs[2].ID {
					rows = lineageConflictingChunks(mode, rows, f.objs[1].ID)
				}
				return rows, nil
			}
			if mode == "usage-chunk-object" {
				p.usage = func(rows []repository.Usage) ([]repository.Usage, error) {
					rows[0].ChunkIDs = []int64{f.chunks[1].ID}
					return rows, nil
				}
			}
			lineageCheckFailure(t, lineageRequest(lineageTestHandler(p, f.repo), fmt.Sprint(f.objs[0].ID)), 500)
		})
	}
}

func lineageConflictingObject(mode string, obj repository.Object) repository.Object {
	switch mode {
	case "object-tenant":
		obj.TenantID = "beta"
	case "object-id":
		obj.ID++
	case "object-empty":
		obj.Bucket = ""
	}
	return obj
}

func lineageConflictingChunks(mode string, rows []repository.Chunk, otherID int64) []repository.Chunk {
	switch mode {
	case "chunk-tenant":
		rows[0].TenantID = "beta"
	case "chunk-key":
		rows[0].ObjectKey = "replacement"
	case "chunk-id":
		rows[0].ID = 0
	case "chunk-object":
		rows[0].ObjectID = otherID
	case "chunk-duplicate":
		rows = append(rows, rows[0])
	}
	return rows
}

func TestSpaceLineageMalformedHistory(t *testing.T) {
	f := newLineageFixture(t)
	for _, mode := range []string{"usage-id", "tenant", "missing-request", "negative-object", "duplicate-object", "zero-chunk", "duplicate-chunk"} {
		t.Run(mode, func(t *testing.T) {
			p := &lineageProbe{Repository: f.repo}
			p.usage = func(rows []repository.Usage) ([]repository.Usage, error) {
				rows[0] = lineageMalformedUsage(mode, rows[0], f.objs[2].ID)
				return rows, nil
			}
			lineageCheckFailure(t, lineageRequest(lineageTestHandler(p, f.repo), fmt.Sprint(f.objs[0].ID)), 500)
		})
	}
	db, err := sql.Open("sqlite", f.dsn)
	lineageMust(t, err)
	defer db.Close()
	_, err = db.ExecContext(t.Context(), `UPDATE ai_usage SET chunk_ids='[secret malformed'`)
	lineageMust(t, err)
	lineageCheckFailure(t, lineageRequest(NewAIHandler(f.repo, nil, nil, nil, nil, false), fmt.Sprint(f.objs[0].ID)), 500)
}

func lineageMalformedUsage(mode string, row repository.Usage, otherID int64) repository.Usage {
	switch mode {
	case "usage-id":
		row.ID = 0
	case "tenant":
		row.TenantID = "beta"
	case "missing-request":
		row.ObjectIDs = []int64{otherID}
	case "negative-object":
		row.ObjectIDs = append(row.ObjectIDs, -1)
	case "duplicate-object":
		row.ObjectIDs = append(row.ObjectIDs, row.ObjectIDs[0])
	case "zero-chunk":
		row.ChunkIDs = []int64{0}
	case "duplicate-chunk":
		row.ChunkIDs = append(row.ChunkIDs, row.ChunkIDs[0])
	}
	return row
}

func TestSpaceLineageReaderFaultsPreserveCause(t *testing.T) {
	f := newLineageFixture(t)
	cause := &json.SyntaxError{Offset: 23}
	for _, stage := range []string{"usage", "object", "chunks", "binding", "deadline"} {
		t.Run(stage, func(t *testing.T) {
			fault := errors.Join(repository.ErrNotFound, access.ErrDenied, cause, errors.New("secret"))
			if stage == "deadline" {
				fault = errors.Join(fault, context.DeadlineExceeded)
			}
			p := lineageFaultProbe(f.repo, stage, fault)
			h := lineageTestHandler(p, f.repo)
			if stage == "binding" {
				readers := discoverLineageReaders(f.repo)
				readers.Binding = lineageBinding(func(context.Context, string, string) (spaces.BindingResult, error) {
					return spaces.BindingResult{}, fault
				})
				h.WithLineageReaders(readers)
			}
			_, err := h.readAdmittedLineage(t.Context(), "alpha", f.objs[0].ID, 7)
			var syntax *json.SyntaxError
			if !access.IsResourceBoundaryFault(err) || !errors.Is(err, repository.ErrNotFound) || !errors.As(err, &syntax) || syntax != cause {
				t.Fatalf("lost fault priority/cause: %v", err)
			}
			status := 500
			if stage == "deadline" {
				status = 504
			}
			lineageCheckFailure(t, lineageRequest(h, fmt.Sprint(f.objs[0].ID)), status)
		})
	}
}

func lineageFaultProbe(raw repository.Repository, stage string, fault error) *lineageProbe {
	p := &lineageProbe{Repository: raw}
	if stage == "usage" {
		p.usage = func([]repository.Usage) ([]repository.Usage, error) { return nil, fault }
	}
	if stage == "object" || stage == "deadline" {
		p.object = func(int64, repository.Object, error) (repository.Object, error) { return repository.Object{}, fault }
	}
	if stage == "chunks" {
		p.chunks = func(int64, []repository.Chunk) ([]repository.Chunk, error) { return nil, fault }
	}
	return p
}

func TestSpaceLineageHistoricalEmptyAndCoverageRaces(t *testing.T) {
	f := newLineageFixture(t)
	for _, mode := range []string{"empty", "bound-p0", "bound-p1", "fault-p1", "unknown-p1", "nil-coverage", "typed-nil", "nil-resources", "nil-binding", "typed-binding"} {
		t.Run(mode, func(t *testing.T) {
			p := &lineageProbe{Repository: f.repo}
			h := lineageTestHandler(p, f.repo)
			readers := discoverLineageReaders(f.repo)
			readers.Resources = p
			calls := 0
			readers.Coverage = lineageTestCoverage(f.repo, mode, &calls)
			h.WithLineageReaders(lineageMissingReaders(readers, mode))
			id := int64(99999)
			if mode == "nil-binding" {
				id = f.objs[0].ID
			}
			want := map[string]int{"empty": 200, "bound-p0": 403, "bound-p1": 403}[mode]
			if want == 0 {
				want = 500
			}
			w := lineageRequest(h, fmt.Sprint(id))
			if w.Code != want || strings.Contains(w.Body.String(), "secret") {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if mode == "empty" && (calls != 2 || !strings.Contains(w.Body.String(), `"entries":[]`)) {
				t.Fatalf("uncovered empty history: calls=%d body=%s", calls, w.Body.String())
			}
		})
	}
}

func lineageMissingReaders(readers LineageReaders, mode string) LineageReaders {
	switch mode {
	case "nil-coverage":
		readers.Coverage = nil
	case "typed-nil":
		readers.Coverage = aggregateCoverage(nil)
	case "nil-resources":
		readers.Resources = (*lineageProbe)(nil)
	case "nil-binding":
		readers.Binding = nil
	case "typed-binding":
		readers.Binding = lineageBinding(nil)
	}
	return readers
}

func TestSpaceLineageRealCoverageChangesDuringMaterialization(t *testing.T) {
	f := newLineageFixture(t)
	p := &lineageProbe{Repository: f.repo}
	p.after = func(stage string) {
		if stage == "usage" {
			aggregateBind(t, f.repo, "alpha")
		}
	}
	lineageCheckFailure(t, lineageRequest(lineageTestHandler(p, f.repo), fmt.Sprint(f.objs[0].ID)), 403)
	if p.trace[0] != "coverage" || p.trace[len(p.trace)-1] != "coverage" {
		t.Fatalf("missing fresh P1: %v", p.trace)
	}
}

func TestSpaceLineageExactBoundReferenceDefeatsHistoricalFallback(t *testing.T) {
	f := newLineageFixture(t)
	old := f.objs[0]
	lineageMust(t, f.repo.DeleteChunksForObject(t.Context(), old.ID))
	lineageMust(t, f.repo.HardDeleteObjectByID(t.Context(), old.ID))
	bound := aggregateBind(t, f.repo, "alpha")
	obj, err := f.repo.InsertObjectVersion(t.Context(), repository.Object{TenantID: "alpha", Bucket: bound.BackingBucket, Key: "private", Backend: "local", StorageKey: "bound"})
	lineageMust(t, err)
	lineageMust(t, f.repo.RecordUsage(t.Context(), repository.Usage{TenantID: "alpha", ObjectIDs: []int64{old.ID, obj.ID}}))
	p := &lineageProbe{Repository: f.repo}
	readers := discoverLineageReaders(f.repo)
	readers.Resources = p
	// Deliberately stale/misbehaving coverage must not hide a current bound row.
	readers.Coverage = aggregateCoverage(func(_ context.Context, scope spaces.CoverageScope) (spaces.CoverageResult, error) {
		return spaces.CoverageResult{Scope: scope, State: spaces.CoverageUnbound, Complete: true}, nil
	})
	h := NewAIHandler(p, nil, nil, nil, nil, false).WithLineageReaders(readers)
	lineageCheckFailure(t, lineageRequest(h, fmt.Sprint(old.ID)), 403)
	if !slices.Contains(p.trace, fmt.Sprintf("object:%d", obj.ID)) || p.byKey != 0 {
		t.Fatalf("historical fallback skipped bound usage reference: %v", p.trace)
	}
}

func lineageTestCoverage(raw repository.Repository, mode string, calls *int) aggregateCoverage {
	return func(ctx context.Context, scope spaces.CoverageScope) (spaces.CoverageResult, error) {
		*calls++
		if mode == "bound-p0" || (mode == "bound-p1" && *calls == 2) {
			witness := spaces.BindingResult{TenantID: scope.TenantID, Bucket: "bound", TenantKnown: true, TenantStatus: "active", State: spaces.BindingBound, SpaceID: "94efb08b-813a-4e63-b766-207b46578e38", Revision: 1}
			return spaces.CoverageResult{Scope: scope, TenantKnown: true, TenantStatus: "active", State: spaces.CoverageBound, Complete: true, Witnesses: []spaces.BindingResult{witness}}, nil
		}
		if mode == "fault-p1" && *calls == 2 {
			return spaces.CoverageResult{}, errors.Join(repository.ErrNotFound, errors.New("secret"))
		}
		if mode == "unknown-p1" && *calls == 2 {
			return spaces.CoverageResult{Scope: scope, State: spaces.CoverageUnknown}, nil
		}
		return raw.(spaces.CoverageReader).ProbeSpaceCoverage(ctx, scope)
	}
}

type lineageFailLLM struct{ err error }

func (l lineageFailLLM) Name() string { return "fault" }
func (l lineageFailLLM) Chat(context.Context, ai.ChatRequest) (ai.ChatResponse, error) {
	return ai.ChatResponse{}, l.err
}
func (l lineageFailLLM) ChatStream(context.Context, ai.ChatRequest, func(string)) (ai.ChatResponse, error) {
	return ai.ChatResponse{}, l.err
}

func lineageCheckAIError(t *testing.T, w *httptest.ResponseRecorder, surface string, tc lineageAIErrorCase) {
	t.Helper()
	want, code := tc.status, tc.code
	if tc.err == ai.ErrBudgetExceeded && (surface == "search" || surface == "agent") {
		want, code = 500, "InternalError"
	}
	if surface == "stream" {
		want = 200
	}
	body := w.Body.String()
	if w.Code != want || !strings.Contains(body, `"code":"`+code+`"`) || strings.Contains(body, "secret") || strings.Contains(body, "object not found") || strings.Contains(body, "event: done") {
		t.Fatalf("status=%d body=%s", w.Code, body)
	}
}

type lineageAIErrorCase struct {
	err    error
	status int
	code   string
}

func TestSpaceBoundaryRedactionAcrossAIResponses(t *testing.T) {
	for _, tc := range []lineageAIErrorCase{
		{lineageFault(errors.Join(repository.ErrNotFound, access.ErrDenied, ai.ErrBudgetExceeded, errors.New("secret"))), 500, "InternalError"},
		{lineageFault(errors.Join(context.DeadlineExceeded, errors.New("secret"))), 504, "Timeout"},
		{errors.Join(access.ErrSpaceBound, access.ErrDenied, errors.New("secret")), 403, "AccessDenied"},
		{ai.ErrBudgetExceeded, 402, "BudgetExceeded"},
	} {
		for _, surface := range []string{"search", "chat", "stream", "agent"} {
			t.Run(fmt.Sprintf("%s/%s", tc.code, surface), func(t *testing.T) {
				search := ai.NewSearch(nil, ai.NewHashEmbedder(8), nil).WithVectorIndex(&failingVIndex{err: tc.err})
				llm := lineageFailLLM{err: tc.err}
				h := NewAIHandler(nil, search, ai.NewChat(search, llm, nil, nil), ai.NewAgent(nil, search, llm, nil, nil), nil, false)
				methods := map[string]http.HandlerFunc{"search": h.Search, "chat": h.Chat, "stream": h.ChatStream, "agent": h.Agent}
				w := httptest.NewRecorder()
				methods[surface](w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"query":"q"}`)))
				lineageCheckAIError(t, w, surface, tc)
			})
		}
	}
}
