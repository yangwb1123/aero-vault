package storage

import (
	"context"
	"testing"
)

func TestNewOSS(t *testing.T) {
	s, err := NewOSS(OSSConfig{
		Endpoint:  "https://oss-cn-hangzhou.aliyuncs.com",
		Bucket:    "my-bucket",
		AccessKey: "ak",
		SecretKey: "sk",
	})
	if err != nil {
		t.Fatalf("NewOSS: %v", err)
	}
	if s.Backend() != "oss" {
		t.Fatalf("backend=%q want oss", s.Backend())
	}
	if _, err := NewOSS(OSSConfig{}); err == nil {
		t.Fatal("expected error for missing endpoint/bucket")
	}
}

func TestNewCOS(t *testing.T) {
	s, err := NewCOS(COSConfig{
		BucketURL: "https://my-bucket-1250000000.cos.ap-guangzhou.myqcloud.com",
		SecretID:  "id",
		SecretKey: "key",
	})
	if err != nil {
		t.Fatalf("NewCOS: %v", err)
	}
	if s.Backend() != "cos" {
		t.Fatalf("backend=%q want cos", s.Backend())
	}
	if _, err := NewCOS(COSConfig{}); err == nil {
		t.Fatal("expected error for missing bucket URL")
	}
}

func TestFactoryOSSCOS(t *testing.T) {
	ctx := context.Background()
	oss, err := NewFromConfig(ctx, FactoryConfig{
		Kind: BackendOSS,
		OSS:  OSSConfig{Endpoint: "https://oss-cn-hangzhou.aliyuncs.com", Bucket: "my-bucket"},
	})
	if err != nil || oss.Backend() != "oss" {
		t.Fatalf("factory oss: backend=%v err=%v", backendOf(oss), err)
	}
	cos, err := NewFromConfig(ctx, FactoryConfig{
		Kind: BackendCOS,
		COS:  COSConfig{BucketURL: "https://b-1250000000.cos.ap-guangzhou.myqcloud.com"},
	})
	if err != nil || cos.Backend() != "cos" {
		t.Fatalf("factory cos: backend=%v err=%v", backendOf(cos), err)
	}
	if _, err := NewFromConfig(ctx, FactoryConfig{Kind: "bogus"}); err == nil {
		t.Fatal("expected error for unknown backend")
	}
}

func backendOf(s Storage) string {
	if s == nil {
		return "<nil>"
	}
	return s.Backend()
}

// Compile-time assertions that the native backends satisfy Storage.
var (
	_ Storage = (*OSSStorage)(nil)
	_ Storage = (*COSStorage)(nil)
)
