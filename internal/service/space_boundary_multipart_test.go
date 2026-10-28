package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
)

func boundaryMultipart(t *testing.T, svc *FileService, tenant, bucket, key string, opts PutOptions, read ReadOptions) (repository.Upload, repository.Object) {
	t.Helper()
	u, err := svc.InitMultipart(t.Context(), tenant, bucket, key, opts)
	if err != nil {
		t.Fatal(err)
	}
	scope := MultipartScope{TenantID: tenant, Bucket: bucket, Key: key}
	if _, err := svc.UploadPartFor(t.Context(), scope, u.ID, 1, strings.NewReader("content"), 7, read); err != nil {
		t.Fatal(err)
	}
	obj, err := svc.CompleteMultipartFor(t.Context(), scope, u.ID, read)
	if err != nil {
		t.Fatal(err)
	}
	return u, obj
}

func TestSpaceBoundaryMultipartReplayAfterUploadRemoval(t *testing.T) {
	f := newSpaceFixture(t)
	u, first := boundaryMultipart(t, f.svc, "alpha", "docs", "file", PutOptions{}, ReadOptions{})
	if _, err := f.raw.GetUpload(t.Context(), u.ID); !errors.Is(err, repository.ErrUploadNotFound) {
		t.Fatalf("upload not removed: %v", err)
	}
	before := *f.effects
	provider := &spaceActionProvider{action: access.ActionRead}
	f.svc.WithAuthorizer(provider)
	scope := MultipartScope{TenantID: "alpha", Bucket: "docs", Key: "file"}
	if _, err := f.svc.CompleteMultipartFor(t.Context(), scope, u.ID, ReadOptions{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("replay skipped current action: %v", err)
	}
	provider.action = access.ActionWrite
	cached, err := f.svc.CompleteMultipartFor(t.Context(), scope, u.ID, ReadOptions{})
	if err != nil || cached.ID != first.ID || cached.VersionID != first.VersionID {
		t.Fatalf("legal replay: %+v %v", cached, err)
	}
	if *f.effects != before {
		t.Fatalf("replay duplicated effects: %+v -> %+v", before, *f.effects)
	}
}

func TestSpaceBoundaryMultipartBoundCompleteAndAbortReplay(t *testing.T) {
	f := newSpaceFixture(t)
	space := createBoundarySpace(t, f.raw, "alpha")
	legacy := NewFileService(f.store, f.repo, nil).WithEventSink(spaceSink{f.effects})
	complete, _ := boundaryMultipart(t, legacy, "alpha", space.BackingBucket, "complete", PutOptions{}, ReadOptions{})
	abort, err := legacy.InitMultipart(t.Context(), "alpha", space.BackingBucket, "abort", PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	scope := MultipartScope{TenantID: "alpha", Bucket: space.BackingBucket}
	if err := legacy.AbortMultipartFor(t.Context(), scope, abort.ID); err != nil {
		t.Fatal(err)
	}
	before := *f.effects
	if _, err := f.svc.CompleteMultipartFor(access.SystemContext(t.Context(), "alpha"), scope, complete.ID, ReadOptions{}); !errors.Is(err, access.ErrSpaceBound) {
		t.Fatalf("bound completion replay: %v", err)
	}
	if err := f.svc.AbortMultipartFor(access.SystemContext(t.Context(), "alpha"), scope, abort.ID); !errors.Is(err, access.ErrSpaceBound) {
		t.Fatalf("bound abort replay: %v", err)
	}
	if *f.effects != before {
		t.Fatal("refused cached replay performed effects")
	}
}

func TestSpaceBoundaryMultipartAbortCurrentPermissionAndIdentity(t *testing.T) {
	f := newSpaceFixture(t)
	u, err := f.svc.InitMultipart(t.Context(), "alpha", "docs", "abort", PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	scope := MultipartScope{TenantID: "alpha", Bucket: "docs", Key: "abort"}
	if err := f.svc.AbortMultipartFor(t.Context(), scope, u.ID); err != nil {
		t.Fatal(err)
	}
	before := *f.effects
	provider := &spaceActionProvider{action: access.ActionRead}
	f.svc.WithAuthorizer(provider)
	if err := f.svc.AbortMultipartFor(t.Context(), scope, u.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("abort replay skipped action: %v", err)
	}
	provider.action = access.ActionWrite
	if err := f.svc.AbortMultipartFor(t.Context(), scope, u.ID); err != nil {
		t.Fatalf("unbound abort replay: %v", err)
	}
	corrupt := u
	corrupt.ID = "another-upload"
	body, _ := json.Marshal(corrupt)
	if err := f.raw.CompleteIdempotencyKey(t.Context(), "alpha", multipartAbortKey(u.ID), http.StatusNoContent, body, "application/json", nil); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.AbortMultipartFor(t.Context(), scope, u.ID); !access.IsResourceBoundaryFault(err) {
		t.Fatalf("wrong persisted upload identity: %v", err)
	}
	if *f.effects != before {
		t.Fatal("abort replay duplicated effects")
	}
}

func TestSpaceBoundaryMultipartMalformedPersistedCompletion(t *testing.T) {
	f := newSpaceFixture(t)
	u, first := boundaryMultipart(t, f.svc, "alpha", "docs", "file", PutOptions{}, ReadOptions{})
	before := *f.effects
	for _, mutate := range []func(*repository.Object){
		func(o *repository.Object) { o.ID = 0 }, func(o *repository.Object) { o.TenantID = "" },
		func(o *repository.Object) { o.Bucket = "" }, func(o *repository.Object) { o.Key = "" },
		func(o *repository.Object) { o.VersionID = "" }, func(o *repository.Object) { o.StorageKey = "" },
		func(o *repository.Object) { o.TenantID = "other" },
	} {
		corrupt := first
		mutate(&corrupt)
		body, _ := json.Marshal(corrupt)
		if err := f.raw.CompleteIdempotencyKey(t.Context(), "alpha", multipartCompleteKey(u.ID), http.StatusOK, body, "application/json", nil); err != nil {
			t.Fatal(err)
		}
		_, err := f.svc.CompleteMultipartFor(t.Context(), MultipartScope{TenantID: "alpha", Bucket: "docs", Key: "file"}, u.ID, ReadOptions{})
		if !access.IsResourceBoundaryFault(err) {
			t.Errorf("malformed persisted object accepted: %+v %v", corrupt, err)
		}
	}
	if *f.effects != before {
		t.Fatal("malformed replay caused effects")
	}
}

func TestSpaceBoundaryMultipartReplayStillRequiresOriginalSSEC(t *testing.T) {
	f := newSpaceFixture(t)
	// Use the actual local SSE-C capability, not the counting wrapper which
	// intentionally exposes only Storage. Repository/effects remain observed.
	svc := NewFileService(f.store.Storage, f.repo, nil).WithEventSink(spaceSink{f.effects}).
		WithSpaceBoundary(NewSpaceBoundary(DiscoverSpaceBoundaryReaders(f.raw)))
	key := bytes.Repeat([]byte{7}, 32)
	read := ReadOptions{SSECustomerKey: key}
	u, first := boundaryMultipart(t, svc, "alpha", "docs", "encrypted", PutOptions{SSECustomerKey: key}, read)
	before := *f.effects
	scope := MultipartScope{TenantID: "alpha", Bucket: "docs", Key: "encrypted"}
	if _, err := svc.CompleteMultipartFor(t.Context(), scope, u.ID, ReadOptions{}); !errors.Is(err, ErrInvalidArgs) {
		t.Fatalf("missing SSE-C key: %v", err)
	}
	if _, err := svc.CompleteMultipartFor(t.Context(), scope, u.ID, ReadOptions{SSECustomerKey: bytes.Repeat([]byte{8}, 32)}); !errors.Is(err, ErrBadDigest) {
		t.Fatalf("wrong SSE-C key: %v", err)
	}
	cached, err := svc.CompleteMultipartFor(t.Context(), scope, u.ID, read)
	if err != nil || cached.ID != first.ID {
		t.Fatalf("original SSE-C replay: %+v %v", cached, err)
	}
	assertReadableReplayEncryption(t, svc, first, read)
	if *f.effects != before {
		t.Fatal("SSE-C replay duplicated quota/object/event effects")
	}
}

func assertReadableReplayEncryption(t *testing.T, svc *FileService, obj repository.Object, read ReadOptions) {
	t.Helper()
	admission := ReplayAdmission{Action: access.ActionDownload, Resource: replayObjectResource(obj), Object: &obj}
	if err := svc.AuthorizeReplayAdmission(t.Context(), admission, ReadOptions{}); !errors.Is(err, ErrInvalidArgs) {
		t.Fatalf("read replay skipped SSE-C: %v", err)
	}
	if err := svc.AuthorizeReplayAdmission(t.Context(), admission, read); err != nil {
		t.Fatalf("read replay with original SSE-C: %v", err)
	}
}

func TestSpaceBoundaryMultipartTypedNilAndEmptyUploadIdentity(t *testing.T) {
	f := newSpaceFixture(t)
	u, obj := boundaryMultipart(t, f.svc, "alpha", "docs", "file", PutOptions{}, ReadOptions{})
	abort, err := f.svc.InitMultipart(t.Context(), "alpha", "docs", "abort", PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	scope := MultipartScope{TenantID: "alpha", Bucket: "docs"}
	if err := f.svc.AbortMultipartFor(t.Context(), scope, abort.ID); err != nil {
		t.Fatal(err)
	}
	before := *f.effects
	var typedNil *access.ResourceBoundary
	f.svc.WithSpaceBoundary(typedNil)
	if _, err := f.svc.CompleteMultipartFor(t.Context(), scope, u.ID, ReadOptions{}); !access.IsResourceBoundaryFault(err) {
		t.Fatalf("typed-nil completion replay: %v", err)
	}
	if err := f.svc.AbortMultipartFor(t.Context(), scope, abort.ID); !access.IsResourceBoundaryFault(err) {
		t.Fatalf("typed-nil abort replay: %v", err)
	}
	body, _ := json.Marshal(obj)
	record := repository.IdempotencyRecord{TenantID: "alpha", Key: multipartCompleteKey(""), Status: repository.IdempotencyCompleted, ResponseStatus: http.StatusOK, ResponseBody: body}
	if _, err := replayMultipartCompletion(record, scope, ""); !access.IsResourceBoundaryFault(err) {
		t.Fatalf("empty upload identity accepted: %v", err)
	}
	if *f.effects != before {
		t.Fatal("typed-nil/empty identity replay caused effects")
	}
}
