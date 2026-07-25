package storage

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

var (
	// ErrBackendUnavailable is returned when the circuit breaker is open and
	// the storage backend is presumed unavailable.
	ErrBackendUnavailable = errors.New("storage backend unavailable (circuit breaker open)")
)

// CBState is the circuit-breaker lifecycle state.
type CBState int

const (
	CBClosed   CBState = iota // Normal operation — requests pass through
	CBOpen                    // Fail-fast — all requests rejected
	CBHalfOpen                // Probing — limited requests allowed
)

func (s CBState) String() string {
	switch s {
	case CBClosed:
		return "closed"
	case CBOpen:
		return "open"
	case CBHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// CBConfig controls the circuit breaker behaviour.
type CBConfig struct {
	// FailureThreshold is the number of consecutive failures (or error ratio
	// sample reached) before the circuit opens. Default 5.
	FailureThreshold int
	// RecoveryTimeout is how long the circuit stays open before transitioning
	// to half-open. Default 30 seconds.
	RecoveryTimeout time.Duration
	// HalfOpenMaxRequests is the number of requests allowed in half-open
	// state before deciding to close or re-open. Default 1.
	HalfOpenMaxRequests int
	// Enabled controls whether the breaker is active. Off by default
	// (all requests pass through with zero overhead).
	Enabled bool
}

// DefaultCBConfig returns safe defaults for a circuit breaker.
func DefaultCBConfig() CBConfig {
	return CBConfig{
		FailureThreshold:    5,
		RecoveryTimeout:     30 * time.Second,
		HalfOpenMaxRequests: 1,
		Enabled:             false,
	}
}

// countBucket holds a sliding window of request outcomes.
type countBucket struct {
	failures int
	total    int
}

// circuitBreaker wraps a Storage backend with circuit-breaking logic.
// It is safe for concurrent use.
type circuitBreaker struct {
	Storage // underlying backend

	cfg CBConfig

	mu           sync.Mutex
	state        CBState
	lastFailure  time.Time // when we transitioned to open
	failures     int       // consecutive failures in current window
	halfAllowed  int       // remaining probes in half-open
	stateChanged time.Time

	// Sliding-window counters per second (for finer-grained decisions).
	// Keyed by unix-second timestamp.
	window map[int64]*countBucket
}

// NewCircuitBreaker wraps a Storage with a circuit breaker. When cfg.Enabled
// is false (the default) the wrapper is a transparent pass-through.
func NewCircuitBreaker(inner Storage, cfg CBConfig) Storage {
	if !cfg.Enabled {
		return inner
	}
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = DefaultCBConfig().FailureThreshold
	}
	if cfg.RecoveryTimeout <= 0 {
		cfg.RecoveryTimeout = DefaultCBConfig().RecoveryTimeout
	}
	if cfg.HalfOpenMaxRequests <= 0 {
		cfg.HalfOpenMaxRequests = DefaultCBConfig().HalfOpenMaxRequests
	}
	return &circuitBreaker{
		Storage: inner,
		cfg:     cfg,
		state:   CBClosed,
		window:  make(map[int64]*countBucket),
	}
}

// State returns the current circuit-breaker state (for observability).
func (cb *circuitBreaker) State() CBState {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.tryTransition()
	return cb.state
}

// Stats returns current breaker counters (for Prometheus metrics).
func (cb *circuitBreaker) Stats() (state CBState, failures, total int) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	state = cb.state
	now := time.Now().Unix()
	for ts, b := range cb.window {
		if now-ts > 60 { // keep last 60s
			delete(cb.window, ts)
			continue
		}
		failures += b.failures
		total += b.total
	}
	return
}

// tryTransition checks if the circuit should transition between states.
// Caller must hold cb.mu.
func (cb *circuitBreaker) tryTransition() {
	switch cb.state {
	case CBOpen:
		if time.Since(cb.lastFailure) >= cb.cfg.RecoveryTimeout {
			cb.state = CBHalfOpen
			cb.halfAllowed = cb.cfg.HalfOpenMaxRequests
			cb.stateChanged = time.Now()
		}
	case CBHalfOpen:
		// If no probes have been consumed for the recovery window, go back to
		// open (the probe might have been lost).
		if cb.halfAllowed == cb.cfg.HalfOpenMaxRequests &&
			time.Since(cb.stateChanged) > cb.cfg.RecoveryTimeout {
			cb.state = CBOpen
			cb.lastFailure = time.Now()
		}
	}
}

// beforeRequest returns nil when the request may proceed, or
// ErrBackendUnavailable when the circuit is open (fail-fast).
func (cb *circuitBreaker) beforeRequest() error {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.tryTransition()
	switch cb.state {
	case CBOpen:
		return ErrBackendUnavailable
	case CBHalfOpen:
		if cb.halfAllowed <= 0 {
			return ErrBackendUnavailable
		}
		cb.halfAllowed--
		return nil
	default:
		return nil
	}
}

