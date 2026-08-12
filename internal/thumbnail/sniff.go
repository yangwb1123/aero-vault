package thumbnail

// Format is an image format identified by magic bytes.
type Format uint8

const (
	FormatUnknown Format = iota // no recognized magic (or head too short)
	FormatJPEG                  // 0xFF 0xD8 (SOI)
	FormatPNG                   // 0x89 'P' 'N' 'G'
	FormatGIF                   // "GIF" (GIF87a / GIF89a)
	FormatWebP                  // "RIFF" .... "WEBP"
)

// Sniff returns the image format indicated by the leading magic bytes of
// head, or FormatUnknown when no recognized magic is present or head is too
// short to contain one. It never panics and allocates nothing.
//
// It exists so the REST thumbnail handler can distinguish, for objects whose
// declared Content-Type is absent or generic (application/octet-stream),
// "a valid image the server cannot decode" (WebP → 415 UnsupportedMediaType,
// the server-capability class per RFC 9110 §15.5.16) from "not an image"
// (unknown → 400 InvalidArgument, the client-argument class) — a distinction
// the declared gate makes for image/* declarations and which bytes must now
// make for undeclared ones. image.DecodeConfig cannot be used for this: it
// requires a stream, allocates, and reports WebP as unsupported without
// distinguishing it from garbage.
//
// Sniff is a gate input only. The decode pipeline (DecodeConfig → decode)
// remains the final validity authority: a false positive merely admits an
// object to decode, which then fails with ErrUnsupported exactly as today.
func Sniff(head []byte) Format {
	if len(head) >= 2 && head[0] == 0xFF && head[1] == 0xD8 {
		return FormatJPEG
	}
	if len(head) >= 4 && head[0] == 0x89 && head[1] == 'P' && head[2] == 'N' && head[3] == 'G' {
		return FormatPNG
	}
	if len(head) >= 3 && head[0] == 'G' && head[1] == 'I' && head[2] == 'F' {
		return FormatGIF
	}
	if len(head) >= 12 &&
		head[0] == 'R' && head[1] == 'I' && head[2] == 'F' && head[3] == 'F' &&
		head[8] == 'W' && head[9] == 'E' && head[10] == 'B' && head[11] == 'P' {
		return FormatWebP
	}
	return FormatUnknown
}
