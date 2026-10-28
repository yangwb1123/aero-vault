package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/ai"
	"github.com/aero-vault/aero-vault/internal/auth"
	"github.com/aero-vault/aero-vault/internal/billing"
	"github.com/aero-vault/aero-vault/internal/config"
	"github.com/aero-vault/aero-vault/internal/replication"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/server"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/spaces"
)

func startupTestIntegrations(cfg *config.Config, endpoint string, metered, audited, acl bool) {
	cfg.Access = config.AccessConfig{Enabled: acl, DefaultPolicy: "tenant",
		ShareSecret: "0123456789abcdef0123456789abcdef", DeleteFailClosed: true}
	if metered {
		cfg.Billing = config.BillingConfig{Enabled: true, BaseURL: endpoint, TokenURL: endpoint + "/token",
			AllowInsecureHTTP: true, HTTPTimeoutSeconds: 1, ProjectionIntervalSec: 60,
			OutboxPollMillis: 100, OutboxBatchSize: 8, ClaimTTLSeconds: 2, OutboxMaxAttempts: 3, MaxLagSeconds: 60,
			Bindings: []config.BillingBinding{{TenantID: e2eTenant, ClientID: "billing-client", ClientSecret: "billing-secret"}}}
	}
	if audited {
		cfg.AuditGovernance = governanceE2EConfig()
		cfg.AuditGovernance.BaseURL, cfg.AuditGovernance.TokenURL = endpoint, endpoint+"/token"
	}
}

func startupTestPrincipal(t *testing.T) context.Context {
	t.Helper()
	return access.WithPrincipal(t.Context(), access.Principal{SubjectID: "alice", TenantID: e2eTenant,
		Kind: access.PrincipalUser, Scopes: []string{"read", "write"}})
}

func startupTestChunk(t *testing.T, p *spaceStartup, obj repository.Object) repository.Chunk {
	t.Helper()
	emb := ai.NewHashEmbedder(p.cfg.AI.Dim)
	vectors, err := emb.Embed(t.Context(), []string{"payload"})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.assembly.repo.InsertChunks(t.Context(), []repository.Chunk{{ObjectID: obj.ID,
		TenantID: obj.TenantID, Bucket: obj.Bucket, ObjectKey: obj.Key, Seq: 0, Content: "payload",
		Embedding: vectors[0], Dim: emb.Dimensions(), EmbedModel: emb.Name()}}); err != nil {
		t.Fatal(err)
	}
	rows, err := p.infra.raw.ListChunksForObject(t.Context(), obj.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("canonical chunks: %v %v", rows, err)
	}
	return rows[0]
}

