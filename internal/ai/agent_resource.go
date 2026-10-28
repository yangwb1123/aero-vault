package ai

import (
	"context"
	"errors"
	"fmt"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/spaces"
)

// ErrAgentResourceChanged marks a tool-derived resource whose current identity
// or authorization no longer matches the history already shown to the model.
var ErrAgentResourceChanged = errors.New("agent resource authorization changed")

type agentObjectRef struct {
	tenant   string
	bucket   string
	key      string
	objectID int64
	object   repository.Object
}

type agentSearchObservation struct {
	req  Request
	hits []Hit
}

// agentResourceGuard accumulates only refs derived from actual FileService and
// Search results. It never parses model strings or tool arguments as proof.
type agentResourceGuard struct {
	svc     *service.FileService
	search  *Search
	repo    repository.Repository
	tenant  string
	buckets map[[2]string]struct{}
	objects []agentObjectRef
	hits    []agentSearchObservation
	invalid error
}

func newAgentResourceGuard(svc *service.FileService, search *Search, repo repository.Repository, tenant string) *agentResourceGuard {
	return &agentResourceGuard{
		svc: svc, search: search, repo: repo, tenant: tenant,
		buckets: map[[2]string]struct{}{},
	}
}

func (g *agentResourceGuard) addBucket(tenant, bucket string) {
	g.buckets[[2]string{tenant, bucket}] = struct{}{}
}

func (g *agentResourceGuard) addList(tenant, bucket string, page repository.ListPage) {
	g.addBucket(tenant, bucket)
	for _, obj := range page.Objects {
		if obj.TenantID != tenant || obj.Bucket != bucket {
			g.rememberError(fmt.Errorf("%w: list row scope conflicts with request", ErrAgentResourceChanged))
		}
		g.addObject(obj)
	}
}

func (g *agentResourceGuard) addObject(obj repository.Object) {
	g.addObjectRef(agentObjectRef{
		tenant: obj.TenantID, bucket: obj.Bucket, key: obj.Key, objectID: obj.ID, object: obj,
	})
}

func (g *agentResourceGuard) addObjectRef(ref agentObjectRef) {
	// Keep even malformed refs so validation faults instead of forgetting them.
	g.objects = append(g.objects, ref)
}

func (g *agentResourceGuard) addHits(hits []Hit) {
	g.addSearch(Request{Tenant: g.tenant}, hits)
}

func (g *agentResourceGuard) addSearch(req Request, hits []Hit) {
	// Each observation is immutable, including an empty aggregate scope. A
	// later same-ID result cannot erase content already in assistant/history.
	g.hits = append(g.hits, agentSearchObservation{req: req, hits: cloneHits(hits)})
}

func (g *agentResourceGuard) rememberError(err error) {
	if g.invalid == nil {
		g.invalid = err
	}
}

