package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"path"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/spaces"
	"github.com/aero-vault/aero-vault/internal/storage"
	"github.com/jackc/pgx/v5/pgconn"
	"modernc.org/sqlite"
)

func bindingTestWant(tenant, bucket, status, id string, revision int64, known bool) spaces.BindingResult {
	state := spaces.BindingUnbound
	if id != "" {
		state = spaces.BindingBound
	}
	return spaces.BindingResult{TenantID: tenant, Bucket: bucket, TenantKnown: known,
		TenantStatus: status, State: state, SpaceID: id, Revision: revision}
}

func bindingTestProbe(t *testing.T, reader spaces.BindingReader, want spaces.BindingResult) {
	t.Helper()
	got, err := reader.ProbeSpaceBinding(context.Background(), want.TenantID, want.Bucket)
	if err != nil || got != want {
		t.Fatalf("probe got=%+v err=%v, want=%+v", got, err, want)
	}
	spaceTestOK(t, spaces.ValidateBindingResult(got))
}

func bindingTestFault(t *testing.T, got spaces.BindingResult, err, cause error) {
	t.Helper()
	if got != (spaces.BindingResult{}) {
		t.Errorf("fault returned partial facts: %+v", got)
	}
	if !errors.Is(err, spaces.ErrPersistence) {
		t.Errorf("fault must retain persistence class: %v", err)
	}
	if err == nil || errors.Is(err, spaces.ErrNotFound) {
		t.Errorf("fault confused with absence: %v", err)
	}
	if cause != nil && !errors.Is(err, cause) {
		t.Errorf("lost cause %v: %v", cause, err)
	}
}

func bindingTestFacts(t *testing.T) (*sqlStore, spaces.Space, []spaces.BindingResult) {
	t.Helper()
	s, _ := spaceTestDB(t)
	ctx := context.Background()
	command := spaceTestCreate("alpha", spaces.Team, spaceTestSubject("issuer", "human"))
	a := spaceTestSpace(t, s, command)
	command.TenantID = "beta"
	b := spaceTestSpace(t, s, command) // Same UUID and bucket in another tenant.
	spaceTestApply(t, s, &a, spaceTestChange(a, spaceTestSubject("issuer", "viewer"), spaces.AddMember, spaces.Viewer))
	onlyBeta := spaceTestSpace(t, s, spaceTestCreate("beta", spaces.Team, b.CreatedBy))
	spaceTestOK(t, s.UpsertTenant(ctx, TenantRecord{TenantID: "beta", Status: "disabled"}))
	spaceTestOK(t, s.UpsertTenant(ctx, TenantRecord{TenantID: "odd", Status: "Paused"}))
	spaceTestOK(t, s.UpsertTenant(ctx, TenantRecord{TenantID: "empty-status"}))
	_, err := s.db.ExecContext(ctx, `UPDATE tenants SET status='' WHERE tenant_id='empty-status'`)
	spaceTestOK(t, err)
	spaceTestOK(t, s.CreateBucket(ctx, "legacy-tenant", "legacy"))
	a, err = s.GetSpace(ctx, "alpha", a.ID)
	spaceTestOK(t, err) // Capture the full post-command root, including UpdatedAt.
	return s, a, []spaces.BindingResult{
		bindingTestWant("alpha", a.BackingBucket, "active", a.ID, 2, true),
		bindingTestWant("alpha", "absent", "active", "", 0, true),
		bindingTestWant("beta", b.BackingBucket, "disabled", b.ID, 1, true),
		bindingTestWant("beta", "absent", "disabled", "", 0, true),
		bindingTestWant("alpha", onlyBeta.BackingBucket, "active", "", 0, true),
		bindingTestWant("legacy-tenant", "legacy", "", "", 0, false),
		bindingTestWant("legacy-tenant", "missing", "", "", 0, false),
		bindingTestWant("ghost", a.BackingBucket, "", "", 0, false),
		bindingTestWant("alpha", "spc-12345678-1234-1234-1234-123456789abc", "active", "", 0, true),
		bindingTestWant("Alpha", a.BackingBucket, "", "", 0, false),
		bindingTestWant("alpha", strings.ToUpper(a.BackingBucket), "active", "", 0, true),
		bindingTestWant("odd", "absent", "Paused", "", 0, true),
		bindingTestWant("empty-status", "absent", "", "", 0, true),
	}
}

