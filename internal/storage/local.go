package storage

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// LocalConfig configures the on-disk backend.
type LocalConfig struct {
	Root      string // root directory holding objects
	PublicURL string // optional, used to build presigned URLs (e.g. http://host:8080/files)
	SignKey   string // HMAC key for presigning; empty disables presigning
	SSEKey    string // master key for server-side encryption; empty disables SSE
}

// LocalStorage stores objects on the local filesystem. Metadata is sidecar JSON.
type LocalStorage struct {
	cfg LocalConfig
	enc *envelopeEncrypter // nil when SSE disabled

	mu      sync.RWMutex
	uploads map[string]*localUpload // uploadID -> parts dir state
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

// NewLocal returns a filesystem-backed Storage rooted at cfg.Root.
func NewLocal(cfg LocalConfig) (*LocalStorage, error) {
	if cfg.Root == "" {
		return nil, errors.New("local storage: root is required")
	}
	if err := os.MkdirAll(cfg.Root, 0o755); err != nil {
		return nil, fmt.Errorf("create root: %w", err)
	}
	ls := &LocalStorage{cfg: cfg, uploads: make(map[string]*localUpload)}
	if cfg.SSEKey != "" {
		enc, err := newEnvelopeEncrypter(cfg.SSEKey)
		if err != nil {
			return nil, fmt.Errorf("init sse: %w", err)
		}
		ls.enc = enc
	}
	return ls, nil
}

func (s *LocalStorage) Backend() string { return "local" }

func (s *LocalStorage) objectPath(key string) (string, error) {
	if key == "" || strings.HasPrefix(key, "/") || strings.Contains(key, "..") {
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

func (s *LocalStorage) metaPath(p string) string { return p + localMetaSuffix }

func (s *LocalStorage) Put(ctx context.Context, key string, r io.Reader, size int64, opts PutOptions) (ObjectInfo, error) {
	path, err := s.objectPath(key)
	if err != nil {
		return ObjectInfo{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return ObjectInfo{}, err
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".upload-*")
	if err != nil {
		return ObjectInfo{}, err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()

	h := md5.New()
	var (
		reader   io.Reader = io.TeeReader(r, h)
		envelope string
	)
	// SSE: read the plaintext into memory, compute etag, then write ciphertext.
	if s.enc != nil {
		plain, err := io.ReadAll(reader)
		if err != nil {
			return ObjectInfo{}, err
		}
		ct, env, err := s.enc.encrypt(plain)
		if err != nil {
			return ObjectInfo{}, err
		}
		envelope = env
		reader = bytesReader(ct)
	}
	written, err := io.Copy(tmp, reader)
	if err != nil {
		return ObjectInfo{}, err
	}
	if err := tmp.Sync(); err != nil {
		return ObjectInfo{}, err
	}
	if err := tmp.Close(); err != nil {
		return ObjectInfo{}, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return ObjectInfo{}, err
	}

	plainSize := written
	if s.enc != nil {
		// `written` is the ciphertext length; reconstruct plaintext size by
		// subtracting the 16-byte GCM tag.
		plainSize = written - 16
		if plainSize < 0 {
			plainSize = 0
		}
	}
	meta := localMeta{
		Key:          key,
		Size:         plainSize,
		ETag:         hex.EncodeToString(h.Sum(nil)),
		ContentType:  opts.ContentType,
		LastModified: time.Now().UTC(),
		Metadata:     opts.Metadata,
		Envelope:     envelope,
	}
	if err := writeMeta(s.metaPath(path), meta); err != nil {
		return ObjectInfo{}, err
	}
	return meta.toInfo(), nil
}

func (s *LocalStorage) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	info, err := s.Stat(ctx, key)
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	path, _ := s.objectPath(key)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ObjectInfo{}, ErrNotFound
		}
		return nil, ObjectInfo{}, err
	}
	// Decrypt on the fly if the object has an SSE envelope.
	if s.enc != nil {
		meta, mErr := readMeta(s.metaPath(path))
		if mErr == nil && meta.Envelope != "" {
			rc, err := decryptReader(f, meta.Envelope, s.enc)
			_ = f.Close()
			if err != nil {
				return nil, ObjectInfo{}, fmt.Errorf("sse decrypt: %w", err)
			}
			return rc, info, nil
		}
	}
	return f, info, nil
}

// bytesReader is a tiny adapter so we can swap a *bytes.Reader into the io.Reader chain.
func bytesReader(b []byte) io.Reader { return &byteSliceReader{b: b} }

type byteSliceReader struct {
	b   []byte
	off int
}

func (r *byteSliceReader) Read(p []byte) (int, error) {
	if r.off >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.off:])
	r.off += n
	return n, nil
}

