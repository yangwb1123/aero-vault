package rest

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/spaces"
)

// LineageResourceReader reads exact identities, including historical versions.
// A latest-key lookup cannot substitute for a missing persisted reference.
type LineageResourceReader interface {
	GetObjectByID(context.Context, int64) (repository.Object, error)
	ListChunksForObject(context.Context, int64) ([]repository.Chunk, error)
}

// LineageReaders permits the root to retain raw, narrow authority before
// repository wrapping. Explicit nils replace discovery and remain faults.
// These readers govern lineage only; they do not install a Search boundary.
type LineageReaders struct {
	Binding   spaces.BindingReader
	Coverage  spaces.CoverageReader
	Resources LineageResourceReader
}

func discoverLineageReaders(repo repository.Repository) LineageReaders {
	binding, _ := repo.(spaces.BindingReader)
	coverage, _ := repo.(spaces.CoverageReader)
	return LineageReaders{Binding: binding, Coverage: coverage, Resources: repo}
}

func (h *AIHandler) WithLineageReaders(readers LineageReaders) *AIHandler {
	h.lineageBoundary = access.NewResourceBoundary(readers.Binding, readers.Coverage)
	h.lineageResources = readers.Resources
	h.lineageReaderFault = nil
	if adminPortMissing(readers.Binding) || adminPortMissing(readers.Coverage) || adminPortMissing(readers.Resources) {
		h.lineageReaderFault = lineageFault(errors.New("missing lineage capability"))
	}
	return h
}

func lineageFault(cause error) error {
	return fmt.Errorf("%w: %w", access.ErrResourceBoundaryFault, cause)
}

// readAdmittedLineage brackets the complete materialization, even empty history,
// with fresh tenant coverage. Missing historical objects/chunks may survive only
// under that complete absence proof, never instead of validating observed facts.
// This is not authorization or an atomic snapshot with bindings or Blob state.
func (h *AIHandler) readAdmittedLineage(ctx context.Context, tenant string, id int64, limit int) ([]repository.Usage, error) {
	if err := h.validateLineageReaders(id); err != nil {
		return nil, err
	}
	scope := spaces.CoverageScope{Kind: spaces.CoverageTenant, TenantID: tenant}
	if err := h.lineageBoundary.CheckCoverage(ctx, scope); err != nil {
		return nil, err
	}
	usages, err := h.repo.ListUsageForObject(ctx, tenant, id, limit)
	if err = errors.Join(err, ctx.Err()); err != nil {
		return nil, lineageFault(err)
	}
	facts, err := h.resolveLineage(ctx, tenant, id, usages)
	if err != nil {
		return nil, lineageFault(errors.Join(err, ctx.Err()))
	}
	for _, ref := range facts.objects {
		if ref.Bucket == "" {
			continue // genuinely missing; P0/P1, not a guessed bucket, covers history
		}
		if err := h.lineageBoundary.CheckBucket(ctx, tenant, ref.Bucket); err != nil {
			return nil, err // contradictory bound/fault never falls back to coverage
		}
	}
	if err := h.lineageBoundary.CheckCoverage(ctx, scope); err != nil {
		return nil, err
	}
	return usages, nil
}

func (h *AIHandler) validateLineageReaders(id int64) error {
	if h.lineageReaderFault != nil {
		return h.lineageReaderFault
	}
	if adminPortMissing(h.repo) || adminPortMissing(h.lineageResources) || id <= 0 {
		return lineageFault(errors.New("missing lineage reader or invalid identity"))
	}
	return nil
}

type lineageFacts struct {
	objects map[int64]access.ChunkRef
	chunks  map[int64]access.ChunkRef
}

func (h *AIHandler) resolveLineage(ctx context.Context, tenant string, requested int64, usages []repository.Usage) (lineageFacts, error) {
	facts := lineageFacts{objects: make(map[int64]access.ChunkRef), chunks: make(map[int64]access.ChunkRef)}
	ids, err := lineageObjectIDs(tenant, requested, usages)
	if err != nil {
		return facts, err
	}
	for _, id := range ids {
		ref, rows, err := h.readLineageObject(ctx, tenant, id)
		if err != nil {
			return facts, err
		}
		facts.objects[id] = ref
		for _, row := range rows {
			if _, exists := facts.chunks[row.ID]; exists {
				return facts, errors.New("duplicate lineage chunk identity")
			}
			facts.chunks[row.ID] = ref
		}
	}
	return facts, facts.validateUsageChunks(usages)
}

