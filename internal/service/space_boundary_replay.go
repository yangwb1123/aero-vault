package service

import (
	"context"
	"errors"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/spaces"
)

// ReplayAdmission contains original, server-observed action and resource facts.
// It is persisted with the cached response, never reconstructed from retry data.
type ReplayAdmission struct {
	Action   access.Action      `json:"action"`
	Resource access.Resource    `json:"resource"`
	Object   *repository.Object `json:"object,omitempty"`
}

type replayObservationKey struct{}

// ObserveReplayAdmission creates request-local capture, not an authority cache
// or a global side table. It is used only while executing a fresh REST mutation.
func ObserveReplayAdmission(ctx context.Context) (context.Context, *ReplayAdmission) {
	observation := &ReplayAdmission{}
	return context.WithValue(ctx, replayObservationKey{}, observation), observation
}

func recordReplayAdmission(ctx context.Context, action access.Action, resource access.Resource, obj *repository.Object) {
	observation, _ := ctx.Value(replayObservationKey{}).(*ReplayAdmission)
	if observation != nil {
		*observation = ReplayAdmission{Action: action, Resource: resource, Object: obj}
	}
}

// RecordReplayObject retains the exact object returned by a fresh Put, without
// changing the original create/write action observed before execution.
func RecordReplayObject(ctx context.Context, obj repository.Object) {
	observation, _ := ctx.Value(replayObservationKey{}).(*ReplayAdmission)
	if observation == nil {
		return
	}
	observation.Resource = replayObjectResource(obj)
	observation.Object = &obj
}

func replayObjectResource(obj repository.Object) access.Resource {
	return access.Resource{TenantID: obj.TenantID, Bucket: obj.Bucket, Key: obj.Key,
		Kind: objectResourceKind(obj.Key), OwnerID: ObjectOwner(obj)}
}

func validateReplayObject(obj repository.Object) error {
	ref := access.ChunkRef{TenantID: obj.TenantID, Bucket: obj.Bucket, Key: obj.Key, ObjectID: obj.ID}
	if err := access.ValidateChunkRef(ref); err != nil {
		return spaceBoundaryFault(err)
	}
	if err := validateKey(obj.Key); err != nil {
		return spaceBoundaryFault(err)
	}
	if obj.VersionID == "" || obj.Backend == "" || obj.StorageKey == "" {
		return spaceBoundaryFault(errors.New("incomplete replay object identity"))
	}
	return nil
}

// AuthorizeReplayAdmission rechecks the current policy on original facts. It
// neither opens storage, changes quota, consumes an upload, nor signs a URL.
// Ordinary/admin delete and encryption requirements remain independent gates.
func (s *FileService) AuthorizeReplayAdmission(ctx context.Context, admission ReplayAdmission, opts ReadOptions) error {
	if err := ValidateReplayAdmission(admission); err != nil {
		return err
	}
	if err := s.authorizeReplayAction(ctx, admission); err != nil {
		return err
	}
	switch admission.Action {
	case access.ActionRead, access.ActionPreview, access.ActionDownload:
		if admission.Object == nil {
			return spaceBoundaryFault(errors.New("missing readable replay identity"))
		}
		if err := checkCorrupt(*admission.Object); err != nil {
			return err
		}
		return validateSSECRead(admission.Object.Metadata, opts)
	default:
		return nil
	}
}

// ValidateReplayAdmission checks complete persisted facts without reading a
// replacement object, normalizing scope, or granting access.
func ValidateReplayAdmission(admission ReplayAdmission) error {
	r := admission.Resource
	if err := spaces.ValidateBindingInput(r.TenantID, r.Bucket); err != nil {
		return spaceBoundaryFault(err)
	}
	if err := validateKey(r.Key); err != nil {
		return spaceBoundaryFault(err)
	}
	if !access.ValidAction(admission.Action) || admission.Action == access.ActionAll || r.Kind != objectResourceKind(r.Key) {
		return spaceBoundaryFault(errors.New("invalid replay action or kind"))
	}
	if admission.Object != nil {
		if err := validateReplayObject(*admission.Object); err != nil {
			return err
		}
		if replayObjectResource(*admission.Object) != r {
			return spaceBoundaryFault(errors.New("contradictory replay object scope"))
		}
	}
	return nil
}

func (s *FileService) authorizeReplayAction(ctx context.Context, admission ReplayAdmission) error {
	r := admission.Resource
	if admission.Action == access.ActionCreate && admission.Object == nil {
		// A PUT capability may have been issued for an absent key. Its exact
		// target is known, but current create/write policy must be observed;
		// this read is admission, never repair of a missing cached identity.
		_, err := s.preparePutAccess(ctx, r.TenantID, r.Bucket, r.Key, nil)
		return err
	}
	action := admission.Action
	if action == access.ActionCreate {
		// A completed upload now names a persisted object: current PUT policy
		// is write, not the original pre-creation shortcut.
		action = access.ActionWrite
	}
	return s.authorize(ctx, action, r)
}

func (s *FileService) AuthorizeReplayObject(ctx context.Context, action access.Action, obj repository.Object, opts ReadOptions) error {
	if err := s.AuthorizeReplayAdmission(ctx, ReplayAdmission{Action: action, Resource: replayObjectResource(obj), Object: &obj}, opts); err != nil {
		return err
	}
	return validateSSECRead(obj.Metadata, opts)
}
