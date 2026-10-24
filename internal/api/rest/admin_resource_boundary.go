package rest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/spaces"
)

func adminBoundaryFault(cause error) error {
	return fmt.Errorf("%w: %w", access.ErrResourceBoundaryFault, cause)
}

func adminPortMissing(port any) bool {
	if port == nil {
		return true
	}
	value := reflect.ValueOf(port)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// Each aggregate calls this for fresh P0, fully materializes its response, then
// calls it for fresh P1 before any success header or successful-read audit.
// No transaction, list limit, empty result, or previous request proves absence.
func (h *AdminHandler) checkAggregate(w http.ResponseWriter, r *http.Request, scope spaces.CoverageScope) bool {
	var err error
	if adminPortMissing(h.repo) {
		err = adminBoundaryFault(errors.New("missing aggregate repository"))
	} else {
		err = h.boundary.CheckCoverage(r.Context(), scope)
	}
	if err != nil {
		h.writeError(w, r, err)
		return false
	}
	return true
}

func (h *AdminHandler) checkTenantAggregate(w http.ResponseWriter, r *http.Request, tenant string) bool {
	return h.checkAggregate(w, r, spaces.CoverageScope{Kind: spaces.CoverageTenant, TenantID: tenant})
}

func (h *AdminHandler) checkGlobalAggregate(w http.ResponseWriter, r *http.Request) bool {
	return h.checkAggregate(w, r, spaces.CoverageScope{Kind: spaces.CoverageGlobal})
}

// admitRetryJob reads exactly the persisted requested job, not ListJobs' capped
// window. Missing is the historical successful no-op, but never calls RetryJob.
// Boundary admission is last, immediately before the caller's mutation. It
// does not claim an atomic job/binding transaction or authorize Space members.
func (h *AdminHandler) admitRetryJob(ctx context.Context, id int64) (bool, error) {
	job, missing, err := h.readRetryJob(ctx, id)
	if err != nil || missing {
		return false, err
	}
	if adminPortMissing(h.repo) || adminPortMissing(h.jobResources) {
		return false, adminBoundaryFault(errors.New("missing job repository or resource reader"))
	}
	ref, err := h.resolveRetryResource(ctx, job)
	if err != nil {
		return false, adminBoundaryFault(errors.Join(err, ctx.Err()))
	}
	if err := h.boundary.CheckBucket(ctx, ref.TenantID, ref.Bucket); err != nil {
		return false, err
	}
	return true, nil
}

func (h *AdminHandler) readRetryJob(ctx context.Context, id int64) (repository.Job, bool, error) {
	if err := ctx.Err(); err != nil {
		return repository.Job{}, false, adminBoundaryFault(err)
	}
	if adminPortMissing(h.jobReader) {
		return repository.Job{}, false, adminBoundaryFault(repository.ErrJobReaderUnavailable)
	}
	lookup, err := h.jobReader.ReadJob(ctx, id)
	if err = errors.Join(err, ctx.Err()); err != nil {
		return repository.Job{}, false, adminBoundaryFault(err)
	}
	if lookup.RequestedID != id || lookup.Present == lookup.Missing {
		return repository.Job{}, false, adminBoundaryFault(errors.New("invalid job lookup echo or state"))
	}
	if lookup.Missing {
		if lookup.Job != (repository.Job{}) {
			return repository.Job{}, false, adminBoundaryFault(errors.New("missing job with row"))
		}
		return repository.Job{}, true, nil
	}
	if lookup.Job.ID != id || id <= 0 {
		return repository.Job{}, false, adminBoundaryFault(errors.New("invalid job identity"))
	}
	return lookup.Job, false, nil
}

type adminRetryPayload struct {
	SchemaVersion json.RawMessage `json:"schema_version"`
	ObjectID      int64           `json:"object_id"`
	TenantID      string          `json:"tenant_id"`
	Bucket        string          `json:"bucket"`
	Key           string          `json:"key"`
	EventID       int64           `json:"event_id"`
}

// These are the four existing durable object-job encodings. Unknown work is
// unresolved, not an excuse to guess a default bucket or retry arbitrary scope.
func decodeAdminRetryPayload(job repository.Job) (adminRetryPayload, error) {
	switch job.Type {
	case "index_object", "delete_chunks", "virus_scan", "replicate":
	default:
		return adminRetryPayload{}, errors.New("unresolved job type")
	}
	if err := spaces.ValidateTenant(job.TenantID); err != nil {
		return adminRetryPayload{}, err
	}
	if err := validateAdminRetryJSON(job.Payload); err != nil {
		return adminRetryPayload{}, err
	}
	var payload adminRetryPayload
	decoder := json.NewDecoder(strings.NewReader(job.Payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return adminRetryPayload{}, err
	}
	if payload.ObjectID <= 0 {
		return adminRetryPayload{}, errors.New("invalid job object identity")
	}
	return payload, validateAdminRetrySchema(job, payload)
}

func validateAdminRetrySchema(job repository.Job, payload adminRetryPayload) error {
	if len(payload.SchemaVersion) == 0 {
		return validateAdminRetryLegacyScope(payload)
	}
	var version int
	if err := json.Unmarshal(payload.SchemaVersion, &version); err != nil {
		return err
	}
	if job.Type != "delete_chunks" || version != 2 || payload.EventID <= 0 || payload.TenantID != job.TenantID {
		return errors.New("invalid scoped job schema or tenant")
	}
	return access.ValidateChunkRef(payload.ref())
}

func validateAdminRetryLegacyScope(payload adminRetryPayload) error {
	if payload.TenantID != "" || payload.Bucket != "" || payload.Key != "" || payload.EventID != 0 {
		return errors.New("unexpected legacy job scope")
	}
	return nil
}

func (p adminRetryPayload) ref() access.ChunkRef {
	return access.ChunkRef{TenantID: p.TenantID, Bucket: p.Bucket, Key: p.Key, ObjectID: p.ObjectID}
}

// Reject duplicate fields, non-objects and trailing JSON rather than silently
// selecting a competing historical identity. Values are type-checked above.
func validateAdminRetryJSON(payload string) error {
	decoder := json.NewDecoder(strings.NewReader(payload))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token != json.Delim('{') {
		return errors.New("job payload must be an object")
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return errors.New("duplicate or invalid job payload field")
		}
		seen[key] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.Join(errors.New("trailing job payload"), err)
	}
	return nil
}

func (h *AdminHandler) resolveRetryResource(ctx context.Context, job repository.Job) (access.ChunkRef, error) {
	payload, err := decodeAdminRetryPayload(job)
	if err != nil {
		return access.ChunkRef{}, err
	}
	ref, err := h.readRetryObject(ctx, job, payload)
	if err != nil {
		return access.ChunkRef{}, err
	}
	if job.Type == "delete_chunks" {
		rows, err := h.jobResources.ListChunksForObject(ctx, ref.ObjectID)
		if err != nil {
			return access.ChunkRef{}, err
		}
		ref, err = adminRetryChunkRef(ref, rows)
		if err != nil {
			return access.ChunkRef{}, err
		}
	}
	if len(payload.SchemaVersion) != 0 {
		if err := h.checkRetryEvent(ctx, payload); err != nil {
			return access.ChunkRef{}, err
		}
	}
	return ref, access.ValidateChunkRef(ref)
}

func (h *AdminHandler) readRetryObject(ctx context.Context, job repository.Job, payload adminRetryPayload) (access.ChunkRef, error) {
	durable := len(payload.SchemaVersion) != 0
	ref := access.ChunkRef{TenantID: job.TenantID, ObjectID: payload.ObjectID}
	if durable {
		ref = payload.ref()
	}
	obj, err := h.jobResources.GetObjectByID(ctx, ref.ObjectID)
	if err != nil {
		if !adminRetryNotFound(err) || (!durable && job.Type != "delete_chunks") {
			return access.ChunkRef{}, err
		}
		return ref, nil
	}
	return adminRetryObjectRef(job, payload, obj)
}

func adminRetryObjectRef(job repository.Job, payload adminRetryPayload, obj repository.Object) (access.ChunkRef, error) {
	ref := access.ChunkRef{TenantID: obj.TenantID, Bucket: obj.Bucket, Key: obj.Key, ObjectID: obj.ID}
	if err := access.ValidateChunkRef(ref); err != nil {
		return access.ChunkRef{}, err
	}
	if ref.TenantID != job.TenantID || ref.ObjectID != payload.ObjectID {
		return access.ChunkRef{}, errors.New("job object identity conflict")
	}
	if len(payload.SchemaVersion) != 0 && ref != payload.ref() {
		return access.ChunkRef{}, errors.New("job object scope conflict")
	}
	return ref, nil
}

func adminRetryNotFound(err error) bool {
	return errors.Is(err, repository.ErrNotFound) && !access.IsResourceBoundaryFault(err) &&
		!errors.Is(err, access.ErrDenied) && !errors.Is(err, spaces.ErrPersistence) &&
		!errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

func adminRetryChunkRef(ref access.ChunkRef, rows []repository.Chunk) (access.ChunkRef, error) {
	seen := make(map[int64]bool, len(rows))
	for _, row := range rows {
		got := access.ChunkRef{TenantID: row.TenantID, Bucket: row.Bucket, Key: row.ObjectKey, ObjectID: row.ObjectID}
		if err := access.ValidateChunkRef(got); err != nil {
			return access.ChunkRef{}, err
		}
		if ref.Bucket == "" && got.TenantID == ref.TenantID && got.ObjectID == ref.ObjectID {
			ref = got
		}
		if got != ref || row.ID <= 0 || seen[row.ID] {
			return access.ChunkRef{}, errors.New("job chunk scope or identity conflict")
		}
		seen[row.ID] = true
	}
	return ref, access.ValidateChunkRef(ref)
}

func (h *AdminHandler) checkRetryEvent(ctx context.Context, payload adminRetryPayload) error {
	rows, err := h.jobResources.ListEventsAfter(ctx, payload.TenantID, payload.EventID-1, 1)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil // pruning does not erase the durable v2 identity
	}
	event := rows[0]
	if len(rows) != 1 || event.ID < payload.EventID || event.TenantID != payload.TenantID {
		return errors.New("invalid job event observation")
	}
	if event.ID > payload.EventID {
		return nil // the exact event has been pruned; never use the later row
	}
	if event.Type != repository.EventDeleted || event.ObjectID == nil {
		return errors.New("job event is not an identified deleted fact")
	}
	ref := access.ChunkRef{TenantID: event.TenantID, Bucket: event.Bucket, Key: event.Key, ObjectID: *event.ObjectID}
	if ref != payload.ref() {
		return errors.New("job event scope conflict")
	}
	return nil
}
