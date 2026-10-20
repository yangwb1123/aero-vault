package rest

import (
	"fmt"
	"testing"
)

type restFixtureRemainingWiring struct {
	file, function, ctx, database string
	inline                        bool
}

func TestRESTFixtureSchemaRemainingConstructorWiring(t *testing.T) {
	cases := []restFixtureRemainingWiring{
		{"admin_audit_test.go", "newAdminAuditTest", "context.Background()", "audit.db", false},
		{"admin_files_delete_test.go", "newAdminDeleteEnv", "ctx", "admin-delete.db", false},
		{"admin_files_delete_test.go", "envRepo", "ctx", "mgr.db", true},
		{"admin_keys_test.go", "testAdminHandler", "context.Background()", "admin.db", true},
		{"admin_scope_test.go", "newAdminScopeTestServer", "ctx", "scope.db", false},
		{"admin_tenants_test.go", "newAdminTenantTest", "context.Background()", "tenants.db", false},
		{"buckets_test.go", "newBucketTestServer", "ctx", "b.db", false},
		{"buckets_test.go", "newBucketQuotaAuthTestServer", "ctx", "quota-auth.db", false},
		{"enterprise_access_test.go", "newEnterpriseRESTTest", "ctx", "enterprise.db", false},
		{"idempotency_test.go", "idemTestRepo", "context.Background()", "idem.db", true},
		{"management_test.go", "newManagementRESTTest", "ctx", "x.db", false},
		{"thumbnail_close_test.go", "newCloseErrorThumbnailREST", "ctx", "close-errors.db", false},
		{"thumbnail_close_test.go", "newCompletePayloadErrorREST", "ctx", "thumbnail.db", false},
		{"thumbnail_content_encoding_test.go", "newContentEncodingHarness", "context.Background()", "encoding.db", false},
		{"thumbnail_error_redaction_test.go", "newPrefixErrorThumbnailREST", "ctx", "prefix-error.db", false},
		{"thumbnail_sniff_test.go", "newNilThumbnailSourceREST", "ctx", "nil-source.db", false},
		{"thumbnail_sniff_test.go", "newDeadlineSniffREST", "ctx", "deadline-sniff.db", false},
	}
	for _, tc := range cases {
		t.Run(tc.function, func(t *testing.T) {
			assertRESTFixtureRemainingWiring(t, tc)
		})
	}
}

func assertRESTFixtureRemainingWiring(t *testing.T, tc restFixtureRemainingWiring) {
	t.Helper()
	seed := fmt.Sprintf("restFixtureSchemaDir(t, %s, %q)", tc.ctx, tc.database)
	dir := "dir"
	if tc.inline {
		dir = seed
	} else {
		assertRESTFixtureSchemaAdapterWiring(t, tc.file, tc.function, "dir := "+seed)
	}
	path := fmt.Sprintf("\"file:\"+filepath.Join(%s, %q)", dir, tc.database)
	if tc.function == "newAdminDeleteEnv" {
		assertRESTFixtureSchemaAdapterWiring(t, tc.file, tc.function, fmt.Sprintf("dsn := \"file:\" + filepath.Join(dir, %q)", tc.database))
		path = "dsn"
	}
	open := fmt.Sprintf("repo, err := repository.Open(%s, \"sqlite\", %s)", tc.ctx, path)
	assertRESTFixtureSchemaAdapterWiring(t, tc.file, tc.function, open)
	migrate := fmt.Sprintf("if err := repo.Migrate(%s); err != nil {", tc.ctx)
	assertRESTFixtureSchemaAdapterWiring(t, tc.file, tc.function, migrate)
}
