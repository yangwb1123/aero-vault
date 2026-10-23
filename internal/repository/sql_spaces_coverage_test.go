package repository

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/spaces"
	"github.com/aero-vault/aero-vault/internal/storage"
	"github.com/jackc/pgx/v5/pgconn"
	"modernc.org/sqlite"
)

func coverageSQLTestTenant(tenant string) spaces.CoverageScope {
	return spaces.CoverageScope{Kind: spaces.CoverageTenant, TenantID: tenant}
}

func coverageSQLTestWitness(v spaces.Space, status string) spaces.BindingResult {
	return spaces.BindingResult{TenantID: v.TenantID, Bucket: v.BackingBucket, TenantKnown: true,
		TenantStatus: status, State: spaces.BindingBound, SpaceID: v.ID, Revision: v.Revision}
}

func coverageSQLTestWant(scope spaces.CoverageScope, known bool, status string, witnesses ...spaces.BindingResult) spaces.CoverageResult {
	state := spaces.CoverageUnbound
	if len(witnesses) > 0 {
		state = spaces.CoverageBound
	}
	sort.Slice(witnesses, func(i, j int) bool {
		if witnesses[i].TenantID != witnesses[j].TenantID {
			return witnesses[i].TenantID < witnesses[j].TenantID
		}
		return witnesses[i].Bucket < witnesses[j].Bucket
	})
	return spaces.CoverageResult{Scope: scope, TenantKnown: known, TenantStatus: status,
		State: state, Complete: true, Witnesses: witnesses}
}

func coverageSQLTestObserve(t *testing.T, reader spaces.CoverageReader, want spaces.CoverageResult) spaces.CoverageResult {
	t.Helper()
	got, err := reader.ProbeSpaceCoverage(context.Background(), want.Scope)
	spaceTestOK(t, err)
	spaceTestOK(t, spaces.ValidateCoverageObservation(want.Scope, got))
	spaceTestEqual(t, "complete authoritative observation", got, want)
	return got
}

func coverageSQLTestFault(t *testing.T, got spaces.CoverageResult, err, cause error) {
	t.Helper()
	if !reflect.DeepEqual(got, spaces.CoverageResult{}) {
		t.Errorf("fault returned partial coverage: %+v", got)
	}
	if !errors.Is(err, spaces.ErrPersistence) {
		t.Errorf("fault must retain persistence class: %v", err)
	}
	if cause != nil && !errors.Is(err, cause) {
		t.Errorf("lost cause %v: %v", cause, err)
	}
}

func TestSpaceCoverageCompleteObservations(t *testing.T) {
	s, _ := spaceTestDB(t)
	global, alpha := spaces.CoverageScope{Kind: spaces.CoverageGlobal}, coverageSQLTestTenant("alpha")
	coverageSQLTestObserve(t, s, coverageSQLTestWant(global, false, ""))
	coverageSQLTestObserve(t, s, coverageSQLTestWant(alpha, true, "active"))
	coverageSQLTestObserve(t, s, coverageSQLTestWant(coverageSQLTestTenant("ghost"), false, ""))
	command := spaceTestCreate("alpha", spaces.Team, spaceTestSubject("issuer", "human"))
	a := spaceTestSpace(t, s, command)
	command.TenantID = "beta"
	b := spaceTestSpace(t, s, command) // Same UUID and bucket, different tenant.
	other := spaceTestSpace(t, s, spaceTestCreate("alpha", spaces.Personal, command.Actor))
	spaceTestApply(t, s, &a, spaceTestChange(a, spaceTestSubject("issuer", "viewer"), spaces.AddMember, spaces.Viewer))
	aw, bw, ow := coverageSQLTestWitness(a, "active"), coverageSQLTestWitness(b, "active"), coverageSQLTestWitness(other, "active")
	coverageSQLTestObserve(t, s, coverageSQLTestWant(alpha, true, "active", aw, ow))
	coverageSQLTestObserve(t, s, coverageSQLTestWant(coverageSQLTestTenant("beta"), true, "active", bw))
	coverageSQLTestObserve(t, s, coverageSQLTestWant(global, false, "", aw, bw, ow))
	for _, tenant := range []string{"ghost", "Alpha", "alpha' OR '1'='1"} {
		coverageSQLTestObserve(t, s, coverageSQLTestWant(coverageSQLTestTenant(tenant), false, ""))
	}
}