func (g *agentResourceGuard) validate(ctx context.Context) (err error) {
	if g.invalid != nil {
		return g.invalid
	}
	defer func() { g.rememberError(err) }()
	if err := searchContextError(ctx); err != nil {
		return err
	}
	for key := range g.buckets {
		if err := g.validateBucket(ctx, key[0], key[1]); err != nil {
			return err
		}
	}
	for _, ref := range g.objects {
		if err := g.validateObject(ctx, ref); err != nil {
			return err
		}
	}
	if len(g.hits) > 0 {
		if err := g.validateHits(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (g *agentResourceGuard) validateBucket(ctx context.Context, tenant, bucket string) error {
	if err := g.validateBucketScope(tenant, bucket); err != nil {
		return err
	}
	if g.search != nil {
		if err := g.search.boundaryP0(ctx, tenant, bucket); err != nil {
			return err
		}
	}
	if g.svc == nil {
		return fmt.Errorf("agent: file service unavailable")
	}
	if _, err := g.svc.List(ctx, tenant, bucket, "", "", 1); err != nil {
		return err
	}
	if g.search != nil {
		if err := g.search.boundaryP1(ctx, tenant, bucket); err != nil {
			return err
		}
	}
	return nil
}

func (g *agentResourceGuard) validateObject(ctx context.Context, ref agentObjectRef) error {
	if err := g.validateObjectScope(ref); err != nil {
		return err
	}
	if g.search != nil {
		if err := g.search.boundaryP0(ctx, ref.tenant, ref.bucket); err != nil {
			return err
		}
	}
	if g.svc == nil {
		return fmt.Errorf("agent: file service unavailable")
	}
	obj, err := g.svc.Stat(ctx, ref.tenant, ref.bucket, ref.key)
	if err != nil {
		return err
	}
	if !sameObjectObservation(ref.object, obj) {
		return fmt.Errorf("%w: object replaced, moved or rewritten", ErrAgentResourceChanged)
	}
	if g.search != nil {
		if err := g.search.boundaryP1(ctx, ref.tenant, ref.bucket); err != nil {
			return err
		}
	}
	return nil
}

func (g *agentResourceGuard) validateHits(ctx context.Context) error {
	if g.search == nil {
		return fmt.Errorf("%w: search unavailable", ErrAgentResourceChanged)
	}
	for _, observation := range g.hits {
		if observation.req.Tenant != g.tenant {
			return fmt.Errorf("%w: search scope conflicts with request", ErrAgentResourceChanged)
		}
		out, denied, err := g.search.revalidateHits(ctx, observation.req, observation.hits)
		if err != nil {
			return err
		}
		if denied || len(out) != len(observation.hits) {
			return ErrAgentResourceChanged
		}
	}
	return nil
}

func ordinaryAgentToolError(err error) bool {
	if err == nil {
		return false
	}
	if accessFaultOrCanonical(err) {
		return false
	}
	return errors.Is(err, service.ErrNotFound) ||
		errors.Is(err, repository.ErrNotFound) ||
		errors.Is(err, service.ErrForbidden) ||
		errors.Is(err, service.ErrInvalidArgs)
}

func accessFaultOrCanonical(err error) bool {
	return securityResourceError(err) || errors.Is(err, ErrAgentResourceChanged) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (g *agentResourceGuard) admitToolBucket(ctx context.Context, tenant, bucket string) error {
	if err := searchContextError(ctx); err != nil {
		return err
	}
	if err := g.validateBucketScope(tenant, bucket); err != nil {
		return err
	}
	if g.search != nil {
		if err := g.search.boundaryP0(ctx, tenant, bucket); err != nil {
			return err
		}
	}
	if g.svc == nil {
		return fmt.Errorf("agent: file service unavailable")
	}
	return g.svc.CheckSpaceBucket(ctx, tenant, bucket)
}

func (g *agentResourceGuard) validateBucketScope(tenant, bucket string) error {
	if tenant != g.tenant {
		return fmt.Errorf("%w: tenant scope conflict", ErrAgentResourceChanged)
	}
	if err := spaces.ValidateBindingInput(tenant, bucket); err != nil {
		return fmt.Errorf("%w: bucket scope: %w", ErrAgentResourceChanged, err)
	}
	return nil
}

func (g *agentResourceGuard) validateObjectScope(ref agentObjectRef) error {
	if ref.tenant != g.tenant {
		return fmt.Errorf("%w: object tenant conflict", ErrAgentResourceChanged)
	}
	if err := access.ValidateChunkRef(access.ChunkRef{TenantID: ref.tenant, Bucket: ref.bucket, Key: ref.key, ObjectID: ref.objectID}); err != nil {
		return fmt.Errorf("%w: object scope: %w", ErrAgentResourceChanged, err)
	}
	return nil
}

func (g *agentResourceGuard) addReadObject(tenant, bucket, key string, obj repository.Object) error {
	if obj.TenantID != tenant || obj.Bucket != bucket || obj.Key != key {
		return fmt.Errorf("%w: read row conflicts with requested scope", ErrAgentResourceChanged)
	}
	g.addObject(obj)
	return nil
}