func TestSpaceBindingFactsAndIsolation(t *testing.T) {
	s, _, wants := bindingTestFacts(t)
	for _, want := range wants {
		bindingTestProbe(t, s, want)
	}
	for _, status := range []string{"Paused", ""} {
		_, err := s.db.ExecContext(context.Background(), s.rebind(`UPDATE tenants SET status=$1 WHERE tenant_id=$2`), status, "beta")
		spaceTestOK(t, err)
		for _, want := range wants[2:4] {
			want.TenantStatus = status
			bindingTestProbe(t, s, want)
		}
	}
	bindingTestProbe(t, s, bindingTestWant("alpha", "literal'--", "active", "", 0, true))
	bindingTestProbe(t, s, bindingTestWant("alpha' OR '1'='1", wants[0].Bucket, "", "", 0, false))
}

func TestSpaceBindingInvalidInput(t *testing.T) {
	s, _, wants := bindingTestFacts(t)
	before := bindingTestSnapshot(t, s)
	for _, bad := range []string{"", "*", " alpha", "alpha ", "\xff", "a\x00b", "a\nb", "a\tb"} {
		for _, pair := range [][2]string{{bad, wants[0].Bucket}, {"alpha", bad}} {
			got, err := s.ProbeSpaceBinding(context.Background(), pair[0], pair[1])
			spaceTestError(t, err, spaces.ErrInvalidArgument)
			spaceTestEqual(t, "invalid input zero result", got, spaces.BindingResult{})
		}
	}
	spaceTestEqual(t, "invalid inputs leave all facts", bindingTestSnapshot(t, s), before)
	spaceTestOK(t, s.Close())
	// Even a closed DB cannot override input validation or normalize bad keys.
	got, err := s.ProbeSpaceBinding(context.Background(), " alpha", wants[0].Bucket)
	spaceTestError(t, err, spaces.ErrInvalidArgument)
	spaceTestEqual(t, "input rejected before SQL", got, spaces.BindingResult{})
}

func bindingTestTable(t *testing.T, s *sqlStore, table string) []string {
	t.Helper()
	rows, err := s.db.QueryContext(context.Background(), `SELECT * FROM `+table)
	spaceTestOK(t, err)
	columns, err := rows.Columns()
	if err != nil {
		_ = rows.Close()
		t.Fatal(err)
	}
	values, err := collectSpaceRows(rows, func(row spaceScanner) (string, error) {
		data, ptrs := make([]any, len(columns)), make([]any, len(columns))
		for i := range data {
			ptrs[i] = &data[i]
		}
		if err := row.Scan(ptrs...); err != nil {
			return "", err
		}
		encoded, err := json.Marshal(data)
		return string(encoded), err
	})
	spaceTestOK(t, err)
	sort.Strings(values)
	return append([]string{strings.Join(columns, ",")}, values...)
}

func bindingTestSnapshot(t *testing.T, s *sqlStore) map[string][]string {
	t.Helper()
	out := make(map[string][]string)
	for _, table := range []string{"tenants", "buckets", "spaces", "space_members", "space_history", "objects"} {
		out[table] = bindingTestTable(t, s, table)
	}
	return out
}