func TestSpaceCoverageRawTenantStates(t *testing.T) {
	s, _ := spaceTestDB(t)
	ctx := context.Background()
	v := spaceTestSpace(t, s, spaceTestCreate("alpha", spaces.Team, spaceTestSubject("issuer", "human")))
	for _, status := range []string{"active", "disabled", "Paused", "", " active "} {
		_, err := s.db.ExecContext(ctx, s.rebind(`UPDATE tenants SET status=$1 WHERE tenant_id=$2 OR tenant_id=$3`), status, "alpha", "beta")
		spaceTestOK(t, err)
		witness := coverageSQLTestWitness(v, status)
		coverageSQLTestObserve(t, s, coverageSQLTestWant(coverageSQLTestTenant("alpha"), true, status, witness))
		coverageSQLTestObserve(t, s, coverageSQLTestWant(coverageSQLTestTenant("beta"), true, status))
		coverageSQLTestObserve(t, s, coverageSQLTestWant(spaces.CoverageScope{Kind: spaces.CoverageGlobal}, false, "", witness))
	}
}

func TestSpaceCoverageUsesBindingsNotBucketNames(t *testing.T) {
	s, _ := spaceTestDB(t)
	ctx := context.Background()
	for _, fact := range []struct {
		tenant, status string
		known          bool
	}{{"alpha", "active", true}, {"legacy'--/桶", "", false}} {
		for _, bucket := range []string{"default", "spc-12345678-1234-1234-1234-123456789abc"} {
			spaceTestOK(t, s.CreateBucket(ctx, fact.tenant, bucket))
		}
		coverageSQLTestObserve(t, s, coverageSQLTestWant(coverageSQLTestTenant(fact.tenant), fact.known, fact.status))
	}
	v := spaceTestSpace(t, s, spaceTestCreate("alpha", spaces.Team, spaceTestSubject("issuer", "human")))
	// Test-only persisted binding that cannot be inferred from a bucket prefix.
	v.BackingBucket = "ordinary'--/桶"
	spaceTestOK(t, s.CreateBucket(ctx, "alpha", v.BackingBucket))
	_, err := s.db.ExecContext(ctx, s.rebind(`UPDATE spaces SET backing_bucket=$1 WHERE tenant_id=$2 AND id=$3`), v.BackingBucket, v.TenantID, v.ID)
	spaceTestOK(t, err)
	witness := coverageSQLTestWitness(v, "active")
	coverageSQLTestObserve(t, s, coverageSQLTestWant(coverageSQLTestTenant("alpha"), true, "active", witness))
	coverageSQLTestObserve(t, s, coverageSQLTestWant(spaces.CoverageScope{Kind: spaces.CoverageGlobal}, false, "", witness))
}

func TestSpaceCoverageReadOnlyAndInvalidScopes(t *testing.T) {
	s, _, _ := bindingTestFacts(t)
	local, err := storage.NewLocal(storage.LocalConfig{Root: t.TempDir()})
	spaceTestOK(t, err)
	objects := bindingTestLegacy(t, s, local)
	before, blobs := bindingTestSnapshot(t, s), bindingTestBlobs(t, local, objects)
	for _, scope := range []spaces.CoverageScope{coverageSQLTestTenant("alpha"), coverageSQLTestTenant("legacy-tenant"), {Kind: spaces.CoverageGlobal}} {
		got, err := s.ProbeSpaceCoverage(context.Background(), scope)
		spaceTestOK(t, err)
		spaceTestOK(t, spaces.ValidateCoverageObservation(scope, got))
		if !got.Complete {
			t.Fatalf("successful SQL observation incomplete: %+v", got)
		}
	}
	spaceTestEqual(t, "all persisted logical facts", bindingTestSnapshot(t, s), before)
	spaceTestEqual(t, "legacy Blob bytes", bindingTestBlobs(t, local, objects), blobs)
	spaceTestOK(t, s.Close())
	bad := []spaces.CoverageScope{{}, {Kind: "other"}, {Kind: spaces.CoverageGlobal, TenantID: "alpha"}}
	for _, tenant := range []string{"", "*", " alpha", "alpha ", "\xff", "a\x00b", "a\nb", "a\tb"} {
		bad = append(bad, coverageSQLTestTenant(tenant))
	}
	for _, scope := range bad {
		got, err := s.ProbeSpaceCoverage(context.Background(), scope)
		spaceTestError(t, err, spaces.ErrInvalidArgument)
		spaceTestEqual(t, "invalid input before SQL", got, spaces.CoverageResult{})
		if errors.Is(err, spaces.ErrPersistence) {
			t.Fatalf("invalid scope reached closed database: %v", err)
		}
	}
}