func lineageObjectIDs(tenant string, requested int64, usages []repository.Usage) ([]int64, error) {
	ids := []int64{requested}
	seenObjects := map[int64]bool{requested: true}
	seenUsages := make(map[int64]bool, len(usages))
	for _, usage := range usages {
		if usage.ID <= 0 || seenUsages[usage.ID] || usage.TenantID != tenant {
			return nil, errors.New("invalid lineage usage identity or tenant")
		}
		seenUsages[usage.ID] = true
		objects, err := lineageReferenceSet(usage.ObjectIDs)
		if err != nil {
			return nil, err
		}
		if !objects[requested] {
			return nil, errors.New("lineage usage omits requested object")
		}
		for _, id := range usage.ObjectIDs {
			if !seenObjects[id] {
				ids = append(ids, id)
				seenObjects[id] = true
			}
		}
	}
	return ids, nil
}

func lineageReferenceSet(ids []int64) (map[int64]bool, error) {
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			return nil, errors.New("invalid or duplicate lineage reference")
		}
		seen[id] = true
	}
	return seen, nil
}

func (facts lineageFacts) validateUsageChunks(usages []repository.Usage) error {
	for _, usage := range usages {
		if _, err := lineageReferenceSet(usage.ChunkIDs); err != nil {
			return err
		}
		objects, err := lineageReferenceSet(usage.ObjectIDs)
		if err != nil {
			return err
		}
		for _, id := range usage.ChunkIDs {
			if ref, present := facts.chunks[id]; present && !objects[ref.ObjectID] {
				return errors.New("lineage chunk contradicts usage objects")
			}
			// Absent historical chunk IDs remain historical, not canonicalized
			// to new chunks or objects. Complete tenant P0/P1 is mandatory.
		}
	}
	return nil
}

func (h *AIHandler) readLineageObject(ctx context.Context, tenant string, id int64) (access.ChunkRef, []repository.Chunk, error) {
	ref := access.ChunkRef{TenantID: tenant, ObjectID: id}
	obj, err := h.lineageResources.GetObjectByID(ctx, id)
	if ctx.Err() != nil {
		return ref, nil, errors.Join(err, ctx.Err())
	}
	if err != nil {
		if !lineageObjectMissing(err) || !reflect.ValueOf(obj).IsZero() {
			return ref, nil, errors.Join(err, errors.New("unresolved lineage object"))
		}
	} else {
		ref = access.ChunkRef{TenantID: obj.TenantID, Bucket: obj.Bucket, Key: obj.Key, ObjectID: obj.ID}
		if err := access.ValidateChunkRef(ref); err != nil {
			return ref, nil, err
		}
		if ref.TenantID != tenant || ref.ObjectID != id {
			return ref, nil, errors.New("lineage object scope or identity conflict")
		}
	}
	rows, err := h.lineageResources.ListChunksForObject(ctx, id)
	if err = errors.Join(err, ctx.Err()); err != nil {
		return ref, nil, err
	}
	ref, err = lineageChunkRef(ref, rows)
	return ref, rows, err
}

func lineageChunkRef(ref access.ChunkRef, rows []repository.Chunk) (access.ChunkRef, error) {
	seen := make(map[int64]bool, len(rows))
	for _, row := range rows {
		got := access.ChunkRef{TenantID: row.TenantID, Bucket: row.Bucket, Key: row.ObjectKey, ObjectID: row.ObjectID}
		if err := access.ValidateChunkRef(got); err != nil {
			return ref, err
		}
		if got.TenantID != ref.TenantID || got.ObjectID != ref.ObjectID || row.ID <= 0 || seen[row.ID] {
			return ref, errors.New("lineage chunk scope or identity conflict")
		}
		if ref.Bucket == "" {
			ref = got
		}
		if got != ref {
			return ref, errors.New("lineage chunks contradict object scope")
		}
		seen[row.ID] = true
	}
	return ref, nil
}

// Accept only a genuine NotFound chain, not a joined fault containing NotFound.
// Fault classification precedes sentinel causes; Is/As remain intact on return.
func lineageObjectMissing(err error) bool {
	if access.IsResourceBoundaryFault(err) || errors.Is(err, access.ErrDenied) || errors.Is(err, spaces.ErrPersistence) ||
		errors.Is(err, access.ErrSpaceBound) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if err == repository.ErrNotFound {
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return lineageObjectMissing(wrapped.Unwrap())
	}
	return false
}
