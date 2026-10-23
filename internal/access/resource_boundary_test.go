package access

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/spaces"
)

const boundaryTestID = "12345678-1234-1234-1234-123456789abc"

type boundaryTestReader struct {
	binding        spaces.BindingResult
	coverage       spaces.CoverageResult
	err            error
	bindingCalls   int
	coverageCalls  int
	tenant, bucket string
	scope          spaces.CoverageScope
	ctx            context.Context
	afterRead      func()
}

func (r *boundaryTestReader) ProbeSpaceBinding(ctx context.Context, tenant, bucket string) (spaces.BindingResult, error) {
	r.bindingCalls++
	r.ctx, r.tenant, r.bucket = ctx, tenant, bucket
	if r.afterRead != nil {
		r.afterRead()
	}
	return r.binding, r.err
}

func (r *boundaryTestReader) ProbeSpaceCoverage(ctx context.Context, scope spaces.CoverageScope) (spaces.CoverageResult, error) {
	r.coverageCalls++
	r.ctx, r.scope = ctx, scope
	if r.afterRead != nil {
		r.afterRead()
	}
	return r.coverage, r.err
}

func boundaryTestFacts() *boundaryTestReader {
	return &boundaryTestReader{
		binding: spaces.BindingResult{TenantID: "Alpha", Bucket: "Bucket", TenantKnown: true,
			TenantStatus: "active", State: spaces.BindingUnbound},
		coverage: spaces.CoverageResult{Scope: spaces.CoverageScope{Kind: spaces.CoverageTenant, TenantID: "Alpha"},
			TenantKnown: true, TenantStatus: "active", State: spaces.CoverageUnbound, Complete: true},
	}
}

func boundaryTestSetBound(r *boundaryTestReader) {
	r.binding.State, r.binding.SpaceID, r.binding.Revision = spaces.BindingBound, boundaryTestID, 1
	r.coverage.State, r.coverage.Witnesses = spaces.CoverageBound, []spaces.BindingResult{r.binding}
}

func boundaryTestExpectFault(t *testing.T, err error) {
	t.Helper()
	if !IsResourceBoundaryFault(err) || IsResourceBoundaryDenied(err) {
		t.Fatalf("want boundary fault, not ordinary denial: %v", err)
	}
}

func boundaryTestExpectDenied(t *testing.T, err, cause error) {
	t.Helper()
	if IsResourceBoundaryFault(err) || !IsResourceBoundaryDenied(err) || !errors.Is(err, cause) {
		t.Fatalf("want ordinary denial with %v, got %v", cause, err)
	}
}

func TestResourceBoundaryBindingAdmission(t *testing.T) {
	for _, status := range []string{"active", "disabled", "", "Active", " active ", "paused"} {
		t.Run(status, func(t *testing.T) {
			r := boundaryTestFacts()
			r.binding.TenantStatus = status
			b := NewResourceBoundary(r, nil)
			err := b.CheckBucket(t.Context(), "Alpha", "Bucket")
			if status == "active" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				boundaryTestExpectDenied(t, err, spaces.ErrTenantUnavailable)
			}
			if r.bindingCalls != 1 || r.tenant != "Alpha" || r.bucket != "Bucket" || r.ctx != t.Context() {
				t.Fatal("binding request was not forwarded exactly once")
			}
		})
	}
	r := boundaryTestFacts()
	r.binding.TenantKnown, r.binding.TenantStatus = false, ""
	r.binding.Bucket = "spc-legacy/桶"
	b := NewResourceBoundary(r, nil)
	if err := b.CheckBucket(t.Context(), "Alpha", r.binding.Bucket); err != nil {
		t.Fatalf("authoritative unknown-tenant unbound fact must continue old policy: %v", err)
	}
	r.binding.TenantKnown, r.binding.TenantStatus = true, "active"
	boundaryTestSetBound(r)
	boundaryTestExpectDenied(t, b.CheckBucket(t.Context(), "Alpha", r.binding.Bucket), ErrSpaceBound)
}