func (s *LocalStorage) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	path, err := s.objectPath(key)
	if err != nil {
		return ObjectInfo{}, err
	}
	meta, err := readMeta(s.metaPath(path))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if _, statErr := os.Stat(path); statErr == nil {
				// object exists without sidecar — fabricate metadata
				info, _ := os.Stat(path)
				return ObjectInfo{
					Key:          key,
					Size:         info.Size(),
					LastModified: info.ModTime().UTC(),
				}, nil
			}
			return ObjectInfo{}, ErrNotFound
		}
		return ObjectInfo{}, err
	}
	return meta.toInfo(), nil
}

func (s *LocalStorage) Delete(ctx context.Context, key string) error {
	path, err := s.objectPath(key)
	if err != nil {
		return err
	}
	_ = os.Remove(s.metaPath(path))
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s *LocalStorage) List(ctx context.Context, prefix, marker string, limit int) (ListResult, error) {
	if limit <= 0 {
		limit = 1000
	}
	root := s.cfg.Root
	var matches []ObjectInfo
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || strings.HasSuffix(path, localMetaSuffix) {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		if prefix != "" && !strings.HasPrefix(key, prefix) {
			return nil
		}
		if marker != "" && key <= marker {
			return nil
		}
		info, err := s.Stat(ctx, key)
		if err != nil {
			return nil
		}
		matches = append(matches, info)
		return nil
	})
	if err != nil {
		return ListResult{}, err
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].Key < matches[j].Key })

	res := ListResult{}
	if len(matches) > limit {
		res.Objects = matches[:limit]
		res.NextMarker = matches[limit-1].Key
		res.HasMore = true
	} else {
		res.Objects = matches
	}
	return res, nil
}

func (s *LocalStorage) PresignGet(ctx context.Context, key string, expiry time.Duration) (string, error) {
	return s.presign(key, "GET", expiry)
}

func (s *LocalStorage) PresignPut(ctx context.Context, key string, expiry time.Duration) (string, error) {
	return s.presign(key, "PUT", expiry)
}

func (s *LocalStorage) presign(key, method string, expiry time.Duration) (string, error) {
	if s.cfg.PublicURL == "" || s.cfg.SignKey == "" {
		return "", errors.New("local presign disabled: configure PublicURL and SignKey")
	}
	if _, err := s.objectPath(key); err != nil {
		return "", err
	}
	exp := time.Now().Add(expiry).Unix()
	sig := signLocal(s.cfg.SignKey, method, key, exp)
	base := strings.TrimRight(s.cfg.PublicURL, "/")
	q := url.Values{}
	q.Set("expires", fmt.Sprintf("%d", exp))
	q.Set("sig", sig)
	q.Set("method", method)
	return fmt.Sprintf("%s/%s?%s", base, url.PathEscape(key), q.Encode()), nil
}

// VerifyLocalSig validates a presigned-URL signature. Exposed so the HTTP layer can gate downloads.
func VerifyLocalSig(signKey, method, key string, expires int64, sig string) bool {
	if signKey == "" {
		return false
	}
	if time.Now().Unix() > expires {
		return false
	}
	want := signLocal(signKey, method, key, expires)
	return hmacEqual(want, sig)
}

