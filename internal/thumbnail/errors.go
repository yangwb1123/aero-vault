package thumbnail

import "errors"

// ErrUnsupportedFormat is returned by the REST layer when an object's
// declared Content-Type is an image the pipeline cannot decode (anything
// outside image/jpeg, image/png, image/gif). It is distinct from
// ErrUnsupported (bytes are not decodable at all) so callers can tell a
// server-capability rejection from a corrupt/non-image input.
//
// It is produced by the REST handler from the declared Content-Type — never
// by Generate/GenerateContext, which keep returning ErrUnsupported for
// byte-level failures (corrupt or non-image input).
var ErrUnsupportedFormat = errors.New("thumbnail: unsupported image format")