func TestResourceBoundaryMalformedBindingsFault(t *testing.T) {
	for name, mutate := range map[string]func(*spaces.BindingResult){
		"tenant echo":         func(v *spaces.BindingResult) { v.TenantID = "alpha" },
		"bucket echo":         func(v *spaces.BindingResult) { v.Bucket = "bucket" },
		"zero":                func(v *spaces.BindingResult) { *v = spaces.BindingResult{} },
		"unknown state":       func(v *spaces.BindingResult) { v.State = "future" },
		"bad tenant":          func(v *spaces.BindingResult) { v.TenantID = "*" },
		"bad bucket":          func(v *spaces.BindingResult) { v.Bucket = " Bucket" },
		"unknown with status": func(v *spaces.BindingResult) { v.TenantKnown = false },
		"unbound with ID":     func(v *spaces.BindingResult) { v.SpaceID = boundaryTestID },
		"unbound revision":    func(v *spaces.BindingResult) { v.Revision = 1 },
		"bound no ID":         func(v *spaces.BindingResult) { v.State = spaces.BindingBound; v.Revision = 1 },
		"bound no revision":   func(v *spaces.BindingResult) { v.State = spaces.BindingBound; v.SpaceID = boundaryTestID },
		"bad UUID":            func(v *spaces.BindingResult) { v.State = spaces.BindingBound; v.SpaceID = "bad"; v.Revision = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			r := boundaryTestFacts()
			mutate(&r.binding)
			boundaryTestExpectFault(t, NewResourceBoundary(r, nil).CheckBucket(t.Context(), "Alpha", "Bucket"))
			if r.bindingCalls != 1 {
				t.Fatal("unexpected observation count")
			}
		})
	}
}

func TestResourceBoundaryCoverageAdmission(t *testing.T) {
	for _, global := range []bool{false, true} {
		t.Run(fmt.Sprint(global), func(t *testing.T) {
			r := boundaryTestFacts()
			if global {
				r.coverage.Scope = spaces.CoverageScope{Kind: spaces.CoverageGlobal}
				r.coverage.TenantKnown, r.coverage.TenantStatus = false, ""
			}
			b := NewResourceBoundary(nil, r)
			scope := r.coverage.Scope
			if err := b.CheckCoverage(t.Context(), scope); err != nil {
				t.Fatal(err)
			}
			if r.coverageCalls != 1 || r.scope != scope || r.ctx != t.Context() {
				t.Fatal("coverage request was not forwarded exactly once")
			}
			boundaryTestSetBound(r)
			boundaryTestExpectDenied(t, b.CheckCoverage(t.Context(), scope), ErrSpaceBound)
			r.coverage.Complete = false
			boundaryTestExpectFault(t, b.CheckCoverage(t.Context(), scope))
		})
	}
}

func TestResourceBoundaryCoverageTenantEligibility(t *testing.T) {
	for _, status := range []string{"active", "disabled", "", "Active", " active "} {
		r := boundaryTestFacts()
		r.coverage.TenantStatus = status
		err := NewResourceBoundary(nil, r).CheckCoverage(t.Context(), r.coverage.Scope)
		if status == "active" {
			if err != nil {
				t.Fatal(err)
			}
		} else {
			boundaryTestExpectDenied(t, err, spaces.ErrTenantUnavailable)
		}
	}
	r := boundaryTestFacts()
	r.coverage.TenantKnown, r.coverage.TenantStatus = false, ""
	if err := NewResourceBoundary(nil, r).CheckCoverage(t.Context(), r.coverage.Scope); err != nil {
		t.Fatalf("unknown tenant with complete absence must continue old policy: %v", err)
	}
}

