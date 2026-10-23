package events

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/spaces"
)

const eventBoundaryTestSpaceID = "12345678-1234-1234-1234-123456789abc"

type eventBoundaryTestReader struct {
	result         spaces.BindingResult
	err            error
	calls          int
	tenant, bucket string
	after          func()
}

func (r *eventBoundaryTestReader) ProbeSpaceBinding(
	_ context.Context, tenant, bucket string,
) (spaces.BindingResult, error) {
	r.calls++
	r.tenant, r.bucket = tenant, bucket
	if r.after != nil {
		r.after()
	}
	return r.result, r.err
}

type eventBoundaryTestFunc func(context.Context, string, string) (spaces.BindingResult, error)

func (f eventBoundaryTestFunc) ProbeSpaceBinding(
	ctx context.Context, tenant, bucket string,
) (spaces.BindingResult, error) {
	return f(ctx, tenant, bucket)
}

func eventBoundaryTestFacts() *eventBoundaryTestReader {
	return &eventBoundaryTestReader{result: spaces.BindingResult{
		TenantID: "Alpha", Bucket: "Bucket", TenantKnown: true,
		TenantStatus: "active", State: spaces.BindingUnbound,
	}}
}

func eventBoundaryTestSetBound(reader *eventBoundaryTestReader) {
	reader.result.State = spaces.BindingBound
	reader.result.SpaceID = eventBoundaryTestSpaceID
	reader.result.Revision = 1
}

func eventBoundaryTestExpectFault(t *testing.T, err error) {
	t.Helper()
	if !IsResourceBoundaryFault(err) || IsResourceBoundaryDenied(err) {
		t.Fatalf("want event boundary fault, not ordinary denial: %v", err)
	}
	if err.Error() != ErrResourceBoundaryFault.Error() {
		t.Fatalf("fault exposed raw text: %q", err.Error())
	}
}

func TestEventResourceBoundaryClassifiesExactScope(t *testing.T) {
	cases := []struct {
		name    string
		status  string
		known   bool
		bound   bool
		want    BoundaryOutcome
		wantErr error
	}{
		{"active_unbound", "active", true, false, BoundaryUnbound, nil},
		{"active_bound", "active", true, true, BoundaryBound, nil},
		{"disabled_bound", "disabled", true, true, BoundaryBound, nil},
		{"disabled_unbound", "disabled", true, false, BoundaryUnbound, spaces.ErrTenantUnavailable},
		{"empty_status_unbound", "", true, false, BoundaryUnbound, spaces.ErrTenantUnavailable},
		{"case_status_unbound", "Active", true, false, BoundaryUnbound, spaces.ErrTenantUnavailable},
		{"unknown_unbound", "", false, false, BoundaryUnbound, nil},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			reader := eventBoundaryTestFacts()
			reader.result.TenantKnown, reader.result.TenantStatus = test.known, test.status
			if test.bound {
				eventBoundaryTestSetBound(reader)
			}
			boundary := NewResourceBoundary(reader)
			outcome, err := boundary.Classify(t.Context(), "Alpha", "Bucket")
			if outcome != test.want {
				t.Fatalf("outcome = %q, want %q", outcome, test.want)
			}
			if test.wantErr == nil && err != nil {
				t.Fatalf("unexpected boundary error: %v", err)
			}
			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
			if test.wantErr != nil && (!IsResourceBoundaryDenied(err) || IsResourceBoundaryFault(err)) {
				t.Fatalf("known inactive tenant must be an explicit denial: %v", err)
			}
			if reader.calls != 1 || reader.tenant != "Alpha" || reader.bucket != "Bucket" {
				t.Fatal("boundary must probe the exact requested scope once")
			}
			checkErr := boundary.CheckBucket(t.Context(), "Alpha", "Bucket")
			switch test.want {
			case BoundaryBound:
				if !errors.Is(checkErr, ErrSpaceBound) || !IsResourceBoundaryDenied(checkErr) {
					t.Fatalf("bound CheckBucket = %v, want ErrSpaceBound", checkErr)
				}
			case BoundaryUnbound:
				if test.wantErr == nil && checkErr != nil {
					t.Fatalf("unbound CheckBucket = %v, want nil", checkErr)
				}
				if test.wantErr != nil && !errors.Is(checkErr, test.wantErr) {
					t.Fatalf("unbound CheckBucket = %v, want %v", checkErr, test.wantErr)
				}
			}
		})
	}
}

