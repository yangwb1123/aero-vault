package rest

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// Pin all ten common constructors, not inline per-Test databases or setupTestWith.
func TestRESTFixtureSchemaAdaptersCommonConstructorWiring(t *testing.T) {
	cases := []struct{ file, function, ctx, database string }{
		{"acl_test.go", "newAuthRESTTest", "context.Background()", "acl.db"},
		{"conditional_test.go", "newRESTTest", "context.Background()", "c.db"},
		{"thumbnail_cache_test.go", "newThumbnailCacheREST", "context.Background()", "c.db"},
		{"thumbnail_coalesce_test.go", "newThumbnailCoalesceREST", "context.Background()", "c.db"},
		{"thumbnail_identity_test.go", "newThumbnailIdentityREST", "context.Background()", "thumb.db"},
		{"thumbnail_test.go", "newRESTTestWithRepo", "context.Background()", "c.db"},
		{"thumbnail_test.go", "newAuthRESTTestWithRepo", "context.Background()", "acl.db"},
		{"thumbnail_test.go", "newThumbnailAccessHarness", "ctx", "access.db"},
		{"thumbnail_test.go", "newThumbnailFailingGetServer", "context.Background()", "fg.db"},
		{"thumbnail_test.go", "newThumbnail304Harness", "context.Background()", "c.db"},
	}
	for _, tc := range cases {
		t.Run(tc.function, func(t *testing.T) {
			want := fmt.Sprintf("dir := restFixtureSchemaDir(t, %s, %q)", tc.ctx, tc.database)
			assertRESTFixtureSchemaAdapterWiring(t, tc.file, tc.function, want)
		})
	}
}

func assertRESTFixtureSchemaAdapterWiring(t *testing.T, path, name, want string) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != name {
			continue
		}
		var body bytes.Buffer
		if err := format.Node(&body, fset, fn.Body); err != nil {
			t.Fatal(err)
		}
		if strings.Count(body.String(), "\n\t"+want+"\n") != 1 {
			t.Fatalf("%s: missing exact common-fixture adapter: %s", name, want)
		}
		return
	}
	t.Fatalf("common fixture %s not found in %s", name, path)
}