func TestResourceBoundaryMalformedCoverageFaults(t *testing.T) {
	for name, mutate := range map[string]func(*spaces.CoverageResult){
		"zero":        func(v *spaces.CoverageResult) { *v = spaces.CoverageResult{} },
		"tenant echo": func(v *spaces.CoverageResult) { v.Scope.TenantID = "alpha" },
		"scope echo": func(v *spaces.CoverageResult) {
			v.Scope = spaces.CoverageScope{Kind: spaces.CoverageGlobal}
			v.TenantKnown = false
			v.TenantStatus = ""
		},
		"bad scope":            func(v *spaces.CoverageResult) { v.Scope.TenantID = "*" },
		"unknown state":        func(v *spaces.CoverageResult) { v.State = "future" },
		"incomplete":           func(v *spaces.CoverageResult) { v.Complete = false },
		"unknown":              func(v *spaces.CoverageResult) { v.State = spaces.CoverageUnknown; v.Complete = false },
		"unknown complete":     func(v *spaces.CoverageResult) { v.State = spaces.CoverageUnknown },
		"unknown status":       func(v *spaces.CoverageResult) { v.TenantKnown = false },
		"bound no witness":     func(v *spaces.CoverageResult) { v.State = spaces.CoverageBound },
		"unbound with witness": func(v *spaces.CoverageResult) { v.Witnesses = []spaces.BindingResult{boundaryTestFacts().binding} },
		"bad bound witness": func(v *spaces.CoverageResult) {
			v.State = spaces.CoverageBound
			v.Witnesses = []spaces.BindingResult{{}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := boundaryTestFacts()
			scope := r.coverage.Scope
			mutate(&r.coverage)
			boundaryTestExpectFault(t, NewResourceBoundary(nil, r).CheckCoverage(t.Context(), scope))
		})
	}
}

func TestResourceBoundaryMissingCapabilities(t *testing.T) {
	for _, b := range []*ResourceBoundary{nil, {}, NewResourceBoundary(nil, nil)} {
		boundaryTestExpectFault(t, b.CheckBucket(t.Context(), "Alpha", "Bucket"))
		boundaryTestExpectFault(t, b.CheckCoverage(t.Context(), boundaryTestFacts().coverage.Scope))
	}
	r := boundaryTestFacts()
	boundaryTestExpectFault(t, NewResourceBoundary(nil, r).CheckBucket(t.Context(), "Alpha", "Bucket"))
	boundaryTestExpectFault(t, NewResourceBoundary(r, nil).CheckCoverage(t.Context(), r.coverage.Scope))
	if r.bindingCalls != 0 || r.coverageCalls != 0 {
		t.Fatal("substituted an unrelated port")
	}
}

type boundaryValueReader struct{ *boundaryTestReader }

func TestResourceBoundaryNonPointerReaders(t *testing.T) {
	r := boundaryTestFacts()
	for _, b := range []*ResourceBoundary{
		NewResourceBoundary(boundaryValueReader{r}, boundaryValueReader{r}),
		NewResourceBoundary(boundaryBindingFunc(r.ProbeSpaceBinding), boundaryCoverageFunc(r.ProbeSpaceCoverage)),
	} {
		if err := b.CheckBucket(t.Context(), "Alpha", "Bucket"); err != nil {
			t.Fatal(err)
		}
		if err := b.CheckCoverage(t.Context(), r.coverage.Scope); err != nil {
			t.Fatal(err)
		}
	}
	if r.bindingCalls != 2 || r.coverageCalls != 2 {
		t.Fatal("non-pointer readers were not invoked exactly once per check")
	}
}

type boundaryBindingFunc func(context.Context, string, string) (spaces.BindingResult, error)

func (f boundaryBindingFunc) ProbeSpaceBinding(ctx context.Context, tenant, bucket string) (spaces.BindingResult, error) {
	return f(ctx, tenant, bucket)
}

type boundaryCoverageFunc func(context.Context, spaces.CoverageScope) (spaces.CoverageResult, error)

func (f boundaryCoverageFunc) ProbeSpaceCoverage(ctx context.Context, scope spaces.CoverageScope) (spaces.CoverageResult, error) {
	return f(ctx, scope)
}