func bindingTestLegacy(t *testing.T, s *sqlStore, local *storage.LocalStorage) []Object {
	t.Helper()
	ctx := context.Background()
	var objects []Object
	for i, bucket := range []string{"default", "default", "legacy"} {
		spaceTestOK(t, s.CreateBucket(ctx, "default", bucket))
		version := NewVersionID()
		key := path.Join("default", bucket, "old@v-key") + "@v" + version
		content := fmt.Sprintf("legacy content %d", i)
		info, err := local.Put(ctx, key, strings.NewReader(content), int64(len(content)), storage.PutOptions{})
		spaceTestOK(t, err)
		obj := Object{TenantID: "default", Bucket: bucket, Key: "old@v-key", VersionID: version,
			Backend: "local", StorageKey: key, Size: info.Size, ETag: info.ETag,
			Metadata: map[string]string{"_aero_owner": fmt.Sprintf("owner-%d", i), "old": content}}
		obj, err = s.InsertObjectVersion(ctx, obj)
		spaceTestOK(t, err)
		objects = append(objects, obj)
	}
	return objects
}

func bindingTestBlobs(t *testing.T, local *storage.LocalStorage, objects []Object) map[string]string {
	t.Helper()
	out := make(map[string]string)
	for _, obj := range objects {
		r, _, err := local.Get(context.Background(), obj.StorageKey)
		spaceTestOK(t, err)
		data, err := io.ReadAll(r)
		closeErr := r.Close()
		spaceTestOK(t, err)
		spaceTestOK(t, closeErr)
		spaceTestEqual(t, "original Blob bytes", string(data), obj.Metadata["old"])
		out[obj.StorageKey] = string(data)
	}
	return out
}

