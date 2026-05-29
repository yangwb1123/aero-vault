package storage

import (
	"context"
	"fmt"
)

// BackendKind enumerates the supported storage backends.
type BackendKind string

const (
	BackendLocal BackendKind = "local"
	BackendS3    BackendKind = "s3"
	BackendOSS   BackendKind = "oss"
	BackendCOS   BackendKind = "cos"
)

// FactoryConfig is the union of options accepted by NewFromConfig. Only the fields
// matching Kind need to be populated.
type FactoryConfig struct {
	Kind  BackendKind
	Local LocalConfig
	S3    S3Config
	OSS   OSSConfig
	COS   COSConfig
}

// NewFromConfig builds a Storage according to cfg.Kind.
func NewFromConfig(ctx context.Context, cfg FactoryConfig) (Storage, error) {
	switch cfg.Kind {
	case BackendLocal:
		return NewLocal(cfg.Local)
	case BackendS3:
		return NewS3(ctx, cfg.S3)
	case BackendOSS:
		return NewOSS(cfg.OSS)
	case BackendCOS:
		return NewCOS(cfg.COS)
	default:
		return nil, fmt.Errorf("unknown storage backend %q", cfg.Kind)
	}
}