// recordOutcome records a success or failure after a request completes.
func (cb *circuitBreaker) recordOutcome(err error) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	now := time.Now()
	sec := now.Unix()
	b, ok := cb.window[sec]
	if !ok {
		b = &countBucket{}
		cb.window[sec] = b
	}
	b.total++

	if err == nil {
		// Success
		switch cb.state {
		case CBHalfOpen:
			// Success in half-open → close the circuit.
			cb.state = CBClosed
			cb.failures = 0
			cb.stateChanged = now
		case CBClosed:
			cb.failures = 0
		}
		return
	}

	// Failure
	b.failures++
	cb.failures++

	switch cb.state {
	case CBClosed:
		if cb.failures >= cb.cfg.FailureThreshold {
			cb.state = CBOpen
			cb.lastFailure = now
			cb.stateChanged = now
		}
	case CBHalfOpen:
		// Failure in half-open → back to open.
		cb.state = CBOpen
		cb.lastFailure = now
		cb.stateChanged = now
	}
}

// --- Storage interface methods (decorate each with circuit breaker) ---

func (cb *circuitBreaker) Put(ctx context.Context, key string, r io.Reader, size int64, opts PutOptions) (ObjectInfo, error) {
	if err := cb.beforeRequest(); err != nil {
		return ObjectInfo{}, err
	}
	info, err := cb.Storage.Put(ctx, key, r, size, opts)
	cb.recordOutcome(err)
	return info, err
}

func (cb *circuitBreaker) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	if err := cb.beforeRequest(); err != nil {
		return nil, ObjectInfo{}, err
	}
	rc, info, err := cb.Storage.Get(ctx, key)
	cb.recordOutcome(err)
	return rc, info, err
}

func (cb *circuitBreaker) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	if err := cb.beforeRequest(); err != nil {
		return ObjectInfo{}, err
	}
	info, err := cb.Storage.Stat(ctx, key)
	cb.recordOutcome(err)
	return info, err
}

func (cb *circuitBreaker) Delete(ctx context.Context, key string) error {
	if err := cb.beforeRequest(); err != nil {
		return err
	}
	err := cb.Storage.Delete(ctx, key)
	cb.recordOutcome(err)
	return err
}

func (cb *circuitBreaker) List(ctx context.Context, prefix, marker string, limit int) (ListResult, error) {
	if err := cb.beforeRequest(); err != nil {
		return ListResult{}, err
	}
	res, err := cb.Storage.List(ctx, prefix, marker, limit)
	cb.recordOutcome(err)
	return res, err
}

func (cb *circuitBreaker) PresignGet(ctx context.Context, key string, expiry time.Duration) (string, error) {
	if err := cb.beforeRequest(); err != nil {
		return "", err
	}
	u, err := cb.Storage.PresignGet(ctx, key, expiry)
	cb.recordOutcome(err)
	return u, err
}

func (cb *circuitBreaker) PresignPut(ctx context.Context, key string, expiry time.Duration) (string, error) {
	if err := cb.beforeRequest(); err != nil {
		return "", err
	}
	u, err := cb.Storage.PresignPut(ctx, key, expiry)
	cb.recordOutcome(err)
	return u, err
}

func (cb *circuitBreaker) InitMultipart(ctx context.Context, key string, opts PutOptions) (MultipartInit, error) {
	if err := cb.beforeRequest(); err != nil {
		return MultipartInit{}, err
	}
	mi, err := cb.Storage.InitMultipart(ctx, key, opts)
	cb.recordOutcome(err)
	return mi, err
}

func (cb *circuitBreaker) UploadPart(ctx context.Context, key, uploadID string, partNumber int32, r io.Reader, size int64) (MultipartPart, error) {
	if err := cb.beforeRequest(); err != nil {
		return MultipartPart{}, err
	}
	mp, err := cb.Storage.UploadPart(ctx, key, uploadID, partNumber, r, size)
	cb.recordOutcome(err)
	return mp, err
}

func (cb *circuitBreaker) CompleteMultipart(ctx context.Context, key, uploadID string, parts []MultipartPart) (ObjectInfo, error) {
	if err := cb.beforeRequest(); err != nil {
		return ObjectInfo{}, err
	}
	info, err := cb.Storage.CompleteMultipart(ctx, key, uploadID, parts)
	cb.recordOutcome(err)
	return info, err
}

func (cb *circuitBreaker) AbortMultipart(ctx context.Context, key, uploadID string) error {
	if err := cb.beforeRequest(); err != nil {
		return err
	}
	err := cb.Storage.AbortMultipart(ctx, key, uploadID)
	cb.recordOutcome(err)
	return err
}

func (cb *circuitBreaker) CleanupParts(ctx context.Context, key, uploadID string) error {
	if err := cb.beforeRequest(); err != nil {
		return err
	}
	err := cb.Storage.CleanupParts(ctx, key, uploadID)
	cb.recordOutcome(err)
	return err
}

// Backend returns the underlying backend name with a "(cb)" suffix.
func (cb *circuitBreaker) Backend() string {
	return cb.Storage.Backend() + "(cb)"
}

// Ensure compile-time interface satisfaction.
var _ Storage = (*circuitBreaker)(nil)
