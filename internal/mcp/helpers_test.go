package mcp

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/ai"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

// newTestServer sets up the full stack and returns a ready-to-use *Server.
// search may be nil (stdio/nil path) or a real *ai.Search.
func newTestServer(t *testing.T, search *ai.Search) (*Server, *service.FileService, repository.Repository) {
	t.Helper()
	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repo.Close() })
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatal(err)
	}
	svc := service.NewFileService(store, repo, nil).WithAuthorizer(allowAllProvider{})
	srv := NewServer(svc, repo, search, "default", nil)
	return srv, svc, repo
}

// allowAllProvider is the CI-baseline test double injected into the mcp test
// helper: it preserves the pre-fail-closed baseline (all actions allowed) for
// tests exercising MCP behavior other than the delete gate. The default-config
// (no authorizer) delete denial is covered by TestCallTool_DeleteFile_FailClosed
// in server_test.go.
type allowAllProvider struct{}

func (allowAllProvider) Authorize(context.Context, access.Principal, access.Action, access.Resource) (access.Decision, error) {
	return access.Decision{Allowed: true, Reason: "test_allow_all"}, nil
}

// seedObject uploads content under tenant/bucket/key via the service.
func seedObject(t *testing.T, svc *service.FileService, tenant, bucket, key, content string) repository.Object {
	t.Helper()
	obj, err := svc.Put(
		context.Background(), tenant, bucket, key,
		strings.NewReader(content), int64(len(content)),
		service.PutOptions{ContentType: "text/plain"},
	)
	if err != nil {
		t.Fatalf("seedObject %s/%s/%s: %v", tenant, bucket, key, err)
	}
	return obj
}

// seedChunks inserts chunks for a seeded object so the search tool has data.
func seedChunks(t *testing.T, repo repository.Repository, obj repository.Object, contents []string, emb *ai.HashEmbedder) {
	t.Helper()
	ctx := context.Background()
	chunks := make([]repository.Chunk, 0, len(contents))
	for i, c := range contents {
		vecs, err := emb.Embed(ctx, []string{c})
		if err != nil {
			t.Fatalf("embed chunk %d: %v", i, err)
		}
		chunks = append(chunks, repository.Chunk{
			ObjectID:   obj.ID,
			TenantID:   obj.TenantID,
			Bucket:     obj.Bucket,
			ObjectKey:  obj.Key,
			Seq:        i,
			Content:    c,
			Embedding:  vecs[0],
			Dim:        emb.Dimensions(),
			EmbedModel: emb.Name(),
		})
	}
	if err := repo.InsertChunks(ctx, chunks); err != nil {
		t.Fatalf("InsertChunks: %v", err)
	}
}

// mustReadAll reads all bytes from rc and closes it; fatal on error.
func mustReadAll(t *testing.T, rc io.ReadCloser) []byte {
	t.Helper()
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	return b
}