type boundaryNilMap map[string]int
type boundaryNilSlice []string
type boundaryNilChan chan int

func (boundaryNilMap) ProbeSpaceBinding(context.Context, string, string) (spaces.BindingResult, error) {
	panic("nil map reader invoked")
}
func (boundaryNilSlice) ProbeSpaceBinding(context.Context, string, string) (spaces.BindingResult, error) {
	panic("nil slice reader invoked")
}
func (boundaryNilChan) ProbeSpaceBinding(context.Context, string, string) (spaces.BindingResult, error) {
	panic("nil channel reader invoked")
}

func TestResourceBoundaryTypedNilCapabilities(t *testing.T) {
	for _, reader := range []spaces.BindingReader{(*boundaryTestReader)(nil), boundaryBindingFunc(nil),
		boundaryNilMap(nil), boundaryNilSlice(nil), boundaryNilChan(nil)} {
		boundaryTestExpectFault(t, NewResourceBoundary(reader, nil).CheckBucket(t.Context(), "Alpha", "Bucket"))
	}
	for _, reader := range []spaces.CoverageReader{(*boundaryTestReader)(nil), boundaryCoverageFunc(nil)} {
		boundaryTestExpectFault(t, NewResourceBoundary(nil, reader).CheckCoverage(t.Context(), boundaryTestFacts().coverage.Scope))
	}
}

func TestResourceBoundaryInvalidInputDoesNotRead(t *testing.T) {
	r := boundaryTestFacts()
	b := NewResourceBoundary(r, r)
	for _, bad := range []string{"", "*", " Alpha", "Alpha ", "\xff", "a\x00b", "a\nb"} {
		boundaryTestExpectFault(t, b.CheckBucket(t.Context(), bad, "Bucket"))
		boundaryTestExpectFault(t, b.CheckBucket(t.Context(), "Alpha", bad))
		boundaryTestExpectFault(t, b.CheckCoverage(t.Context(), spaces.CoverageScope{Kind: spaces.CoverageTenant, TenantID: bad}))
	}
	for _, scope := range []spaces.CoverageScope{{}, {Kind: "other"}, {Kind: spaces.CoverageGlobal, TenantID: "Alpha"}} {
		boundaryTestExpectFault(t, b.CheckCoverage(t.Context(), scope))
	}
	if r.bindingCalls != 0 || r.coverageCalls != 0 {
		t.Fatal("invalid scope reached provider")
	}
}

func TestResourceBoundaryFaultCausePriority(t *testing.T) {
	for _, cause := range []error{ErrDenied, ErrNotFound, spaces.ErrNotFound, spaces.ErrPersistence,
		sql.ErrNoRows, context.Canceled, context.DeadlineExceeded, errors.Join(ErrDenied, spaces.ErrNotFound)} {
		r := boundaryTestFacts()
		boundaryTestSetBound(r)
		r.err = fmt.Errorf("provider: %w", cause)
		b := NewResourceBoundary(r, r)
		for _, err := range []error{b.CheckBucket(t.Context(), "Alpha", "Bucket"), b.CheckCoverage(t.Context(), r.coverage.Scope)} {
			boundaryTestExpectFault(t, err)
			if !errors.Is(err, cause) {
				t.Fatalf("lost cause %v: %v", cause, err)
			}
			outer := fmt.Errorf("outer: %w", errors.Join(ErrDenied, ErrNotFound, err))
			boundaryTestExpectFault(t, outer)
		}
	}
	if IsResourceBoundaryFault(nil) || IsResourceBoundaryDenied(nil) || IsResourceBoundaryFault(ErrDenied) {
		t.Fatal("classifier confused ordinary errors with a boundary fault")
	}
	boundaryTestExpectDenied(t, fmt.Errorf("ordinary: %w", ErrDenied), ErrDenied)
}

