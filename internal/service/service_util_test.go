package service

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func TestStorageClassOrDefault(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", "STANDARD"},
		{"STANDARD", "STANDARD"},
		{"GLACIER", "GLACIER"},
	}
	for _, tt := range tests {
		got := StorageClassOrDefault(tt.input)
		if got != tt.expected {
			t.Errorf("StorageClassOrDefault(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestWithDefaultStorageClass(t *testing.T) {
	original := DefaultStorageClass
	defer func() { DefaultStorageClass = original }()

	WithDefaultStorageClass("GLACIER")
	if DefaultStorageClass != "GLACIER" {
		t.Errorf("DefaultStorageClass = %q, want %q", DefaultStorageClass, "GLACIER")
	}

	WithDefaultStorageClass("")
	if DefaultStorageClass != "GLACIER" {
		t.Errorf("empty string should not change default")
	}
}

func TestWithReadVerification(t *testing.T) {
	svc, _ := newTestSvc(t)

	if svc.readVerify.Enabled {
		t.Fatal("expected disabled by default")
	}

	svc.WithReadVerification(ReadVerificationConfig{
		Enabled: true,
		MaxSize: 1024,
		Sample:  true,
	})

	if !svc.readVerify.Enabled {
		t.Fatal("expected enabled after WithReadVerification")
	}
	if svc.readVerify.MaxSize != 1024 {
		t.Errorf("MaxSize = %d, want 1024", svc.readVerify.MaxSize)
	}
}

func TestChunkCleaner(t *testing.T) {
	svc, _ := newTestSvc(t)

	if cleaner := svc.ChunkCleaner(); cleaner != nil {
		t.Fatal("expected nil chunk cleaner by default")
	}

	cleaned := false
	mock := &mockChunkCleaner{fn: func(ctx context.Context, id int64) error {
		cleaned = true
		return nil
	}}
	svc.WithChunkCleaner(mock)
	if svc.chunkCleaner == nil {
		t.Fatal("chunk cleaner should be set")
	}

	// Verify it's returned by the getter.
	if svc.ChunkCleaner() == nil {
		t.Fatal("ChunkCleaner() should not be nil")
	}

	// Clear it.
	svc.WithChunkCleaner(nil)
	if svc.chunkCleaner != nil {
		t.Fatal("chunk cleaner should be nil after clearing")
	}
	_ = cleaned
}

func TestPublishNoopSink(t *testing.T) {
	// Verify that the noop sink doesn't panic.
	svc, _ := newTestSvc(t)
	ctx := context.Background()

	obj, err := svc.Put(ctx, "", "", "test.txt", strings.NewReader("hello"), 5, PutOptions{})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// The noop sink should swallow events silently.
	svc.sink.Publish(ctx, repository.Event{
		Type:     repository.EventCreated,
		TenantID: obj.TenantID,
		Bucket:   obj.Bucket,
		Key:      obj.Key,
	})

	// Verify the file is still accessible.
	rc, _, err := svc.Get(ctx, "", "", "test.txt")
	if err != nil {
		t.Fatalf("Get after publish: %v", err)
	}
	io.Copy(io.Discard, rc)
	rc.Close()
}

func TestEmitDoesNotBlock(t *testing.T) {
	svc, _ := newTestSvc(t)
	ctx := context.Background()

	svc.emit(ctx, repository.Object{
		TenantID: "t", Bucket: "b", Key: "k",
	}, repository.EventCreated)
}