func TestEventResourceBoundaryMalformedObservationsFault(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*spaces.BindingResult)
	}{
		{"zero", func(v *spaces.BindingResult) { *v = spaces.BindingResult{} }},
		{"future_state", func(v *spaces.BindingResult) { v.State = "future" }},
		{"tenant_echo", func(v *spaces.BindingResult) { v.TenantID = "alpha" }},
		{"bucket_echo", func(v *spaces.BindingResult) { v.Bucket = "bucket" }},
		{"bucket_wildcard", func(v *spaces.BindingResult) { v.Bucket = "*" }},
		{"bound_without_tenant", func(v *spaces.BindingResult) { v.TenantKnown = false }},
		{"bound_without_id", func(v *spaces.BindingResult) { v.SpaceID = "" }},
		{"bound_without_revision", func(v *spaces.BindingResult) { v.Revision = 0 }},
		{"unbound_with_space", func(v *spaces.BindingResult) {
			v.State, v.SpaceID, v.Revision = spaces.BindingUnbound, eventBoundaryTestSpaceID, 1
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			reader := eventBoundaryTestFacts()
			eventBoundaryTestSetBound(reader)
			test.mutate(&reader.result)
			outcome, err := NewResourceBoundary(reader).Classify(t.Context(), "Alpha", "Bucket")
			if outcome != BoundaryUnknown {
				t.Fatalf("malformed outcome = %q, want unknown", outcome)
			}
			eventBoundaryTestExpectFault(t, err)
			if reader.calls != 1 {
				t.Fatal("malformed observation must not be retried")
			}
		})
	}
}

func TestEventResourceBoundaryPreservesReaderFaults(t *testing.T) {
	for _, cause := range []error{spaces.ErrNotFound, context.Canceled, errors.New("persistence detail")} {
		reader := eventBoundaryTestFacts()
		reader.err = cause
		boundary := NewResourceBoundary(reader)
		outcome, err := boundary.Classify(t.Context(), "Alpha", "Bucket")
		if outcome != BoundaryUnknown || !IsResourceBoundaryFault(err) || !errors.Is(err, cause) {
			t.Fatalf("reader fault %v lost or misclassified: outcome=%q err=%v", cause, outcome, err)
		}
		if err.Error() != ErrResourceBoundaryFault.Error() {
			t.Fatalf("fault exposed raw cause: %q", err.Error())
		}
		eventBoundaryTestExpectFault(t, boundary.CheckBucket(t.Context(), "Alpha", "Bucket"))
	}
	reader := eventBoundaryTestFacts()
	reader.err = errors.Join(access.ErrDenied, spaces.ErrNotFound)
	err := NewResourceBoundary(reader).CheckBucket(t.Context(), "Alpha", "Bucket")
	if !IsResourceBoundaryFault(err) || IsResourceBoundaryDenied(err) {
		t.Fatalf("fault classification must win over nested denial/absence: %v", err)
	}
}

func TestEventResourceBoundaryInvalidInputsAndContexts(t *testing.T) {
	reader := eventBoundaryTestFacts()
	boundary := NewResourceBoundary(reader)
	for _, bad := range []string{"", "*", " Alpha", "Alpha ", "\xff", "a\x00b", "a\nb"} {
		for _, pair := range [][2]string{{bad, "Bucket"}, {"Alpha", bad}} {
			outcome, err := boundary.Classify(t.Context(), pair[0], pair[1])
			if outcome != BoundaryUnknown || !IsResourceBoundaryFault(err) ||
				!errors.Is(err, spaces.ErrInvalidArgument) {
				t.Fatalf("invalid input %q outcome=%q err=%v", pair, outcome, err)
			}
		}
	}
	if reader.calls != 0 {
		t.Fatal("invalid input reached the reader")
	}

	type nilContext struct{ context.Context }
	var typedNil *nilContext
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, test := range []struct {
		name  string
		ctx   context.Context
		cause error
	}{
		{"nil", nil, nil},
		{"typed_nil", typedNil, nil},
		{"canceled", canceled, context.Canceled},
	} {
		outcome, err := boundary.Classify(test.ctx, "Alpha", "Bucket")
		if outcome != BoundaryUnknown || !IsResourceBoundaryFault(err) {
			t.Fatalf("%s context outcome=%q err=%v", test.name, outcome, err)
		}
		if test.cause != nil && !errors.Is(err, test.cause) {
			t.Fatalf("%s context cause was lost: %v", test.name, err)
		}
	}
	if reader.calls != 0 {
		t.Fatal("invalid context reached the reader")
	}
}

func TestEventResourceBoundaryCancellationAfterObservation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	reader := eventBoundaryTestFacts()
	reader.after = cancel
	outcome, err := NewResourceBoundary(reader).Classify(ctx, "Alpha", "Bucket")
	if outcome != BoundaryUnknown || !IsResourceBoundaryFault(err) || !errors.Is(err, context.Canceled) {
		t.Fatalf("post-read cancellation must remain a fault: outcome=%q err=%v", outcome, err)
	}
}