type boundaryTestCause struct{ detail string }

func (e *boundaryTestCause) Error() string { return e.detail }

func TestResourceBoundaryPreservesTypedCauseWithoutLeakingText(t *testing.T) {
	r := boundaryTestFacts()
	cause := &boundaryTestCause{detail: "private database credentials and resource identities"}
	r.err = errors.Join(ErrDenied, fmt.Errorf("backend: %w", cause))
	b := NewResourceBoundary(r, r)
	for _, err := range []error{b.CheckBucket(t.Context(), "Alpha", "Bucket"), b.CheckCoverage(t.Context(), r.coverage.Scope)} {
		boundaryTestExpectFault(t, err)
		var got *boundaryTestCause
		if !errors.As(err, &got) || got != cause || !errors.Is(err, ErrDenied) {
			t.Fatalf("lost typed or sentinel cause: %v", err)
		}
		if strings.Contains(err.Error(), cause.detail) || strings.Contains(err.Error(), "Alpha") {
			t.Fatalf("boundary error leaked internal detail: %v", err)
		}
	}
}

func TestResourceBoundaryContextFailures(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	expired, stop := context.WithDeadline(t.Context(), time.Unix(1, 0))
	defer stop()
	for _, ctx := range []context.Context{nil, canceled, expired} {
		r := boundaryTestFacts()
		b := NewResourceBoundary(r, r)
		for _, err := range []error{b.CheckBucket(ctx, "Alpha", "Bucket"), b.CheckCoverage(ctx, r.coverage.Scope)} {
			boundaryTestExpectFault(t, err)
			if ctx != nil && !errors.Is(err, ctx.Err()) {
				t.Fatalf("lost context cause: %v", err)
			}
		}
		if r.bindingCalls != 0 || r.coverageCalls != 0 {
			t.Fatal("read despite unusable context")
		}
	}
}

func TestResourceBoundaryCancellationDuringObservation(t *testing.T) {
	for _, coverage := range []bool{false, true} {
		for _, backendErr := range []error{nil, spaces.ErrNotFound} {
			ctx, cancel := context.WithCancel(t.Context())
			r := boundaryTestFacts()
			r.afterRead, r.err = cancel, backendErr
			b := NewResourceBoundary(r, r)
			var err error
			if coverage {
				err = b.CheckCoverage(ctx, r.coverage.Scope)
			} else {
				err = b.CheckBucket(ctx, "Alpha", "Bucket")
			}
			cancel()
			boundaryTestExpectFault(t, err)
			if !errors.Is(err, context.Canceled) || r.ctx != ctx {
				t.Fatalf("lost cancellation: %v", err)
			}
			if backendErr != nil && !errors.Is(err, backendErr) {
				t.Fatalf("lost simultaneous backend cause: %v", err)
			}
		}
	}
}

func TestResourceBoundaryAlwaysReobservesAndHasNoPrincipalBypass(t *testing.T) {
	r := boundaryTestFacts()
	b := NewResourceBoundary(r, r)
	if err := b.CheckBucket(t.Context(), "Alpha", "Bucket"); err != nil {
		t.Fatal(err)
	}
	if err := b.CheckCoverage(t.Context(), r.coverage.Scope); err != nil {
		t.Fatal(err)
	}
	boundaryTestSetBound(r)
	contexts := []context.Context{t.Context(), SystemContext(t.Context(), "Alpha"),
		WithPrincipal(t.Context(), Principal{TenantID: "*", Scopes: []string{"admin", "*"}})}
	for _, ctx := range contexts {
		boundaryTestExpectDenied(t, b.CheckBucket(ctx, "Alpha", "Bucket"), ErrSpaceBound)
		boundaryTestExpectDenied(t, b.CheckCoverage(ctx, r.coverage.Scope), ErrSpaceBound)
	}
	if r.bindingCalls != 4 || r.coverageCalls != 4 {
		t.Fatal("cached a previous absence observation")
	}
}