func startupTestSpace(t *testing.T, raw repository.Repository) spaces.Space {
	t.Helper()
	if err := raw.UpsertTenant(t.Context(), repository.TenantRecord{TenantID: e2eTenant, Status: "active"}); err != nil {
		t.Fatal(err)
	}
	space, err := raw.(spaces.Store).CreateSpace(t.Context(), spaces.CreateCommand{
		TenantID: e2eTenant, ID: repository.NewVersionID(), Kind: spaces.Team, Name: "private",
		Actor: spaces.SubjectRef{Source: "test", Kind: spaces.User, ID: "alice"}, CommandID: repository.NewVersionID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return space
}

func startupTestSQLCount(t *testing.T, dsn, table string) int {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSpaceStartupWriteWrappersAndACLMatrix(t *testing.T) {
	for _, metered := range []bool{false, true} {
		for _, audited := range []bool{false, true} {
			for _, acl := range []bool{false, true} {
				t.Run(fmt.Sprintf("billing=%t/audit=%t/acl=%t", metered, audited, acl), func(t *testing.T) {
					cfg := startupTestConfig(t)
					var calls atomic.Int64
					startupTestIntegrations(cfg, startupTestEndpoint(t, &calls).URL, metered, audited, acl)
					p, _, _ := startupTestPlan(t, cfg)
					if metered {
						seedActivationProjection(t, p.infra.raw.(billing.Store))
					}
					if (p.assembly.manager != nil) != acl {
						t.Fatal("manager toggle changed")
					}
					testStartupMatrixDataPlane(t, p, metered, audited)
					testStartupMatrixBound(t, p)
					if calls.Load() != 0 {
						t.Fatal("unstarted runtime performed egress")
					}
				})
			}
		}
	}
}

func testStartupMatrixDataPlane(t *testing.T, p *spaceStartup, metered, audited bool) {
	t.Helper()
	ctx := startupTestPrincipal(t)
	obj, err := p.assembly.svc.Put(ctx, e2eTenant, "default", "doc.txt", strings.NewReader("payload"), 7, service.PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	chunk := startupTestChunk(t, p, obj)
	hits, err := p.search.Query(ctx, ai.Request{Tenant: e2eTenant, Bucket: "default", Query: "payload", Mode: "vector"})
	if err != nil || len(hits) != 1 || hits[0].ChunkID != chunk.ID {
		t.Fatalf("raw canonical readers lost through wrappers: %+v %v", hits, err)
	}
	startupTestWrappedWrites(t, p, obj, metered, audited)
	startupTestRoute(t, p, http.MethodGet, fmt.Sprintf("/v1/lineage/objects/%d", obj.ID), http.StatusOK)
	if p.assembly.manager != nil {
		testStartupACLDenial(t, p, obj)
	}
}

func startupTestWrappedWrites(t *testing.T, p *spaceStartup, obj repository.Object, metered, audited bool) {
	t.Helper()
	ctx := startupTestPrincipal(t)
	id, _, err := p.assembly.repo.EnqueueJob(ctx, repository.Job{TenantID: e2eTenant,
		Type: "virus_scan", Payload: fmt.Sprintf(`{"object_id":%d}`, obj.ID)})
	if err != nil {
		t.Fatal(err)
	}
	startupTestRoute(t, p, http.MethodPost, fmt.Sprintf("/v1/admin/jobs/%d/retry", id), http.StatusOK)
	lookup, err := p.infra.readers.router.Jobs.ReadJob(ctx, id)
	if err != nil || !lookup.Present || lookup.Job.ID != id {
		t.Fatalf("raw job read = %+v %v", lookup, err)
	}
	if metered && startupTestSQLCount(t, p.cfg.DB.DSN, "billing_usage_outbox") == 0 {
		t.Fatal("FileService usage bypassed billing")
	}
	if audited && startupTestSQLCount(t, p.cfg.DB.DSN, "audit_governance_outbox") != 1 {
		t.Fatal("bus persistence bypassed audit wrapper")
	}
}

func testStartupACLDenial(t *testing.T, p *spaceStartup, obj repository.Object) {
	t.Helper()
	if err := p.infra.raw.(access.Store).PutACLEntry(t.Context(), access.ACLEntry{
		ID: repository.NewVersionID(), TenantID: obj.TenantID, Bucket: obj.Bucket, Key: obj.Key,
		ResourceKind: access.ResourceObject, PrincipalType: access.PrincipalTypeUser, PrincipalID: "alice",
		Action: access.ActionRead, Effect: access.EffectDeny,
	}); err != nil {
		t.Fatal(err)
	}
	hits, err := p.search.Query(startupTestPrincipal(t), ai.Request{Tenant: e2eTenant, Bucket: "default", Query: "payload", Mode: "vector"})
	if err != nil || len(hits) != 0 {
		t.Fatalf("cached candidates bypassed genuine ACL denial: %+v %v", hits, err)
	}
}

func testStartupMatrixBound(t *testing.T, p *spaceStartup) {
	t.Helper()
	space := startupTestSpace(t, p.infra.raw)
	ctx := access.SystemContext(t.Context(), e2eTenant)
	if _, err := p.assembly.svc.Put(ctx, e2eTenant, space.BackingBucket, "private.txt", strings.NewReader("x"), 1, service.PutOptions{}); !access.IsResourceBoundaryDenied(err) {
		t.Fatalf("system/ACL toggle bypassed bound service bucket: %v", err)
	}
	if _, err := p.search.Query(ctx, ai.Request{Tenant: e2eTenant, Bucket: space.BackingBucket, Query: "payload"}); !access.IsResourceBoundaryDenied(err) {
		t.Fatalf("bound search was not refused: %v", err)
	}
	if _, err := p.search.Query(ctx, ai.Request{Tenant: e2eTenant, Query: "payload"}); !access.IsResourceBoundaryDenied(err) {
		t.Fatalf("aggregate search omitted coverage: %v", err)
	}
	testStartupRouterReaders(t, p)
}

func startupTestRouter(t *testing.T, p *spaceStartup) http.Handler {
	t.Helper()
	reg, err := auth.Parse("operator:*:admin")
	if err != nil {
		t.Fatal(err)
	}
	router := buildResourceRouter(p.assembly.svc, p.assembly.repo, p.infra.store, p.search, p.chat, p.agent,
		p.infra.bus, reg, p.assembly.manager, nil, nil, nil, p.cfg, 0, nil, nil, p.logger, nil, nil, p.infra.readers.router)
	return server.ApplyMiddleware(router, p.assembly.repo, reg, nil, p.cfg, p.logger, configuredConcurrency(p.cfg), nil)
}

func testStartupRouterReaders(t *testing.T, p *spaceStartup) {
	t.Helper()
	handler := startupTestRouter(t, p)
	for _, tc := range []struct {
		path string
		code int
	}{
		{"/v1/lineage/objects/1", http.StatusForbidden},
		{"/readyz", http.StatusOK},
	} {
		startupTestServeRoute(t, handler, http.MethodGet, tc.path, tc.code)
	}
}

func startupTestRoute(t *testing.T, p *spaceStartup, method, path string, code int) {
	t.Helper()
	startupTestServeRoute(t, startupTestRouter(t, p), method, path, code)
}

func startupTestServeRoute(t *testing.T, handler http.Handler, method, path string, code int) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer operator")
	req.Header.Set("X-Aero-Tenant", e2eTenant)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != code {
		t.Fatalf("root reader route %s = %d %s", path, w.Code, w.Body.String())
	}
}

func TestSpaceStartupReadinessDoesNotAuthorize(t *testing.T) {
	p, _, ctx := startupTestPlan(t, startupTestConfig(t))
	cause := errors.New("reader failed")
	var observations atomic.Int64
	p.infra.readers.router.Binding = startupBinding(func(context.Context, string, string) (spaces.BindingResult, error) {
		observations.Add(1)
		return spaces.BindingResult{}, errors.Join(cause, repository.ErrNotFound, access.ErrDenied)
	})
	p.infra.readers.wireService(p.assembly.svc)
	p.infra.readers.wireSearch(p.search, p.assembly.svc)
	if err := p.validate(); err != nil || observations.Load() != 0 {
		t.Fatalf("preflight performed authorization: %v observations=%d", err, observations.Load())
	}
	_, err := p.search.Query(ctx, ai.Request{Tenant: "default", Bucket: "default", Query: "payload"})
	if !access.IsResourceBoundaryFault(err) || !errors.Is(err, cause) || access.IsResourceBoundaryDenied(err) {
		t.Fatalf("query fault lost cause/precedence: %v", err)
	}
	w := httptest.NewRecorder()
	readyzHandler(p.assembly.repo, p.infra.store, nil)(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("query refusal became readiness failure: %d %s", w.Code, w.Body.String())
	}
}

func (r *startupObservedRepo) ListEventsAfter(context.Context, string, int64, int) ([]repository.Event, error) {
	return nil, errors.New("write wrapper is not event authority")
}

func startupTestEvent(t *testing.T, raw repository.Repository, bucket string) repository.Event {
	t.Helper()
	e := repository.Event{TenantID: e2eTenant, Bucket: bucket, Key: "event.txt", Type: repository.EventCreated,
		Payload: map[string]string{"size": "1"}, CreatedAt: time.Now().UTC()}
	id, err := raw.InsertEvent(t.Context(), e)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := raw.ListEventsAfter(t.Context(), e2eTenant, id-1, 1)
	if err != nil || len(rows) != 1 {
		t.Fatalf("persisted event: %v %v", rows, err)
	}
	return rows[0]
}

func TestSpaceStartupEventConsumersUseRawAuthority(t *testing.T) {
	cfg := startupTestConfig(t)
	var posts atomic.Int64
	endpoint := startupTestEndpoint(t, &posts).URL
	cfg.Events.WebhookURL = endpoint
	p, _, ctx := startupTestPlan(t, cfg)
	space := startupTestSpace(t, p.infra.raw)
	legacy := startupTestEvent(t, p.infra.raw, "default")
	bound := startupTestEvent(t, p.infra.raw, space.BackingBucket)
	for _, bucket := range []string{"default", space.BackingBucket} {
		if err := p.infra.raw.SetBucketNotifications(ctx, e2eTenant, bucket, []repository.NotificationRule{{
			ID: "created", Events: []string{"s3:ObjectCreated:*"}, EndpointURL: endpoint,
		}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, run := range []func(context.Context, <-chan repository.Event){p.workers.notifier.Run, p.workers.webhook.Run} {
		sub := make(chan repository.Event, 2)
		sub <- legacy
		sub <- bound
		close(sub)
		run(ctx, sub)
	}
	if posts.Load() != 2 {
		t.Fatalf("raw authority/unbound admission lost or bound event leaked: posts=%d", posts.Load())
	}
	testStartupBusDelivery(t, p, legacy, bound)
}

func testStartupBusDelivery(t *testing.T, p *spaceStartup, legacy, bound repository.Event) {
	t.Helper()
	var transported atomic.Int64
	p.infra.bus.WithTransport(func(context.Context, repository.Event) error { transported.Add(1); return nil })
	sub, unsubscribe := p.infra.bus.Subscribe()
	defer unsubscribe()
	before := startupTestSQLCount(t, p.cfg.DB.DSN, "object_events")
	p.infra.bus.DeliverContext(t.Context(), legacy)
	select {
	case got := <-sub:
		if got.ID != legacy.ID {
			t.Fatal("remote delivery did not retain exact event")
		}
	default:
		t.Fatal("wrapped event reader replaced raw authority")
	}
	p.infra.bus.DeliverContext(t.Context(), bound)
	select {
	case <-sub:
		t.Fatal("bound remote event leaked")
	default:
	}
	if transported.Load() != 0 || startupTestSQLCount(t, p.cfg.DB.DSN, "object_events") != before {
		t.Fatal("remote delivery looped or persisted twice")
	}
}

func TestSpaceStartupAVReplicationAndResyncRetainRawBinding(t *testing.T) {
	cfg := startupTestConfig(t)
	cfg.Antivirus.Enabled, cfg.Jobs.Workers = true, 1
	cfg.Replication = config.ReplicationCfg{Enabled: true, ResyncIntervalMinutes: 1, Storage: cfg.Storage}
	p, observed, ctx := startupTestPlan(t, cfg)
	if err := p.initialize(ctx); err != nil {
		t.Fatal(err)
	}
	space := startupTestSpace(t, p.infra.raw)
	bound, err := p.infra.raw.UpsertObject(ctx, repository.Object{TenantID: e2eTenant, Bucket: space.BackingBucket,
		Key: "bound.txt", VersionID: repository.NewVersionID(), Backend: "local", StorageKey: "exact-bound-key"})
	if err != nil {
		t.Fatal(err)
	}
	for _, execute := range []func(context.Context, int64) error{p.workers.av.ScanObjectByID, p.workers.replica.ReplicateObjectByID} {
		if err := execute(access.SystemContext(ctx, e2eTenant), bound.ID); !access.IsResourceBoundaryDenied(err) {
			t.Fatalf("worker did not refuse before storage/scanner work: %v", err)
		}
	}
	legacy, err := p.assembly.svc.Put(access.SystemContext(ctx, e2eTenant), e2eTenant, "default", "legacy.txt", strings.NewReader("x"), 1, service.PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	testStartupResync(t, p, observed, legacy.ID)
}

func testStartupResync(t *testing.T, p *spaceStartup, observed *startupObservedRepo, legacyID int64) {
	t.Helper()
	observed.enqueued = make(chan repository.Job, 2)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); p.workers.resync.Run(ctx) }()
	defer func() { cancel(); <-done }()
	select {
	case job := <-observed.enqueued:
		id, err := replication.DecodeObjectID(job.Payload)
		if err != nil || id != legacyID {
			t.Fatalf("resync enqueued unknown/bound identity: %+v %v", job, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("configured resync could not use raw reader through write wrapper")
	}
	cancel()
	<-done
	rows, err := p.infra.raw.ListJobs(t.Context(), "pending", replication.JobReplicate, 100)
	if err != nil || len(rows) != 1 {
		t.Fatalf("resync did not exclude bound object: %v %v", rows, err)
	}
}

// The two selected governance fixtures retain their original data and timing;
// only the same raw-reader wiring used by the production root is added.
func governanceSpaceReaders(t *testing.T, repo repository.Repository) spaceReaders {
	t.Helper()
	readers, err := captureSpaceReaders(repo)
	if err != nil {
		t.Fatal(err)
	}
	return readers
}
