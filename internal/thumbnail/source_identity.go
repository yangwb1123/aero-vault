package thumbnail

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
)

// SourceIdentity is the authoritative identity of the object generation used
// to produce a thumbnail. It is deliberately kept as separate fields so cache
// keys cannot alias at field boundaries.
type SourceIdentity struct {
	TenantID  string
	Bucket    string
	Key       string
	VersionID string
}

// OpenedSource describes the source actually opened by a cached generation.
// Bound is a storage-generation proof, not merely a repository metadata echo.
type OpenedSource struct {
	Identity SourceIdentity
	ETag     string
	Bound    bool
}

// Opener opens one source stream and reports the identity and ETag observed for
// that stream. Cached generation stores bytes only when Bound is true.
type Opener func() (io.ReadCloser, OpenedSource, error)

// Complete reports whether the identity is safe for cache lookup, storage, or
// a reusable strong validator. Object.ID is intentionally not a substitute for
// VersionID: unversioned updates can retain the same database row ID.
func (id SourceIdentity) Complete() bool {
	return id.TenantID != "" && id.Bucket != "" && id.Key != "" && id.VersionID != ""
}

// Equal compares the complete authoritative identity field by field.
func (id SourceIdentity) Equal(other SourceIdentity) bool {
	return id == other
}

// Token returns a deterministic opaque identity token. Length prefixes make
// slashes, NULs, and adjacent field combinations unambiguous.
func (id SourceIdentity) Token() string {
	return digestToken("aero-vault/thumbnail-source/v1", id.TenantID, id.Bucket, id.Key, id.VersionID)
}

// DerivedValidatorToken hashes every input that affects the derived
// representation. The domain separator prevents this digest from being
// confused with another application-level token.
func DerivedValidatorToken(version uint8, id SourceIdentity, sourceETag string, effW, effH int) string {
	h := sha256.New()
	writeString(h, "aero-vault/thumbnail-validator/v2")
	writeUint(h, uint64(version))
	writeString(h, id.TenantID)
	writeString(h, id.Bucket)
	writeString(h, id.Key)
	writeString(h, id.VersionID)
	writeString(h, sourceETag)
	writeUint(h, uint64(int64(effW)))
	writeUint(h, uint64(int64(effH)))
	return hex.EncodeToString(h.Sum(nil))
}

func digestToken(domain string, fields ...string) string {
	h := sha256.New()
	for _, field := range append([]string{domain}, fields...) {
		writeString(h, field)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func writeString(h interface{ Write([]byte) (int, error) }, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = h.Write(length[:])
	_, _ = h.Write([]byte(value))
}

func writeUint(h interface{ Write([]byte) (int, error) }, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	_, _ = h.Write(encoded[:])
}
