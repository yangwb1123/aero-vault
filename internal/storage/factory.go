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
// matching Kind need to be populated. Timeouts apply to cloud backends (S3/OSS/COS).
type FactoryConfig struct {
	Kind           BackendKind
	Timeouts       TimeoutConfig
	CircuitBreaker CBConfig // optional circuit breaker; Enabled=false by default
	Local          LocalConfig
	S3             S3Config
	OSS            OSSConfig
	COS            COSConfig
}

// NewDefaultFactoryConfig returns a FactoryConfig with default timeouts and
// empty backend configs. Callers override Kind and backend-specific fields.
func NewDefaultFactoryConfig() FactoryConfig {
	return FactoryConfig{
		Timeouts: DefaultTimeoutConfig(),
	}
}

// NewFromConfig builds a Storage according to cfg.Kind. Timeouts from cfg are
// propagated to the backend's config before construction. When the circuit
// breaker is enabled, the returned Storage is wrapped in one.
func NewFromConfig(ctx context.Context, cfg FactoryConfig) (Storage, error) {
	var store Storage
	var err error
	switch cfg.Kind {
	case BackendLocal:
		store, err = NewLocal(cfg.Local)
	case BackendS3:
		bc := cfg.S3
		bc.Timeouts = cfg.Timeouts
		store, err = NewS3(ctx, bc)
	case BackendOSS:
		bc := cfg.OSS
		bc.Timeouts = cfg.Timeouts
		store, err = NewOSS(bc)
	case BackendCOS:
		bc := cfg.COS
		bc.Timeouts = cfg.Timeouts
		store, err = NewCOS(bc)
	default:
		return nil, fmt.Errorf("unknown storage backend %q", cfg.Kind)
	}
	if err != nil {
		return nil, err
	}
	if cfg.CircuitBreaker.Enabled {
		store = NewCircuitBreaker(store, cfg.CircuitBreaker)
	}
	return store, nil
}