func TestEventResourceBoundaryMissingCapabilitiesFault(t *testing.T) {
	var typedNil *eventBoundaryTestReader
	var typedNilFunc eventBoundaryTestFunc
	var typedNilCore *access.ResourceBoundary
	for _, boundary := range []*ResourceBoundary{
		nil, {}, NewResourceBoundary(nil), NewResourceBoundary(typedNil),
		NewResourceBoundary(typedNilFunc), NewResourceBoundaryFromAccess(nil),
		NewResourceBoundaryFromAccess(typedNilCore),
	} {
		outcome, err := boundary.Classify(t.Context(), "Alpha", "Bucket")
		if outcome != BoundaryUnknown || !IsResourceBoundaryFault(err) {
			t.Fatalf("missing capability outcome=%q err=%v", outcome, err)
		}
		eventBoundaryTestExpectFault(t, boundary.CheckBucket(t.Context(), "Alpha", "Bucket"))
	}
}

func TestEventResourceBoundaryRepositoryDiscovery(t *testing.T) {
	repo := eventBoundaryTestRepo(t)
	boundary, err := NewResourceBoundaryFromRepository(repo)
	if err != nil {
		t.Fatalf("discover binding reader: %v", err)
	}
	outcome, err := boundary.Classify(t.Context(), "default", "default")
	if err != nil || outcome != BoundaryUnbound {
		t.Fatalf("legacy unbound bucket outcome=%q err=%v", outcome, err)
	}
	if err := boundary.CheckBucket(t.Context(), "default", "default"); err != nil {
		t.Fatalf("legacy unbound bucket must retain existing policy: %v", err)
	}

	wrapper := &eventBoundaryTestRepository{Repository: repo}
	if boundary, err := NewResourceBoundaryFromRepository(wrapper); boundary != nil ||
		!errors.Is(err, ErrResourceBoundaryUnavailable) || !IsResourceBoundaryFault(err) {
		t.Fatalf("wrapper without capability: boundary=%v err=%v", boundary, err)
	}
	if boundary, err := NewResourceBoundaryFromRepository(nil); boundary != nil ||
		!errors.Is(err, ErrResourceBoundaryUnavailable) {
		t.Fatalf("nil repository: boundary=%v err=%v", boundary, err)
	}
	var typedNil *eventBoundaryTestRepository
	if boundary, err := NewResourceBoundaryFromRepository(typedNil); boundary != nil ||
		!errors.Is(err, ErrResourceBoundaryUnavailable) {
		t.Fatalf("typed-nil repository: boundary=%v err=%v", boundary, err)
	}
}

func TestEventResourceBoundaryInjectionDoesNotEnableEgress(t *testing.T) {
	reader := eventBoundaryTestFacts()
	source := &eventBoundaryTestSource{binding: reader}
	boundary, err := NewResourceBoundaryFromRepository(source)
	if err != nil {
		t.Fatalf("discover source: %v", err)
	}
	if err := boundary.CheckBucket(t.Context(), "Alpha", "Bucket"); err != nil {
		t.Fatalf("unbound source: %v", err)
	}
	if reader.calls != 1 || source.inserts != 0 || source.notifications != 0 {
		t.Fatalf("boundary used egress or skipped observation: binding=%d inserts=%d notifications=%d",
			reader.calls, source.inserts, source.notifications)
	}

	core := access.NewResourceBoundary(reader, nil)
	if err := NewResourceBoundaryFromAccess(core).CheckBucket(t.Context(), "Alpha", "Bucket"); err != nil {
		t.Fatalf("injected shared boundary: %v", err)
	}
	if source.inserts != 0 || source.notifications != 0 {
		t.Fatal("boundary injection enabled event egress")
	}
}

type eventBoundaryTestRepository struct {
	repository.Repository
}

type eventBoundaryTestSource struct {
	repository.Repository
	binding       spaces.BindingReader
	inserts       int
	notifications int
}

func (s *eventBoundaryTestSource) ProbeSpaceBinding(
	ctx context.Context, tenant, bucket string,
) (spaces.BindingResult, error) {
	return s.binding.ProbeSpaceBinding(ctx, tenant, bucket)
}

func (s *eventBoundaryTestSource) InsertEvent(ctx context.Context, event repository.Event) (int64, error) {
	s.inserts++
	return 0, errors.New("unexpected event insert")
}

func (s *eventBoundaryTestSource) GetBucketNotifications(
	ctx context.Context, tenant, bucket string,
) ([]repository.NotificationRule, error) {
	s.notifications++
	return nil, errors.New("unexpected notification lookup")
}

func eventBoundaryTestRepo(t *testing.T) repository.Repository {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "event-boundary.db")
	repo, err := repository.Open(t.Context(), "sqlite", dsn)
	if err != nil {
		t.Fatalf("open event boundary repo: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(t.Context()); err != nil {
		t.Fatalf("migrate event boundary repo: %v", err)
	}
	return repo
}
