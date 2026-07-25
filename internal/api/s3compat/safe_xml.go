package s3compat

import (
	"encoding/xml"
	"fmt"
	"io"
)

// DefaultXMLMaxBytes is the maximum XML body size the server will accept.
// Requests exceeding this limit are rejected at the decoder level.
const DefaultXMLMaxBytes = 1 << 20 // 1 MB

// safeXMLDecoder returns an *xml.Decoder that reads at most maxBytes from r.
// This prevents XML bombs / entity expansion attacks that could otherwise
// exhaust server memory.
//
// Usage:
//
//	var in someType
//	dec := safeXMLDecoder(r.Body, DefaultXMLMaxBytes)
//	if err := dec.Decode(&in); err != nil { ... }
func safeXMLDecoder(r io.Reader, maxBytes int64) *xml.Decoder {
	return xml.NewDecoder(io.LimitReader(r, maxBytes))
}

// decodeXMLBody is a convenience wrapper that decodes xml from r into dest
// with a size limit. It returns a user-facing error suitable for S3 error
// responses when the body is malformed or too large.
func decodeXMLBody(r io.Reader, maxBytes int64, dest any) error {
	dec := safeXMLDecoder(r, maxBytes)
	if err := dec.Decode(dest); err != nil {
		return fmt.Errorf("decode xml: %w", err)
	}
	return nil
}
