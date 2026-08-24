package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// LocalConfig configures the on-disk backend.
type LocalConfig struct {
	Root        string         // root directory holding objects
	PublicURL   string         // optional, used to build presigned URLs (e.g. http://host:8080/files)
	SignKey     string         // HMAC key for presigning; empty disables presigning
	SSEKey      string         // single-key envelope SSE passphrase; empty disables SSE
	SSEKeyfile  string         // versioned key ring path for rotation; takes precedence over SSEKey
	SSEKeyURL   string         // HTTP secret store (Vault KV) serving the key ring; precedence over SSEKeyfile
	SSEKeyToken string         // optional bearer token for SSEKeyURL
	Secrets     SecretProvider // explicit key source; overrides the SSEKey* fields
	// KMS-style remote wrapping (the wrapping key never leaves the KMS); takes
	// precedence over every SecretProvider source above.
	SSEKMSURL   string // HTTP KMS wrap/unwrap endpoint
	SSEKMSKeyID string // KMS key id to wrap new data keys with
	SSEKMSToken string // optional bearer token for SSEKMSURL
	KMS         DataKeyWrapper
}

// LocalStorage stores objects on the local filesystem. Metadata is sidecar JSON.
type LocalStorage struct {
	cfg LocalConfig
	enc *envelopeEncrypter // nil when SSE disabled

	mu           sync.RWMutex
	generationMu sync.RWMutex
	uploads      map[string]*localUpload // uploadID -> parts dir state
}

type localUpload struct {
	key       string
	dir       string
	createdAt time.Time
	opts      PutOptions
}

const localMetaSuffix = ".meta.json"

type localMeta struct {
	Key          string            `json:"key"`
	Size         int64             `json:"size"` // plaintext size
	ETag         string            `json:"etag"` // plaintext etag
	ContentType  string            `json:"content_type"`
	LastModified time.Time         `json:"last_modified"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	Envelope     string            `json:"envelope,omitempty"` // SSE: AES-GCM envelope JSON
}

var _ GenerationBoundStorage = (*LocalStorage)(nil)

// NewLocal returns a filesystem-backed Storage rooted at cfg.Root.
func NewLocal(cfg LocalConfig) (*LocalStorage, error) {
	if cfg.Root == "" {
		return nil, errors.New("local storage: root is required")
	}
	if err := os.MkdirAll(cfg.Root, 0o755); err != nil {
		return nil, fmt.Errorf("create root: %w", err)
	}
	ls := &LocalStorage{cfg: cfg, uploads: make(map[string]*localUpload)}
	// Remote KMS wrapping takes precedence over local-key providers.
	if w := newDataKeyWrapper(cfg); w != nil {
		ls.enc = newWrappingEncrypter(w)
		return ls, nil
	}
	provider, err := newSecretProvider(cfg)
	if err != nil {
		return nil, fmt.Errorf("init sse: %w", err)
	}
	if provider != nil {
		ls.enc = newEnvelopeEncrypter(provider)
	}
	return ls, nil
}

func (s *LocalStorage) Backend() string { return "local" }

func (s *LocalStorage) SupportsServerSideEncryption(algorithm, keyID string) bool {
	if s.enc == nil {
		return false
	}
	switch algorithm {
	case "AES256":
		return keyID == "" && s.enc.provider != nil
	case "aws:kms":
		if s.enc.wrapper == nil {
			return false
		}
		return keyID == "" || s.cfg.SSEKMSKeyID == "" || keyID == s.cfg.SSEKMSKeyID
	default:
		return false
	}
}

func (s *LocalStorage) objectPath(key string) (string, error) {
	if err := validateObjectKey(key); err != nil {
		return "", ErrInvalidKey
	}
	clean := filepath.Clean(filepath.FromSlash(key))
	full := filepath.Join(s.cfg.Root, clean)
	// Guard against escaping root via symlinks/traversal.
	rel, err := filepath.Rel(s.cfg.Root, full)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", ErrInvalidKey
	}
	return full, nil
}
