package storage

import (
	"context"
	"strings"
	"testing"
)

func TestFactoryNewFromConfig_Local(t *testing.T) {
	s, err := NewFromConfig(context.Background(), FactoryConfig{
		Kind:  BackendLocal,
		Local: LocalConfig{Root: t.TempDir()},
	})
	if err != nil {
		t.Fatalf("NewFromConfig local: %v", err)
	}
	if s.Backend() != "local" {
		t.Errorf("Backend() = %q; want local", s.Backend())
	}
}

func TestFactoryNewFromConfig_S3_NoBucket(t *testing.T) {
	_, err := NewFromConfig(context.Background(), FactoryConfig{Kind: BackendS3})
	if err == nil {
		t.Fatal("NewFromConfig S3 with empty config should error")
	}
	if !strings.Contains(err.Error(), "bucket is required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestFactoryNewFromConfig_OSS_Empty(t *testing.T) {
	_, err := NewFromConfig(context.Background(), FactoryConfig{Kind: BackendOSS})
	if err == nil {
		t.Fatal("NewFromConfig OSS with empty config should error")
	}
	if !strings.Contains(err.Error(), "endpoint and bucket are required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestFactoryNewFromConfig_COS_Empty(t *testing.T) {
	_, err := NewFromConfig(context.Background(), FactoryConfig{Kind: BackendCOS})
	if err == nil {
		t.Fatal("NewFromConfig COS with empty config should error")
	}
	if !strings.Contains(err.Error(), "bucket URL is required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestFactoryNewFromConfig_Unknown(t *testing.T) {
	_, err := NewFromConfig(context.Background(), FactoryConfig{Kind: "nonexistent"})
	if err == nil {
		t.Fatal("NewFromConfig with unknown kind should error")
	}
	if !strings.Contains(err.Error(), "unknown storage backend") {
		t.Errorf("unexpected error: %v", err)
	}
}
