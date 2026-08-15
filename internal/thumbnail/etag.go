package thumbnail

// ContentMD5ETag reports whether etag has the whole-object content-MD5
// shape: exactly 32 lowercase hex characters, anchored both ends. This is
// the shape local storage emits for single-PUT objects (hex MD5 of the
// content) and the shape S3/OSS/COS echo for plain uploads; it is the only
// ETag class the server can attribute to content. CacheKey treats the
// source ETag as content identity, so only such ETags may seed a key —
// anything else (empty, quoted, uppercase hex, multipart "<md5>-<n>",
// provider quirks, SSE-KMS non-MD5 values) is not content-derived and must
// bypass the cache. A manual scan rather than a regexp: identical semantics
// with zero per-call compile/allocation cost on the request path.
//
// Residual risk (documented, accepted): the shape test is NOT
// collision-resistance. MD5 pre-image/collision attacks are out of scope
// for a cache identity claim — a deliberate collision would require
// attacker-chosen bytes AND attacker control of the storage ETag, and the
// worst outcome is a wrong cached thumbnail served until eviction, never a
// confidentiality breach (the payload is a public-or-private derived image
// the caller could already fetch). The cache is bounded (THUMBNAIL_CACHE_BYTES)
// and TTL-bounded, so a poisoned entry self-heals.
func ContentMD5ETag(etag string) bool {
	if len(etag) != 32 {
		return false
	}
	for i := 0; i < len(etag); i++ {
		c := etag[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
