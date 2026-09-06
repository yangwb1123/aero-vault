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

// ErrUnsupportedContentEncoding identifies an object whose stored bytes are
// declared with Content-Encoding. Thumbnail derivation only accepts the
// opaque, already-decodable representation; raw object reads remain allowed.
var ErrUnsupportedContentEncoding = errors.New("thumbnail: unsupported content encoding")

// ErrNilSource identifies a missing object stream at the thumbnail opener
// boundary. It is distinct from decode and magic-classification errors so
// protocol adapters can report a server-side failure without exposing the
// implementation detail or treating it as invalid image content.
var ErrNilSource = errors.New("thumbnail: nil source reader")

// ErrNotAnImage is returned by AdmitByMagic when the magic bytes match no
// known image format (or the head is too short to contain one): the object
// bytes are not a supported image, the client-argument class (REST maps it
// to 400 InvalidArgument). It is the byte-decided analogue of the declared-
// type gate's "not an image" path.
var ErrNotAnImage = errors.New("thumbnail: object bytes are not a supported image format")
