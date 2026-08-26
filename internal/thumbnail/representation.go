package thumbnail

import "strconv"

// currentRepresentationToken identifies the output-byte representation used
// by the thumbnail pipeline. It is distinct from CacheKeyVersion: the latter
// is the cache/wire schema family, while this token tracks output-affecting
// representation settings. Today JPEG quality is the only such setting; any
// future encoder or pixel-output setting must be added to this builder.
var currentRepresentationToken = representationTokenForJPEGQuality(quality)

func representationTokenForJPEGQuality(q int) string {
	return digestToken("aero-vault/thumbnail-representation/jpeg/v1", strconv.Itoa(q))
}
