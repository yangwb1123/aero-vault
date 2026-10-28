package ai

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/spaces"
)

const searchBoundarySpaceID = "12345678-1234-1234-1234-123456789abc"

type searchBoundaryReader struct {
	binding       spaces.BindingResult
	coverage      spaces.CoverageResult
	bindingErr    error
	coverageErr   error
	bindingCalls  int
	coverageCalls int
	tenant        string
	bucket        string
	scope         spaces.CoverageScope
	afterBinding  func()
	afterCoverage func()
}

func newSearchBoundaryReader(tenant, bucket string) *searchBoundaryReader {
	binding := spaces.BindingResult{TenantID: tenant, Bucket: bucket, TenantKnown: true,
		TenantStatus: "active", State: spaces.BindingUnbound}
	coverage := spaces.CoverageResult{
		Scope:       spaces.CoverageScope{Kind: spaces.CoverageTenant, TenantID: tenant},
		TenantKnown: true, TenantStatus: "active", State: spaces.CoverageUnbound, Complete: true,
	}
	return &searchBoundaryReader{binding: binding, coverage: coverage}
}

func (r *searchBoundaryReader) ProbeSpaceBinding(_ context.Context, tenant, bucket string) (spaces.BindingResult, error) {
	r.bindingCalls++
	r.tenant, r.bucket = tenant, bucket
	result := r.binding
	if r.afterBinding != nil {
		r.afterBinding()
	}
	return result, r.bindingErr
}

func (r *searchBoundaryReader) ProbeSpaceCoverage(_ context.Context, scope spaces.CoverageScope) (spaces.CoverageResult, error) {
	r.coverageCalls++
	r.scope = scope
	result := r.coverage
	if r.afterCoverage != nil {
		r.afterCoverage()
	}
	return result, r.coverageErr
}

func (r *searchBoundaryReader) setBound() {
	r.binding.State = spaces.BindingBound
	r.binding.SpaceID = searchBoundarySpaceID
	r.binding.Revision = 1
	r.coverage.State = spaces.CoverageBound
	r.coverage.Witnesses = []spaces.BindingResult{r.binding}
}

func (r *searchBoundaryReader) setUnbound() {
	r.binding.State = spaces.BindingUnbound
	r.binding.SpaceID = ""
	r.binding.Revision = 0
	r.coverage.State = spaces.CoverageUnbound
	r.coverage.Witnesses = nil
}

func (r *searchBoundaryReader) setCoverageBound() {
	binding := r.binding
	binding.State = spaces.BindingBound
	binding.SpaceID = searchBoundarySpaceID
	binding.Revision = 1
	r.coverage.State = spaces.CoverageBound
	r.coverage.Witnesses = []spaces.BindingResult{binding}
}

type typedNilSearchReader struct{}

func (*typedNilSearchReader) ProbeSpaceBinding(context.Context, string, string) (spaces.BindingResult, error) {
	panic("typed nil binding reader must never be called")
}

func (*typedNilSearchReader) ProbeSpaceCoverage(context.Context, spaces.CoverageScope) (spaces.CoverageResult, error) {
	panic("typed nil coverage reader must never be called")
}

type countingSearchRepo struct {
	repository.Repository
	searches    int
	usages      int
	objectReads int
	chunkReads  int
}

func (r *countingSearchRepo) SearchChunks(ctx context.Context, tenant, bucket string, query []float32, limit int) ([]repository.SearchHit, error) {
	r.searches++
	return r.Repository.SearchChunks(ctx, tenant, bucket, query, limit)
}

func (r *countingSearchRepo) RecordUsage(ctx context.Context, usage repository.Usage) error {
	r.usages++
	return r.Repository.RecordUsage(ctx, usage)
}

func (r *countingSearchRepo) GetObject(ctx context.Context, tenant, bucket, key string) (repository.Object, error) {
	r.objectReads++
	return r.Repository.GetObject(ctx, tenant, bucket, key)
}

func (r *countingSearchRepo) GetObjectByID(ctx context.Context, id int64) (repository.Object, error) {
	r.objectReads++
	return r.Repository.GetObjectByID(ctx, id)
}

func (r *countingSearchRepo) ListChunksForObject(ctx context.Context, id int64) ([]repository.Chunk, error) {
	r.chunkReads++
	return r.Repository.ListChunksForObject(ctx, id)
}

