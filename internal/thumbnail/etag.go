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
