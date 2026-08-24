package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"
)

func TestLocalGenerationBoundOpenRejectsReplacement(t *testing.T) {
	store, err := NewLocal(LocalConfig{Root: filepath.Join(t.TempDir(), "objects")})
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	ctx := context.Background()
	key := "tenant/bucket/photo.png"
	first, err := store.Put(ctx, key, bytes.NewReader([]byte("first")), 5, PutOptions{})
	if err != nil {
		t.Fatalf("first Put: %v", err)
	}
	if first.Metadata[GenerationMetadataKey] == "" {
		t.Fatal("first Put did not return a generation marker")
	}
	second, err := store.Put(ctx, key, bytes.NewReader([]byte("second")), 6, PutOptions{})
	if err != nil {
		t.Fatalf("replacement Put: %v", err)
	}
	if second.Metadata[GenerationMetadataKey] == first.Metadata[GenerationMetadataKey] {
		t.Fatal("replacement reused the storage generation")
	}

	_, got, err := store.GetGenerationBound(ctx, key, first)
	if !errors.Is(err, ErrGenerationMismatch) {
		t.Fatalf("stale expected generation error=%v, got=%+v; want ErrGenerationMismatch", err, got)
	}

	// A caller cannot weaken the proof by changing only the expected ETag and
	// size: the sidecar generation remains an independent required field.
	staleGeneration := second
	staleGeneration.Metadata = map[string]string{
		GenerationMetadataKey: first.Metadata[GenerationMetadataKey],
	}
	_, _, err = store.GetGenerationBound(ctx, key, staleGeneration)
	if !errors.Is(err, ErrGenerationMismatch) {
		t.Fatalf("generation-only mismatch error=%v, want ErrGenerationMismatch", err)
	}
}

func TestLocalGenerationBoundOpenReturnsTheProvenDescriptor(t *testing.T) {
	store, err := NewLocal(LocalConfig{Root: filepath.Join(t.TempDir(), "objects")})
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	ctx := context.Background()
	want := []byte("stable bytes")
	info, err := store.Put(ctx, "tenant/bucket/object", bytes.NewReader(want), int64(len(want)), PutOptions{})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	rc, opened, err := store.GetGenerationBound(ctx, info.Key, info)
	if err != nil {
		t.Fatalf("GetGenerationBound: %v", err)
	}
	got, readErr := io.ReadAll(rc)
	closeErr := rc.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read/close: read=%v close=%v", readErr, closeErr)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("opened bytes=%q, want %q", got, want)
	}
	if opened.Key != info.Key || opened.ETag != info.ETag ||
		opened.Size != info.Size ||
		opened.Metadata[GenerationMetadataKey] != info.Metadata[GenerationMetadataKey] {
		t.Fatalf("opened descriptor=%+v, want %+v", opened, info)
	}
}