type countingSearchReranker struct {
	calls int
}

func (r *countingSearchReranker) Name() string { return "counting" }
func (r *countingSearchReranker) Rerank(_ context.Context, _ string, hits []Hit, topK int) ([]Hit, error) {
	r.calls++
	if topK > 0 && len(hits) > topK {
		return hits[:topK], nil
	}
	return hits, nil
}

func seedBoundarySearchEnv(t *testing.T) (*testEnv, *HashEmbedder, repository.Object, []repository.Chunk) {
	t.Helper()
	env := newTestEnv(t)
	emb := NewHashEmbedder(128)
	obj := env.putObject(t, "boundary.txt", "text/plain", "boundary")
	env.seedChunks(t, obj, emb, "raft consensus protocol", "baking sourdough bread")
	rows, err := env.repo.ListChunksForObject(context.Background(), obj.ID)
	if err != nil {
		t.Fatalf("list canonical chunks: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("seeded %d chunks, want 2", len(rows))
	}
	return env, emb, obj, rows
}

func TestSearchBoundaryUnconfiguredKeepsLegacySearch(t *testing.T) {
	env, emb, _, _ := seedBoundarySearchEnv(t)
	s := NewSearch(env.repo, emb, nil)
	hits, err := s.Query(context.Background(), Request{
		Tenant: testTenant, Bucket: testBucket, Query: "raft", K: 2, Mode: "vector",
	})
	if err != nil {
		t.Fatalf("legacy query: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("legacy query returned no hits")
	}
}

func TestSearchBoundaryMissingAndTypedNilFault(t *testing.T) {
	env, emb, _, _ := seedBoundarySearchEnv(t)
	var typedNil *typedNilSearchReader
	cases := map[string]*Search{
		"nil readers":       NewSearch(env.repo, emb, nil).WithResourceBoundary(nil, nil),
		"typed nil binding": NewSearch(env.repo, emb, nil).WithResourceBoundary(typedNil, newSearchBoundaryReader(testTenant, testBucket)),
		"nil core":          NewSearch(env.repo, emb, nil).WithResourceBoundaryCore(nil),
	}
	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := s.Query(context.Background(), Request{
				Tenant: testTenant, Bucket: testBucket, Query: "raft", K: 2, Mode: "vector",
			})
			if !IsSearchResourceBoundaryFault(err) {
				t.Fatalf("want boundary fault, got %v", err)
			}
		})
	}
	s := NewSearch(env.repo, emb, nil).WithResourceBoundary(newSearchBoundaryReader(testTenant, testBucket), typedNil)
	if _, err := s.Query(context.Background(), Request{
		Tenant: testTenant, Query: "raft", K: 2, Mode: "vector",
	}); !IsSearchResourceBoundaryFault(err) {
		t.Fatalf("typed nil coverage: want boundary fault, got %v", err)
	}
}