func TestSpaceBindingReadOnly(t *testing.T) {
	s, _, wants := bindingTestFacts(t)
	local, err := storage.NewLocal(storage.LocalConfig{Root: t.TempDir()})
	spaceTestOK(t, err)
	objects := bindingTestLegacy(t, s, local)
	before, blobs := bindingTestSnapshot(t, s), bindingTestBlobs(t, local, objects)
	for _, want := range wants {
		bindingTestProbe(t, s, want)
	}
	for _, bucket := range []string{"default", "legacy"} {
		bindingTestProbe(t, s, bindingTestWant("default", bucket, "", "", 0, false))
	}
	got, err := s.ProbeSpaceBinding(context.Background(), "*", "legacy")
	spaceTestError(t, err, spaces.ErrInvalidArgument)
	spaceTestEqual(t, "invalid zero", got, spaces.BindingResult{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err = s.ProbeSpaceBinding(ctx, "alpha", wants[0].Bucket)
	bindingTestFault(t, got, err, context.Canceled)
	spaceTestEqual(t, "all rows and logical column values", bindingTestSnapshot(t, s), before)
	spaceTestEqual(t, "all legacy Blob bytes", bindingTestBlobs(t, local, objects), blobs)
}

func TestSpaceBindingFaultsRetainCause(t *testing.T) {
	s, _ := spaceTestDB(t)
	v := spaceTestSpace(t, s, spaceTestCreate("alpha", spaces.Team, spaceTestSubject("issuer", "u")))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer stop()
	for _, ctx := range []context.Context{ctx, expired} {
		got, err := s.ProbeSpaceBinding(ctx, "alpha", v.BackingBucket)
		bindingTestFault(t, got, err, ctx.Err())
	}
	t.Run("real scan conversion", func(t *testing.T) {
		_, err := s.db.ExecContext(context.Background(), `ALTER TABLE spaces RENAME TO binding_saved_spaces;
CREATE VIEW spaces AS SELECT tenant_id,backing_bucket,id,'bad-integer' AS revision FROM binding_saved_spaces`)
		spaceTestOK(t, err)
		got, err := s.ProbeSpaceBinding(context.Background(), "alpha", v.BackingBucket)
		bindingTestFault(t, got, err, nil)
		row := s.db.QueryRowContext(context.Background(), s.rebind(spaceBindingQuery), "alpha", v.BackingBucket)
		partial, raw := scanSpaceBinding(row, "alpha", v.BackingBucket)
		bindingTestFault(t, partial, spaceBindingSQLError(context.Background(), "real scan", raw), raw)
		if raw == nil || !strings.Contains(err.Error(), "bad-integer") {
			t.Fatalf("lost actual scan detail: raw=%v probe=%v", raw, err)
		}
	})
	t.Run("unknown tenant still queries missing spaces", func(t *testing.T) {
		_, err := s.db.ExecContext(context.Background(), `DROP VIEW spaces`)
		spaceTestOK(t, err)
		got, err := s.ProbeSpaceBinding(context.Background(), "unknown", "legacy")
		bindingTestFault(t, got, err, nil)
		var cause *sqlite.Error
		if !errors.As(err, &cause) || cause.Code() != 1 {
			t.Fatalf("missing SQLite typed cause: %v", err)
		}
	})
	t.Run("closed database", func(t *testing.T) {
		spaceTestOK(t, s.Close())
		cause := s.Ping(context.Background())
		got, err := s.ProbeSpaceBinding(context.Background(), "alpha", v.BackingBucket)
		bindingTestFault(t, got, err, cause)
	})
}

type bindingTestScanner struct {
	values [6]any
	err    error
}

func (s bindingTestScanner) Scan(dest ...any) error {
	for i, value := range s.values {
		if err := dest[i].(sql.Scanner).Scan(value); err != nil {
			return err
		}
	}
	return s.err // Also models a late failure after every destination was filled.
}

func bindingTestInvalidRows() [][6]any {
	id := "12345678-1234-1234-1234-123456789abc"
	base := [6]any{"alpha", "active", "alpha", "bucket", id, int64(1)}
	rows := [][6]any{
		{"alpha", nil, nil, nil, nil, nil}, {nil, "active", nil, nil, nil, nil},
		{nil, nil, "alpha", "bucket", id, int64(1)},
		{"beta", "active", nil, nil, nil, nil},
	}
	for i := 2; i < len(base); i++ {
		row := base
		row[i] = nil
		rows = append(rows, row)
	}
	for i, values := range map[int][]any{2: {"beta"}, 3: {"other"}, 4: {"", "bad", "00000000-0000-0000-0000-000000000000"}, 5: {int64(0), int64(-1)}} {
		for _, value := range values {
			row := base
			row[i] = value
			rows = append(rows, row)
		}
	}
	return rows
}

func TestSpaceBindingScanValidation(t *testing.T) {
	ctx := context.Background()
	for _, values := range bindingTestInvalidRows() {
		got, err := scanSpaceBinding(bindingTestScanner{values: values}, "alpha", "bucket")
		bindingTestFault(t, got, spaceBindingSQLError(ctx, "invalid observation", err), spaces.ErrInvalidArgument)
	}
	for _, values := range [][6]any{{}, {"alpha", "", nil, nil, nil, nil}} {
		got, err := scanSpaceBinding(bindingTestScanner{values: values}, "alpha", "bucket")
		spaceTestOK(t, err)
		spaceTestEqual(t, "complete NULL absence", got, bindingTestWant("alpha", "bucket", "", "", 0, values[0] != nil))
	}
	filled := [6]any{"alpha", "active", "alpha", "bucket", "12345678-1234-1234-1234-123456789abc", int64(1)}
	bound, err := scanSpaceBinding(bindingTestScanner{values: filled}, "alpha", "bucket")
	spaceTestOK(t, err)
	spaceTestEqual(t, "non-prefixed bucket bound observation", bound, bindingTestWant("alpha", "bucket", "active", filled[4].(string), 1, true))
	for _, cause := range []error{errors.New("scan sentinel"), sql.ErrNoRows, errors.New("late close sentinel")} {
		got, err := scanSpaceBinding(bindingTestScanner{values: filled, err: cause}, "alpha", "bucket")
		bindingTestFault(t, got, spaceBindingSQLError(ctx, "scanner seam", err), cause)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	cause := errors.New("driver error with cancelled context")
	err = spaceBindingSQLError(cancelled, "context and driver", cause)
	bindingTestFault(t, spaces.BindingResult{}, err, cause)
	spaceTestError(t, err, context.Canceled)
	err = spaceBindingSQLError(cancelled, "unexpected no rows", sql.ErrNoRows)
	bindingTestFault(t, spaces.BindingResult{}, err, sql.ErrNoRows)
	spaceTestError(t, err, context.Canceled)
	pg := &pgconn.PgError{Code: "40001"}
	err = spaceBindingSQLError(ctx, "synthetic PG, not a real database", pg)
	spaceTestError(t, err, spaces.ErrConcurrent)
	var retained *pgconn.PgError
	if !errors.As(err, &retained) || retained != pg {
		t.Fatalf("lost synthetic PG cause: %v", err)
	}
}

func bindingTestOrderedBinds(query string, args int) bool {
	matches := regexp.MustCompile(`\$([0-9]+)`).FindAllStringSubmatch(query, -1)
	if len(matches) != args {
		return false
	}
	for i, match := range matches {
		if match[1] != strconv.Itoa(i+1) {
			return false
		}
	}
	return true
}

func TestSpaceBindingSQLBindingOccurrenceOrder(t *testing.T) {
	for _, bad := range []string{"SELECT $1,$1", "SELECT $1,$3", "SELECT $2,$1", "SELECT $2,$3"} {
		if bindingTestOrderedBinds(bad, 2) {
			t.Fatalf("I1 guard missed %s", bad)
		}
	}
	if !bindingTestOrderedBinds(spaceBindingQuery, 2) {
		t.Fatal("probe placeholders must be exactly $1 then $2")
	}
	want := `SELECT t.tenant_id, t.status, sp.tenant_id, sp.backing_bucket, sp.id, sp.revision
FROM (SELECT CAST($1 AS TEXT) AS tenant_id, CAST($2 AS TEXT) AS bucket_name) AS requested
LEFT JOIN tenants AS t ON t.tenant_id = requested.tenant_id
LEFT JOIN spaces AS sp ON sp.tenant_id = requested.tenant_id AND sp.backing_bucket = requested.bucket_name`
	if strings.Join(strings.Fields(spaceBindingQuery), " ") != strings.Join(strings.Fields(want), " ") {
		t.Fatal("single read-only observation deviated from registered query")
	}
	sq, pg := &sqlStore{dialect: dialectSQLite}, &sqlStore{dialect: dialectPostgres}
	if sq.rebind(spaceBindingQuery) != regexp.MustCompile(`\$[0-9]+`).ReplaceAllString(spaceBindingQuery, "?") {
		t.Fatal("SQLite rebind changed occurrence order")
	}
	if pg.rebind(spaceBindingQuery) != spaceBindingQuery {
		t.Fatal("PG static rebind changed SQL; this is not a real PG execution")
	}
	bindingTestQueryArguments(t)
}

func bindingTestQueryArguments(t *testing.T) {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "sql_spaces_binding.go", nil, 0)
	spaceTestOK(t, err)
	calls := 0
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "QueryRowContext" {
			return true
		}
		calls++
		if len(call.Args) != 4 {
			t.Error("probe requires ctx, rebound SQL, tenant, bucket")
			return false
		}
		for i, name := range []string{"tenant", "bucket"} {
			arg, ok := call.Args[i+2].(*ast.Ident)
			if !ok || arg.Name != name {
				t.Errorf("SQL argument %d must be %s", i+1, name)
			}
		}
		return true
	})
	if calls != 1 {
		t.Fatalf("probe must have exactly one SQL call, got %d", calls)
	}
}

func TestSpaceBindingStrictGetterCompatibility(t *testing.T) {
	s, a, wants := bindingTestFacts(t)
	for _, want := range wants {
		bindingTestProbe(t, s, want)
		got, err := s.GetSpaceByBucket(context.Background(), want.TenantID, want.Bucket)
		switch {
		case !want.TenantKnown || want.TenantStatus != "active":
			spaceTestError(t, err, spaces.ErrTenantUnavailable)
		case want.State == spaces.BindingUnbound:
			spaceTestError(t, err, spaces.ErrNotFound)
		default:
			spaceTestOK(t, err)
			spaceTestEqual(t, "unchanged strict root", got, a)
		}
	}
	if reflect.TypeFor[spaces.Store]().NumMethod() != 9 {
		t.Fatal("binding side port must not widen the existing Store")
	}
}
