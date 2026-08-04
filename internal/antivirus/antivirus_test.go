package antivirus

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

func TestSignatureScannerEICAR(t *testing.T) {
	s := NewSignatureScanner(nil)
	if res, _ := s.Scan(context.Background(), strings.NewReader("perfectly safe content")); !res.Clean {
		t.Fatalf("clean content flagged: %+v", res)
	}
	res, err := s.Scan(context.Background(), strings.NewReader("prefix "+EICAR+" suffix"))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if res.Clean || res.Signature != "EICAR-Test-File" {
		t.Fatalf("EICAR not detected: %+v", res)
	}
}

func TestSignatureScannerExtra(t *testing.T) {
	s := NewSignatureScanner(map[string]string{"Custom-Mal": "BADBADBAD"})
	res, _ := s.Scan(context.Background(), strings.NewReader("xx BADBADBAD xx"))
	if res.Clean || res.Signature != "Custom-Mal" {
		t.Fatalf("custom signature not detected: %+v", res)
	}
}

func TestHTTPScanner(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "virus") {
			_, _ = w.Write([]byte(`{"clean":false,"signature":"Trojan.Test"}`))
			return
		}
		_, _ = w.Write([]byte(`{"clean":true}`))
	}))
	defer srv.Close()
	sc := NewHTTPScanner(srv.URL, "")
	if res, _ := sc.Scan(context.Background(), strings.NewReader("ok")); !res.Clean {
		t.Fatalf("expected clean")
	}
	res, _ := sc.Scan(context.Background(), strings.NewReader("a virus here"))
	if res.Clean || res.Signature != "Trojan.Test" {
		t.Fatalf("expected infected, got %+v", res)
	}
}

func setupSvc(t *testing.T) (repository.Repository, *service.FileService) {
	t.Helper()
	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "av.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, _ := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	t.Cleanup(func() { _ = repo.Close() })
	return repo, service.NewFileService(store, repo, nil)
}

func TestWorkerScanCleanTagsObject(t *testing.T) {
	ctx := context.Background()
	repo, svc := setupSvc(t)
	obj, err := svc.Put(ctx, "default", "default", "clean.txt", strings.NewReader("totally fine"), 12, service.PutOptions{})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	w := NewWorker(repo, svc.Storage(), NewSignatureScanner(nil), nil, true, slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithObjectController(svc)
	if err := w.ScanObjectByID(ctx, obj.ID); err != nil {
		t.Fatalf("scan: %v", err)
	}
	got, err := repo.GetObject(ctx, "default", "default", "clean.txt")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Tags[TagStatus] != "clean" {
		t.Fatalf("expected av_status=clean, got %v", got.Tags)
	}
}

func TestWorkerQuarantinesInfected(t *testing.T) {
	ctx := context.Background()
	repo, svc := setupSvc(t)
	obj, err := svc.Put(ctx, "default", "default", "bad.txt", strings.NewReader(EICAR), int64(len(EICAR)), service.PutOptions{})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	w := NewWorker(repo, svc.Storage(), NewSignatureScanner(nil), nil, true, slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithObjectController(svc)
	if err := w.ScanObjectByID(ctx, obj.ID); err != nil {
		t.Fatalf("scan: %v", err)
	}
	// Quarantine soft-deletes the object.
	if _, err := repo.GetObject(ctx, "default", "default", "bad.txt"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("infected object should be quarantined, got err=%v", err)
	}
	quota, err := repo.GetTenantQuota(ctx, "default")
	if err != nil {
		t.Fatalf("quota: %v", err)
	}
	if quota.UsedBytes != 0 || quota.UsedObjects != 0 {
		t.Fatalf("quarantine left usage at %d bytes/%d objects", quota.UsedBytes, quota.UsedObjects)
	}
}

func TestWorkerNoQuarantineKeepsButTags(t *testing.T) {
	ctx := context.Background()
	repo, svc := setupSvc(t)
	obj, _ := svc.Put(ctx, "default", "default", "bad2.txt", strings.NewReader(EICAR), int64(len(EICAR)), service.PutOptions{})
	w := NewWorker(repo, svc.Storage(), NewSignatureScanner(nil), nil, false, slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithObjectController(svc)
	if err := w.ScanObjectByID(ctx, obj.ID); err != nil {
		t.Fatalf("scan: %v", err)
	}
	got, err := repo.GetObject(ctx, "default", "default", "bad2.txt")
	if err != nil {
		t.Fatalf("object should remain (no quarantine): %v", err)
	}
	if got.Tags[TagStatus] != "infected" || got.Tags[TagSignature] != "EICAR-Test-File" {
		t.Fatalf("expected infected tags, got %v", got.Tags)
	}
}

func TestWorkerQuarantinesOnlyInfectedVersion(t *testing.T) {
	ctx := context.Background()
	repo, svc := setupSvc(t)
	if err := svc.SetBucketVersioning(ctx, "default", "default", true); err != nil {
		t.Fatalf("enable versioning: %v", err)
	}
	infected, err := svc.Put(ctx, "", "", "versioned.txt", strings.NewReader(EICAR), int64(len(EICAR)), service.PutOptions{})
	if err != nil {
		t.Fatalf("put infected version: %v", err)
	}
	cleanBody := "replacement"
	current, err := svc.Put(ctx, "", "", "versioned.txt", strings.NewReader(cleanBody), int64(len(cleanBody)), service.PutOptions{})
	if err != nil {
		t.Fatalf("put clean version: %v", err)
	}
	w := NewWorker(repo, svc.Storage(), NewSignatureScanner(nil), nil, true, slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithObjectController(svc)
	if err := w.ScanObjectByID(ctx, infected.ID); err != nil {
		t.Fatalf("scan old version: %v", err)
	}
	if _, err := repo.GetObjectByID(ctx, infected.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("infected historical version still exists: %v", err)
	}
	got, err := repo.GetObjectByID(ctx, current.ID)
	if err != nil {
		t.Fatalf("current version removed: %v", err)
	}
	if got.Tags[TagStatus] != "" {
		t.Fatalf("current version received stale antivirus tag: %v", got.Tags)
	}
	quota, err := repo.GetTenantQuota(ctx, "default")
	if err != nil {
		t.Fatalf("quota: %v", err)
	}
	if quota.UsedBytes != int64(len(cleanBody)) || quota.UsedObjects != 1 {
		t.Fatalf("usage = %d bytes/%d objects", quota.UsedBytes, quota.UsedObjects)
	}
}

func TestSignatureScannerName(t *testing.T) {
	s := NewSignatureScanner(nil)
	if s.Name() != "signature" {
		t.Fatalf("expected 'signature', got %q", s.Name())
	}
}

func TestEncodeDecodeObjectID(t *testing.T) {
	payload := EncodeObjectID(42)
	if payload == "" {
		t.Fatal("expected non-empty payload")
	}
	id, err := DecodeObjectID(payload)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if id != 42 {
		t.Fatalf("expected 42, got %d", id)
	}
}

func TestDecodeObjectID_Invalid(t *testing.T) {
	if _, err := DecodeObjectID(`{"object_id":0}`); err == nil {
		t.Fatal("expected error for missing object_id")
	}
	if _, err := DecodeObjectID(`not json`); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}