func TestSearchBoundaryExactBucketUsesFreshP0P1(t *testing.T) {
	env, emb, _, _ := seedBoundarySearchEnv(t)
	reader := newSearchBoundaryReader(testTenant, testBucket)
	s := NewSearch(env.repo, emb, nil).WithResourceBoundary(reader, reader)
	hits, err := s.Query(context.Background(), Request{
		Tenant: testTenant, Bucket: testBucket, Query: "raft", K: 2, Mode: "vector",
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected hits")
	}
	if reader.bindingCalls != 2 || reader.coverageCalls != 0 {
		t.Fatalf("calls binding=%d coverage=%d, want binding=2 coverage=0",
			reader.bindingCalls, reader.coverageCalls)
	}
	if reader.tenant != testTenant || reader.bucket != testBucket {
		t.Fatalf("observed %q/%q, want %q/%q", reader.tenant, reader.bucket, testTenant, testBucket)
	}
}

func TestSearchBoundaryTenantCoverageUsesFreshP0P1(t *testing.T) {
	env, emb, _, _ := seedBoundarySearchEnv(t)
	reader := newSearchBoundaryReader(testTenant, testBucket)
	s := NewSearch(env.repo, emb, nil).WithResourceBoundary(reader, reader)
	if _, err := s.Query(context.Background(), Request{
		Tenant: testTenant, Query: "raft", K: 2, Mode: "vector",
	}); err != nil {
		t.Fatalf("query: %v", err)
	}
	if reader.bindingCalls != 0 || reader.coverageCalls != 2 {
		t.Fatalf("calls binding=%d coverage=%d, want binding=0 coverage=2",
			reader.bindingCalls, reader.coverageCalls)
	}
	if reader.scope.Kind != spaces.CoverageTenant || reader.scope.TenantID != testTenant {
		t.Fatalf("coverage scope = %+v", reader.scope)
	}
}

func TestSearchBoundaryBoundOrP1ChangeStopsDelivery(t *testing.T) {
	env, emb, obj, _ := seedBoundarySearchEnv(t)
	reader := newSearchBoundaryReader(testTenant, testBucket)
	reader.setBound()
	s := NewSearch(env.repo, emb, nil).WithResourceBoundary(reader, reader)
	if _, err := s.Query(context.Background(), Request{
		Tenant: testTenant, Bucket: testBucket, Query: "raft", K: 2, Mode: "vector",
	}); !IsSearchResourceBoundaryDenied(err) {
		t.Fatalf("P0 bound: want explicit denial, got %v", err)
	}

	reader = newSearchBoundaryReader(testTenant, testBucket)
	reader.afterBinding = reader.setBound
	s = NewSearch(env.repo, emb, nil).WithResourceBoundary(reader, reader)
	_, err := s.Query(context.Background(), Request{
		Tenant: testTenant, Bucket: testBucket, Query: "raft", K: 2, Mode: "vector",
	})
	if err == nil || !IsSearchResourceBoundaryDenied(err) {
		t.Fatalf("P1 bound: want denial, got %v", err)
	}
	usage, err := env.repo.ListUsageForObject(context.Background(), testTenant, obj.ID, 10)
	if err != nil {
		t.Fatalf("list usage: %v", err)
	}
	if len(usage) != 0 {
		t.Fatalf("bound P1 recorded %d usage rows", len(usage))
	}
}

func TestSearchBoundaryCoverageIncompleteOrFault(t *testing.T) {
	env, emb, _, _ := seedBoundarySearchEnv(t)
	for name, mutate := range map[string]func(*searchBoundaryReader){
		"incomplete": func(r *searchBoundaryReader) { r.coverage.Complete = false },
		"unknown":    func(r *searchBoundaryReader) { r.coverage.State = spaces.CoverageUnknown },
		"fault":      func(r *searchBoundaryReader) { r.coverageErr = errors.New("coverage provider down") },
		"bound":      func(r *searchBoundaryReader) { r.setCoverageBound() },
	} {
		t.Run(name, func(t *testing.T) {
			reader := newSearchBoundaryReader(testTenant, testBucket)
			mutate(reader)
			s := NewSearch(env.repo, emb, nil).WithResourceBoundary(reader, reader)
			_, err := s.Query(context.Background(), Request{
				Tenant: testTenant, Query: "raft", K: 2, Mode: "vector",
			})
			if err == nil {
				t.Fatal("want non-nil coverage outcome")
			}
			if name == "bound" {
				if !IsSearchResourceBoundaryDenied(err) {
					t.Fatalf("want explicit denial, got %v", err)
				}
				return
			}
			if !IsSearchResourceBoundaryFault(err) {
				t.Fatalf("want fault, got %v", err)
			}
		})
	}
}

func TestSearchRejectsCanonicalHitOutsideRequestedBucket(t *testing.T) {
	ctx := context.Background()
	env, emb, _, _ := seedBoundarySearchEnv(t)
	foreign, err := env.svc.Put(ctx, testTenant, "private", "foreign.txt", strings.NewReader("private"), 7, service.PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	env.seedChunks(t, foreign, emb, "private raft content")
	rows, err := env.repo.ListChunksForObject(ctx, foreign.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"vector", "bm25", "hybrid"} {
		t.Run(mode, func(t *testing.T) {
			repo := &countingSearchRepo{Repository: env.repo}
			reranker := &countingSearchReranker{}
			s := boundarySearch(t, repo, emb).
				WithVectorIndex(&fakeVectorIndex{hits: []repository.SearchHit{chunkHit(foreign, rows[0])}}).
				WithLexicalIndex(&fakeLexical{hits: []repository.SearchHit{chunkHit(foreign, rows[0])}}).
				WithReranker(reranker)
			if _, err := s.Query(ctx, Request{Tenant: testTenant, Bucket: testBucket, Query: "raft", K: 2, Mode: mode}); !errors.Is(err, ErrCanonicalHit) {
				t.Fatalf("out-of-request canonical result delivered: %v", err)
			}
			if reranker.calls != 0 || repo.usages != 0 {
				t.Fatalf("out-of-scope egress: rerank=%d usage=%d", reranker.calls, repo.usages)
			}
		})
	}
}

func TestSearchBoundaryEchoDisabledAndFaultStopAllEgress(t *testing.T) {
	env, seeded, _, _ := seedBoundarySearchEnv(t)
	cause := errors.New("provider fault with nested ordinary denial")
	for _, kind := range []string{"tenant echo", "bucket echo", "disabled", "provider"} {
		t.Run(kind, func(t *testing.T) {
			reader := newSearchBoundaryReader(testTenant, testBucket)
			configureSearchBoundaryFailure(reader, kind, cause)
			emb := &countingHashEmbedder{inner: seeded}
			repo := &countingSearchRepo{Repository: env.repo}
			reranker := &countingSearchReranker{}
			s := NewSearch(repo, emb, nil).WithResourceBoundary(reader, reader).WithReranker(reranker)
			_, err := s.Query(context.Background(), Request{Tenant: testTenant, Bucket: testBucket, Query: "raft", K: 2})
			if kind == "disabled" {
				if !IsSearchResourceBoundaryDenied(err) {
					t.Fatalf("disabled tenant=%v", err)
				}
			} else if !IsSearchResourceBoundaryFault(err) {
				t.Fatalf("uncertain observation was not a fault: %v", err)
			}
			if kind == "provider" && !errors.Is(err, cause) {
				t.Fatalf("provider cause lost: %v", err)
			}
			if emb.embedded+repo.searches+repo.usages+reranker.calls != 0 {
				t.Fatal("refused observation performed model/index/rerank/usage work")
			}
		})
	}
}

func configureSearchBoundaryFailure(reader *searchBoundaryReader, kind string, cause error) {
	switch kind {
	case "tenant echo":
		reader.binding.TenantID = "foreign"
	case "bucket echo":
		reader.binding.Bucket = "private"
	case "disabled":
		reader.binding.TenantStatus = "disabled"
	case "provider":
		reader.bindingErr = errors.Join(cause, service.ErrNotFound, access.ErrDenied)
	}
}

func TestSearchTenantCoverageP1PrecedesRerankerEgress(t *testing.T) {
	env, emb, obj, rows := seedBoundarySearchEnv(t)
	reader := newSearchBoundaryReader(testTenant, testBucket)
	reader.afterCoverage = func() {
		if reader.coverageCalls == 1 {
			reader.setCoverageBound()
		}
	}
	reranker := &countingSearchReranker{}
	repo := &countingSearchRepo{Repository: env.repo}
	s := NewSearch(repo, emb, nil).WithResourceBoundary(reader, reader).
		WithVectorIndex(&fakeVectorIndex{hits: []repository.SearchHit{chunkHit(obj, rows[0])}}).WithReranker(reranker)
	_, err := s.Query(context.Background(), Request{Tenant: testTenant, Query: "raft", K: 2})
	if !IsSearchResourceBoundaryDenied(err) || reader.coverageCalls != 2 {
		t.Fatalf("tenant P0/full D/P1 err=%v observations=%d", err, reader.coverageCalls)
	}
	if repo.objectReads == 0 || repo.chunkReads == 0 {
		t.Fatal("tenant D was not materialized before P1")
	}
	if reranker.calls != 0 || repo.usages != 0 {
		t.Fatalf("bound P1 leaked: rerank=%d usage=%d", reranker.calls, repo.usages)
	}
}

func TestSearchBoundaryUnconfiguredNilRepoPreservesRetrievalErrors(t *testing.T) {
	cause := errors.New("vector provider cause")
	for name, indexErr := range map[string]error{
		"boundary timeout": errors.Join(access.ErrResourceBoundaryFault, context.DeadlineExceeded, cause),
		"space bound":      errors.Join(access.ErrSpaceBound, access.ErrDenied, cause),
		"boundary fault":   errors.Join(access.ErrResourceBoundaryFault, repository.ErrNotFound, access.ErrDenied, ErrBudgetExceeded, cause),
		"budget":           errors.Join(ErrBudgetExceeded, cause),
		"provider":         cause,
	} {
		t.Run(name, func(t *testing.T) {
			emb := &countingHashEmbedder{inner: NewHashEmbedder(8)}
			s := NewSearch(nil, emb, nil).WithVectorIndex(&failingVIndex{err: indexErr})
			if err := s.ValidateResourceBoundary(); !IsSearchResourceBoundaryFault(err) || emb.embedded != 0 {
				t.Fatalf("legacy search passed production validation or performed work: err=%v embed=%d", err, emb.embedded)
			}
			hits, err := s.Query(context.Background(), Request{
				Tenant: testTenant, Bucket: testBucket, Query: "raft", Mode: "vector",
			})
			if hits != nil || err == nil {
				t.Fatalf("error-only retrieval returned hits=%+v err=%v", hits, err)
			}
			if errors.Unwrap(err) != indexErr || err.Error() != "search chunks: "+indexErr.Error() {
				t.Fatalf("retrieval error phase/cause changed: got %v, want search chunks: %v", err, indexErr)
			}
			for _, target := range []error{access.ErrResourceBoundaryFault, context.DeadlineExceeded, access.ErrSpaceBound, access.ErrDenied, repository.ErrNotFound, ErrBudgetExceeded, cause} {
				if errors.Is(err, target) != errors.Is(indexErr, target) {
					t.Fatalf("retrieval classification changed for %v: %v", target, err)
				}
			}
			if emb.embedded != 1 {
				t.Fatalf("retrieval embedded %d queries, want 1", emb.embedded)
			}
		})
	}
}

func TestSearchBoundaryConfiguredNilRepoStillFaultsBeforeWork(t *testing.T) {
	for name, configure := range map[string]func(*Search) *Search{
		"boundary":             func(s *Search) *Search { return s.WithResourceBoundary(nil, nil) },
		"core":                 func(s *Search) *Search { return s.WithResourceBoundaryCore(nil) },
		"canonical":            func(s *Search) *Search { return s.WithCanonicalResources(nil) },
		"typed nil canonical":  func(s *Search) *Search { return s.WithCanonicalResources((*canonicalFaultRepo)(nil)) },
		"authorizer":           func(s *Search) *Search { return s.WithHitAuthorizer(nil) },
		"typed nil authorizer": func(s *Search) *Search { return s.WithHitAuthorizer((*typedNilHitAuthorizer)(nil)) },
		"cache":                func(s *Search) *Search { return s.WithResultCache(1, time.Minute) },
		"reranker":             func(s *Search) *Search { return s.WithReranker(&countingSearchReranker{}) },
		"typed nil reranker":   func(s *Search) *Search { return s.WithReranker((*observingReranker)(nil)) },
		"typed nil vector":     func(s *Search) *Search { return s.WithVectorIndex((*fakeVectorIndex)(nil)) },
		"typed nil lexical":    func(s *Search) *Search { return s.WithLexicalIndex((*fakeLexical)(nil)) },
	} {
		t.Run(name, func(t *testing.T) {
			emb := &countingHashEmbedder{inner: NewHashEmbedder(8)}
			indexErr := errors.New("retrieval must not run")
			s := configure(NewSearch(nil, emb, nil).WithVectorIndex(&failingVIndex{err: indexErr}))
			if err := s.ValidateResourceBoundary(); !IsSearchResourceBoundaryFault(err) || emb.embedded != 0 {
				t.Fatalf("invalid production validation: err=%v embed=%d", err, emb.embedded)
			}
			hits, err := s.Query(context.Background(), Request{
				Tenant: testTenant, Bucket: testBucket, Query: "raft", Mode: "vector",
			})
			if !IsSearchResourceBoundaryFault(err) || hits != nil || errors.Is(err, indexErr) || emb.embedded != 0 {
				t.Fatalf("configured missing port did work or lost fault: hits=%+v err=%v embed=%d", hits, err, emb.embedded)
			}
		})
	}
}
