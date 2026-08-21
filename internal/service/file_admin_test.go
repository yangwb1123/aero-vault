package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
)

func TestAdminDeleteFailClosedWithoutLegacyOptOut(t *testing.T) {
	withProvider, repo := newTestSvc(t)
	obj := putTestObject(t, withProvider, "admin-gate.txt", "body")
	bare := NewFileService(withProvider.store, repo, nil)
	ctx := access.WithPrincipal(context.Background(), access.Principal{
		SubjectID: "operator", TenantID: "*", Kind: access.PrincipalUser, Scopes: []string{"admin"},
	})
	if err := bare.AdminDelete(ctx, obj.TenantID, obj.Bucket, obj.Key, true); !errors.Is(err, ErrForbidden) {
		t.Fatalf("AdminDelete = %v; want ErrForbidden", err)
	}
	if _, err := repo.GetObject(context.Background(), obj.TenantID, obj.Bucket, obj.Key); err != nil {
		t.Fatalf("object after denied AdminDelete: %v", err)
	}
}

func TestAdminDeleteFactCapturesCapabilitiesAndChunks(t *testing.T) {
	svc, repo := newTestSvc(t)
	ctx := access.WithPrincipal(context.Background(), access.Principal{
		SubjectID: "admin-1", TenantID: "*", Kind: access.PrincipalUser, Scopes: []string{"admin"},
	})
	obj, err := svc.Put(ctx, "tenant-b", "archive", "docs/a.txt", strings.NewReader("body"), 4, PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	store, ok := repo.(access.Store)
	if !ok {
		t.Fatal("repository does not implement access.Store")
	}
	if err := store.CreateShare(ctx, access.Share{
		ID: "share-1", TenantID: obj.TenantID, Bucket: obj.Bucket, Key: obj.Key,
		TokenHash: "hash-1", CreatedAt: time.Now().UTC(), CreatedBy: "owner",
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.InsertChunks(ctx, []repository.Chunk{
		{ObjectID: obj.ID, TenantID: obj.TenantID, Bucket: obj.Bucket, ObjectKey: obj.Key, Seq: 0, Content: "a"},
		{ObjectID: obj.ID, TenantID: obj.TenantID, Bucket: obj.Bucket, ObjectKey: obj.Key, Seq: 1, Content: "b"},
	}); err != nil {
		t.Fatal(err)
	}
	svc.WithShareLister(store).WithChunkCleaner(&mockChunkCleaner{fn: repo.DeleteChunksForObject})
	if err := svc.AdminDelete(ctx, obj.TenantID, obj.Bucket, obj.Key, true); err != nil {
		t.Fatalf("AdminDelete: %v", err)
	}
	rows, err := repo.ClaimEventOutbox(context.Background(), "admin-test", "token", 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, row := range rows {
		if row.EventType != repository.EventTypeFileDeleted11 {
			continue
		}
		var fact struct {
			Actor        string   `json:"actor"`
			Tenant       string   `json:"tenant"`
			ShareIDs     []string `json:"share_ids"`
			VersionCount int      `json:"version_count"`
			ChunkCount   int      `json:"chunk_count"`
		}
		if err := json.Unmarshal(row.Payload, &fact); err != nil {
			t.Fatal(err)
		}
		if fact.Actor != "admin-1" || fact.Tenant != "tenant-b" ||
			len(fact.ShareIDs) != 1 || fact.ShareIDs[0] != "share-1" ||
			fact.VersionCount != 1 || fact.ChunkCount != 2 {
			t.Fatalf("delete fact = %+v", fact)
		}
		found = true
	}
	if !found {
		t.Fatalf("claimed rows missing deleted fact: %+v", rows)
	}
	if shares, err := store.ListShares(context.Background(), obj.TenantID, obj.Bucket, obj.Key); err != nil || len(shares) != 0 {
		t.Fatalf("shares after delete = %+v err=%v; want empty", shares, err)
	}
	if chunks, err := repo.ListChunksForObject(context.Background(), obj.ID); err != nil || len(chunks) != 0 {
		t.Fatalf("chunks after delete = %d err=%v; want empty", len(chunks), err)
	}
}