func TestSpaceCoverageContextAndClosedDatabase(t *testing.T) {
	s, _ := spaceTestDB(t)
	spaceTestSpace(t, s, spaceTestCreate("alpha", spaces.Team, spaceTestSubject("issuer", "human")))
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer stop()
	scopes := []spaces.CoverageScope{coverageSQLTestTenant("alpha"), coverageSQLTestTenant("ghost"), {Kind: spaces.CoverageGlobal}}
	for _, scope := range scopes {
		for _, ctx := range []context.Context{cancelled, expired} {
			got, err := s.ProbeSpaceCoverage(ctx, scope)
			coverageSQLTestFault(t, got, err, ctx.Err())
		}
	}
	spaceTestOK(t, s.Close())
	cause := s.Ping(context.Background())
	for _, scope := range scopes {
		got, err := s.ProbeSpaceCoverage(context.Background(), scope)
		coverageSQLTestFault(t, got, err, cause)
		if errors.Is(err, spaces.ErrNotFound) {
			t.Fatalf("database fault classified as absence: %v", err)
		}
	}
}

func TestSpaceCoverageRealScanAndQueryFaults(t *testing.T) {
	s, _ := spaceTestDB(t)
	command := spaceTestCreate("alpha", spaces.Team, spaceTestSubject("issuer", "human"))
	command.ID = "12345678-1234-1234-1234-123456789abc"
	spaceTestSpace(t, s, command)
	command = spaceTestCreate("alpha", spaces.Team, command.Actor)
	command.ID = "22345678-1234-1234-1234-123456789abc"
	spaceTestSpace(t, s, command)
	// Only this temporary migrated database is altered for fault injection.
	_, err := s.db.ExecContext(context.Background(), `ALTER TABLE spaces RENAME TO coverage_saved_spaces;
CREATE VIEW spaces AS SELECT tenant_id,backing_bucket,id,
CASE WHEN id='22345678-1234-1234-1234-123456789abc' THEN 'bad-integer' ELSE revision END AS revision
FROM coverage_saved_spaces`)
	spaceTestOK(t, err)
	for _, scope := range []spaces.CoverageScope{coverageSQLTestTenant("alpha"), {Kind: spaces.CoverageGlobal}} {
		got, err := s.ProbeSpaceCoverage(context.Background(), scope)
		coverageSQLTestFault(t, got, err, nil)
		if err == nil || !strings.Contains(err.Error(), "bad-integer") {
			t.Fatalf("real scan failure lost: %v", err)
		}
	}
	_, err = s.db.ExecContext(context.Background(), `DROP VIEW spaces`)
	spaceTestOK(t, err)
	for _, scope := range []spaces.CoverageScope{coverageSQLTestTenant("ghost"), {Kind: spaces.CoverageGlobal}} {
		got, err := s.ProbeSpaceCoverage(context.Background(), scope)
		coverageSQLTestFault(t, got, err, nil)
		var cause *sqlite.Error
		if !errors.As(err, &cause) || cause.Code() != 1 {
			t.Fatalf("lost real SQLite typed cause: %v", err)
		}
	}
}

// A narrow row seam exercises failures database/sql cannot reliably force,
// including a Close failure after the last successfully scanned binding.
type coverageSQLTestRows struct {
	values                     [][6]any
	index, closes              int
	scanErr, iterErr, closeErr error
}

func (r *coverageSQLTestRows) Next() bool {
	r.index++
	return r.index <= len(r.values)
}

func (r *coverageSQLTestRows) Scan(dest ...any) error {
	return (bindingTestScanner{values: r.values[r.index-1], err: r.scanErr}).Scan(dest...)
}

func (r *coverageSQLTestRows) Err() error { return r.iterErr }

func (r *coverageSQLTestRows) Close() error {
	r.closes++
	return r.closeErr
}

func coverageSQLTestRow() [6]any {
	return [6]any{"alpha", "active", "alpha", "bucket", "12345678-1234-1234-1234-123456789abc", int64(1)}
}

