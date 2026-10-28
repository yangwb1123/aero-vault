package reconcile

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/spaces"
)

var (
	errScopeRow        = errors.New("reconcile: invalid resource scope")
	errScopeObjectGone = errors.New("reconcile: exact object row no longer exists")
	errScopeRefusal    = errors.New("reconcile: explicit resource boundary refusal")
)

type scopeOutcome uint8

const (
	scopeAdmitted scopeOutcome = iota
	scopeRefused
	scopeFault
)

// The zero boundary is the optional legacy path, not production preflight.
// The repository remains the original (possibly write-wrapped) repository.
type scopeState struct {
	boundary *access.ResourceBoundary
	binding  spaces.BindingReader
	repo     repository.Repository
	refused  int
	faults   int
}

func newScopeState(reader spaces.BindingReader) scopeState {
	return scopeState{boundary: access.NewResourceBoundary(reader, nil), binding: reader}
}

func (s *scopeState) resetCounts() { s.refused, s.faults = 0, 0 }

func (s *scopeState) observe(outcome scopeOutcome) {
	switch outcome {
	case scopeRefused:
		s.refused++
	case scopeFault:
		s.faults++
	}
}

// Reflection is used only for interface typed-nil detection, never discovery,
// unwrapping repositories, private fields or reconstruction of resource facts.
func scopePortMissing(port any) bool {
	if port == nil {
		return true
	}
	v := reflect.ValueOf(port)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

func scopeRowFault(err error) error {
	return errors.Join(access.ErrResourceBoundaryFault, errScopeRow, err)
}

func scopeContext(ctx context.Context) error {
	if ctx == nil {
		return scopeRowFault(errors.New("nil maintenance context"))
	}
	if err := ctx.Err(); err != nil {
		return scopeRowFault(err)
	}
	return nil
}

func (s *scopeState) validate() error {
	if s == nil || s.boundary == nil || scopePortMissing(s.binding) || scopePortMissing(s.repo) {
		return scopeRowFault(errors.New("missing maintenance binding or row reader"))
	}
	return nil
}

// ready also runs on empty batches. Only an entirely unconfigured boundary is
// optional; explicitly missing/typed-nil dependencies cannot become legacy.
func (s *scopeState) ready(ctx context.Context) error {
	err := scopeContext(ctx)
	if err == nil && s.boundary != nil {
		err = s.validate()
	}
	if err != nil {
		s.faults++
	}
	return err
}

func (s *scopeState) check(ctx context.Context, tenant, bucket string) (scopeOutcome, error) {
	if s.boundary == nil {
		return scopeAdmitted, nil
	}
	return scopeBoundaryOutcome(s.boundary.CheckBucket(ctx, tenant, bucket))
}

func scopeBoundaryOutcome(err error) (scopeOutcome, error) {
	if err == nil {
		return scopeAdmitted, nil
	}
	if access.IsResourceBoundaryFault(err) {
		return scopeFault, err
	}
	if access.IsResourceBoundaryDenied(err) {
		return scopeRefused, errors.Join(errScopeRefusal, err)
	}
	return scopeFault, scopeRowFault(err)
}

func (s *scopeState) failed(err error) {
	switch {
	case access.IsResourceBoundaryFault(err):
		s.faults++
	case errors.Is(err, errScopeRefusal):
		s.refused++
	case err == errKeyProtected, err == repository.ErrLegalHoldActive:
		// A genuine protection refusal is neither a fault nor a deletion.
	default:
		s.faults++
	}
}

func (s *scopeState) admitObject(ctx context.Context, obj repository.Object, tenant, bucket string) (scopeOutcome, error) {
	_, outcome, err := s.object(ctx, obj, tenant, bucket)
	return outcome, err
}

// object validates the supplied identity, then rereads that exact persisted ID
// and compares all scope/storage/version fields. It returns authoritative mutable
// facts (locks, metadata, size), never a latest-key or ID-only replacement.
func (s *scopeState) object(ctx context.Context, obj repository.Object, tenant, bucket string) (repository.Object, scopeOutcome, error) {
	if err := validateObjectScope(obj, tenant, bucket); err != nil {
		s.faults++
		return repository.Object{}, scopeFault, scopeRowFault(err)
	}
	if err := s.ready(ctx); err != nil {
		return repository.Object{}, scopeFault, err
	}
	actual, err := canonicalObject(ctx, s.repo, obj, tenant, bucket)
	if err != nil {
		s.faults++
		return repository.Object{}, scopeFault, err
	}
	outcome, err := s.check(ctx, actual.TenantID, actual.Bucket)
	s.observe(outcome)
	return actual, outcome, err
}

func canonicalObject(ctx context.Context, repo repository.Repository, obj repository.Object, tenant, bucket string) (repository.Object, error) {
	if err := validateObjectScope(obj, tenant, bucket); err != nil {
		return repository.Object{}, scopeRowFault(err)
	}
	if scopePortMissing(repo) {
		return repository.Object{}, scopeRowFault(errors.New("missing object row reader"))
	}
	actual, err := repo.GetObjectByID(ctx, obj.ID)
	contextErr := scopeContext(ctx)
	if err == repository.ErrNotFound && contextErr == nil {
		err = errors.Join(errScopeObjectGone, err)
	}
	if err = errors.Join(err, contextErr); err != nil {
		return repository.Object{}, scopeRowFault(err)
	}
	if err := validateObjectScope(actual, tenant, bucket); err != nil {
		return repository.Object{}, scopeRowFault(err)
	}
	if !sameObjectIdentity(obj, actual) {
		return repository.Object{}, scopeRowFault(errors.New("object row identity changed"))
	}
	return actual, nil
}

// Only a single ordinary absence chain is idempotent. Joined faults, boundary
// faults and cancellation outrank a nested NotFound and retain their causes.
func ordinaryMissing(err, missing error) bool {
	if err == missing {
		return true
	}
	if err == nil || access.IsResourceBoundaryFault(err) {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		return len(joined.Unwrap()) == 1 && ordinaryMissing(joined.Unwrap()[0], missing)
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return ordinaryMissing(wrapped.Unwrap(), missing)
	}
	return false
}

func sameObjectIdentity(a, b repository.Object) bool {
	return a.ID == b.ID && a.TenantID == b.TenantID && a.Bucket == b.Bucket && a.Key == b.Key &&
		a.VersionID == b.VersionID && a.Backend == b.Backend && a.StorageKey == b.StorageKey &&
		a.VersionTombstone == b.VersionTombstone && sameScopeTime(a.DeletedAt, b.DeletedAt)
}

func sameScopeTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

func (s *scopeState) admitUpload(ctx context.Context, upload repository.Upload) (scopeOutcome, error) {
	_, outcome, err := s.upload(ctx, upload)
	return outcome, err
}

func (s *scopeState) upload(ctx context.Context, upload repository.Upload) (repository.Upload, scopeOutcome, error) {
	if err := validateUploadScope(upload); err != nil {
		s.faults++
		return repository.Upload{}, scopeFault, scopeRowFault(err)
	}
	if err := s.ready(ctx); err != nil {
		return repository.Upload{}, scopeFault, err
	}
	if scopePortMissing(s.repo) {
		s.faults++
		return repository.Upload{}, scopeFault, scopeRowFault(errors.New("missing upload row reader"))
	}
	actual, err := s.repo.GetUpload(ctx, upload.ID)
	err = errors.Join(err, scopeContext(ctx))
	if err == nil {
		err = validateUploadScope(actual)
	}
	if err == nil && !sameUploadIdentity(upload, actual) {
		err = errors.New("upload row identity changed")
	}
	if err != nil {
		s.faults++
		return repository.Upload{}, scopeFault, scopeRowFault(err)
	}
	outcome, err := s.check(ctx, actual.TenantID, actual.Bucket)
	s.observe(outcome)
	return actual, outcome, err
}

func sameUploadIdentity(a, b repository.Upload) bool {
	return a.ID == b.ID && a.TenantID == b.TenantID && a.Bucket == b.Bucket && a.Key == b.Key &&
		a.VersionID == b.VersionID && a.StorageKey == b.StorageKey && a.Backend == b.Backend && a.BackendUID == b.BackendUID
}

func validateObjectScope(obj repository.Object, tenant, bucket string) error {
	if err := validateObjectIdentity(obj); err != nil {
		return err
	}
	if err := spaces.ValidateBindingInput(obj.TenantID, obj.Bucket); err != nil {
		return errors.Join(errScopeRow, err)
	}
	if tenant != "" && obj.TenantID != tenant {
		return fmt.Errorf("%w: object tenant echo", errScopeRow)
	}
	if bucket != "" && obj.Bucket != bucket {
		return fmt.Errorf("%w: object bucket echo", errScopeRow)
	}
	if obj.StorageKey == "" && !isDeleteMarker(obj) {
		return fmt.Errorf("%w: object storage key", errScopeRow)
	}
	return nil
}

func validateObjectIdentity(obj repository.Object) error {
	if obj.ID <= 0 || obj.Key == "" || obj.VersionID == "" || obj.Size < 0 {
		return fmt.Errorf("%w: object identity", errScopeRow)
	}
	if obj.Backend == "" && !isDeleteMarker(obj) {
		return fmt.Errorf("%w: object backend", errScopeRow)
	}
	return nil
}

func validateObjectPage(page repository.ListPage, tenant, bucket, marker, previous string) (string, error) {
	for _, obj := range page.Objects {
		if err := validateObjectScope(obj, tenant, bucket); err != nil {
			return "", scopeRowFault(err)
		}
		if obj.Key <= previous || obj.DeletedAt != nil {
			return "", scopeRowFault(errors.New("object page has no key progress"))
		}
		previous = obj.Key
	}
	if !validObjectMarker(page, marker, previous) {
		return "", scopeRowFault(errors.New("object page has no marker progress"))
	}
	return previous, nil
}

func validObjectMarker(page repository.ListPage, marker, last string) bool {
	return !page.HasMore || len(page.Objects) > 0 && page.NextMarker != "" && page.NextMarker == last && page.NextMarker > marker
}

func validateUploadScope(upload repository.Upload) error {
	if upload.ID == "" || upload.Key == "" || upload.Backend == "" || upload.BackendUID == "" || upload.StorageKey == "" {
		return fmt.Errorf("%w: upload identity", errScopeRow)
	}
	if err := spaces.ValidateBindingInput(upload.TenantID, upload.Bucket); err != nil {
		return errors.Join(errScopeRow, err)
	}
	return nil
}

// scopeRepository guards the actual enumerations consumed by the frozen
// protection/deletion helpers, including their later re-lists. All writes pass
// through the original repository; no deletion algorithm or wrapper is bypassed.
type scopeRepository struct {
	repository.Repository
	scope  *scopeState
	anchor repository.Object
}

func (s *scopeState) deletionRepo(obj repository.Object) repository.Repository {
	return scopeRepository{Repository: s.repo, scope: s, anchor: obj}
}

func (r scopeRepository) ListObjectVersions(ctx context.Context, tenant, bucket, key string) ([]repository.Object, error) {
	if tenant != r.anchor.TenantID || bucket != r.anchor.Bucket || key != r.anchor.Key {
		return nil, scopeRowFault(errors.New("version enumeration scope changed"))
	}
	versions, err := r.Repository.ListObjectVersions(ctx, tenant, bucket, key)
	if err = errors.Join(err, scopeContext(ctx)); err != nil {
		return nil, scopeRowFault(err)
	}
	return r.validateVersions(ctx, versions, tenant, bucket, key)
}

func (r scopeRepository) validateVersions(ctx context.Context, versions []repository.Object, tenant, bucket, key string) ([]repository.Object, error) {
	if len(versions) == 0 {
		return nil, scopeRowFault(errors.New("empty version enumeration"))
	}
	seen, versionIDs := map[int64]bool{}, map[string]bool{}
	for i, version := range versions {
		if version.Key != key || seen[version.ID] || versionIDs[version.VersionID] {
			return nil, scopeRowFault(errors.New("conflicting version enumeration"))
		}
		actual, err := canonicalObject(ctx, r.Repository, version, tenant, bucket)
		if err != nil {
			return nil, err
		}
		if !versionAnchorMatches(r.anchor, actual) {
			return nil, scopeRowFault(errors.New("version anchor changed"))
		}
		if _, err := r.scope.check(ctx, tenant, bucket); err != nil {
			return nil, err
		}
		seen[actual.ID], versionIDs[actual.VersionID] = true, true
		versions[i] = actual
	}
	if !seen[r.anchor.ID] {
		return nil, scopeRowFault(errors.New("version enumeration omitted anchor"))
	}
	return versions, nil
}

func versionAnchorMatches(anchor, row repository.Object) bool {
	return anchor.ID != row.ID || sameObjectIdentity(anchor, row)
}