func (s *LocalStorage) InitMultipart(ctx context.Context, key string, opts PutOptions) (MultipartInit, error) {
	if _, err := s.objectPath(key); err != nil {
		return MultipartInit{}, err
	}
	uploadID := uuid.NewString()
	dir := filepath.Join(s.cfg.Root, ".multipart", uploadID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return MultipartInit{}, err
	}
	s.mu.Lock()
	s.uploads[uploadID] = &localUpload{key: key, dir: dir, createdAt: time.Now(), opts: opts}
	s.mu.Unlock()
	return MultipartInit{Key: key, UploadID: uploadID}, nil
}

func (s *LocalStorage) UploadPart(ctx context.Context, key, uploadID string, partNumber int32, r io.Reader, size int64) (MultipartPart, error) {
	s.mu.RLock()
	up, ok := s.uploads[uploadID]
	s.mu.RUnlock()
	if !ok || up.key != key {
		return MultipartPart{}, fmt.Errorf("unknown upload %s", uploadID)
	}
	path := filepath.Join(up.dir, fmt.Sprintf("part-%05d", partNumber))
	f, err := os.Create(path)
	if err != nil {
		return MultipartPart{}, err
	}
	h := md5.New()
	if _, err := io.Copy(io.MultiWriter(f, h), r); err != nil {
		_ = f.Close()
		return MultipartPart{}, err
	}
	if err := f.Close(); err != nil {
		return MultipartPart{}, err
	}
	return MultipartPart{PartNumber: partNumber, ETag: hex.EncodeToString(h.Sum(nil))}, nil
}

func (s *LocalStorage) CompleteMultipart(ctx context.Context, key, uploadID string, parts []MultipartPart) (ObjectInfo, error) {
	s.mu.Lock()
	up, ok := s.uploads[uploadID]
	if ok {
		delete(s.uploads, uploadID)
	}
	s.mu.Unlock()
	if !ok || up.key != key {
		return ObjectInfo{}, fmt.Errorf("unknown upload %s", uploadID)
	}
	defer os.RemoveAll(up.dir)

	sort.Slice(parts, func(i, j int) bool { return parts[i].PartNumber < parts[j].PartNumber })

	dst, err := s.objectPath(key)
	if err != nil {
		return ObjectInfo{}, err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return ObjectInfo{}, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".assemble-*")
	if err != nil {
		return ObjectInfo{}, err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()

	h := md5.New()
	var total int64
	for _, p := range parts {
		partPath := filepath.Join(up.dir, fmt.Sprintf("part-%05d", p.PartNumber))
		f, err := os.Open(partPath)
		if err != nil {
			return ObjectInfo{}, err
		}
		n, err := io.Copy(io.MultiWriter(tmp, h), f)
		_ = f.Close()
		if err != nil {
			return ObjectInfo{}, err
		}
		total += n
	}
	if err := tmp.Close(); err != nil {
		return ObjectInfo{}, err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return ObjectInfo{}, err
	}
	meta := localMeta{
		Key:          key,
		Size:         total,
		ETag:         hex.EncodeToString(h.Sum(nil)),
		ContentType:  up.opts.ContentType,
		LastModified: time.Now().UTC(),
		Metadata:     up.opts.Metadata,
	}
	if err := writeMeta(s.metaPath(dst), meta); err != nil {
		return ObjectInfo{}, err
	}
	return meta.toInfo(), nil
}

func (s *LocalStorage) AbortMultipart(ctx context.Context, key, uploadID string) error {
	s.mu.Lock()
	up, ok := s.uploads[uploadID]
	if ok {
		delete(s.uploads, uploadID)
	}
	s.mu.Unlock()
	if ok {
		_ = os.RemoveAll(up.dir)
	}
	return nil
}

func (m localMeta) toInfo() ObjectInfo {
	return ObjectInfo{
		Key:          m.Key,
		Size:         m.Size,
		ETag:         m.ETag,
		ContentType:  m.ContentType,
		LastModified: m.LastModified,
		Metadata:     m.Metadata,
	}
}

func writeMeta(path string, m localMeta) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func readMeta(path string) (localMeta, error) {
	var m localMeta
	b, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, err
	}
	return m, nil
}