func coverageSQLTestInvalidRows() [][6]any {
	base := coverageSQLTestRow()
	id := base[4]
	bad := [][6]any{
		{"alpha", nil, nil, nil, nil, nil}, {nil, "active", nil, nil, nil, nil},
		{nil, nil, "alpha", "bucket", id, int64(1)},
		{"beta", "active", "alpha", "bucket", id, int64(1)},
		{"alpha", "active", "beta", "bucket", id, int64(1)},
	}
	for i := 2; i < len(base); i++ {
		row := base
		row[i] = nil
		bad = append(bad, row)
	}
	for i, values := range map[int][]any{
		3: {"", "*", " bucket", "\xff", "a\x00b"},
		4: {"", "bad", "00000000-0000-0000-0000-000000000000", strings.ToUpper(id.(string))},
		5: {int64(0), int64(-1)},
	} {
		for _, value := range values {
			row := base
			row[i] = value
			bad = append(bad, row)
		}
	}
	return bad
}

func TestSpaceCoverageRejectsMalformedObservations(t *testing.T) {
	bad := coverageSQLTestInvalidRows()
	for _, scope := range []spaces.CoverageScope{coverageSQLTestTenant("alpha"), {Kind: spaces.CoverageGlobal}} {
		for _, row := range bad {
			rows := &coverageSQLTestRows{values: [][6]any{row}}
			got, err := scanSpaceCoverage(rows, scope)
			coverageSQLTestFault(t, got, spaceCoverageSQLError(context.Background(), err), spaces.ErrInvalidArgument)
			spaceTestEqual(t, "closed on malformed row", rows.closes, 1)
		}
		good := coverageSQLTestRow()
		conflict := good
		conflict[1], conflict[3], conflict[4] = "disabled", "other", "22345678-1234-1234-1234-123456789abc"
		for _, values := range [][][6]any{{good, good}, {good, conflict}, {{}, good}, {good, {}}} {
			got, err := scanSpaceCoverage(&coverageSQLTestRows{values: values}, scope)
			coverageSQLTestFault(t, got, spaceCoverageSQLError(context.Background(), err), spaces.ErrInvalidArgument)
		}
	}
}

func TestSpaceCoverageRowFaultCauses(t *testing.T) {
	pg := &pgconn.PgError{Code: "40001", Message: "synthetic scan failure"}
	iteration, closeCause := errors.New("iteration fault"), errors.New("close fault")
	for _, rows := range []*coverageSQLTestRows{
		{values: [][6]any{coverageSQLTestRow()}, scanErr: pg},
		{values: [][6]any{coverageSQLTestRow()}, iterErr: iteration},
		{values: [][6]any{coverageSQLTestRow()}, closeErr: closeCause},
		{values: [][6]any{coverageSQLTestRow()}, iterErr: iteration, closeErr: closeCause},
		{},
	} {
		got, err := scanSpaceCoverage(rows, coverageSQLTestTenant("alpha"))
		err = spaceCoverageSQLError(context.Background(), err)
		coverageSQLTestFault(t, got, err, nil)
		for _, cause := range []error{rows.scanErr, rows.iterErr, rows.closeErr} {
			if cause != nil {
				spaceTestError(t, err, cause)
			}
		}
		if rows.scanErr != nil {
			var retained *pgconn.PgError
			if !errors.As(err, &retained) || retained != pg {
				t.Fatalf("lost synthetic typed cause (not a real PG test): %v", err)
			}
		}
		if len(rows.values) == 0 {
			spaceTestError(t, err, sql.ErrNoRows)
			if errors.Is(err, spaces.ErrNotFound) {
				t.Fatalf("missing observation classified as unbound/not found: %v", err)
			}
		}
		spaceTestEqual(t, "rows closed exactly once", rows.closes, 1)
	}
}

func TestSpaceCoverageContextJoinsOriginalCause(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cause := errors.Join(spaces.ErrNotFound, errors.New("driver failure"))
	err := spaceCoverageSQLError(ctx, cause)
	coverageSQLTestFault(t, spaces.CoverageResult{}, err, cause)
	spaceTestError(t, err, spaces.ErrNotFound) // Cause is retained, but persistence takes precedence.
	spaceTestError(t, err, context.Canceled)
}

func TestSpaceCoverageSQLPlaceholderOrder(t *testing.T) {
	for query, count := range map[string]int{spaceCoverageTenantQuery: 1, spaceCoverageGlobalQuery: 0} {
		if !bindingTestOrderedBinds(query, count) {
			t.Fatalf("coverage query violates I1 occurrence order: %s", query)
		}
		if strings.Contains(strings.ToUpper(query), "LIMIT") || strings.Contains(query, "buckets") {
			t.Fatalf("coverage must not depend on a limited bucket inventory: %s", query)
		}
		pg := &sqlStore{dialect: dialectPostgres}
		spaceTestEqual(t, "static PG rebind, not real execution", pg.rebind(query), query)
	}
}
