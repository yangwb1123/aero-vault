package rest

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/service"
)

// This selector disambiguates protocol representations, never Key validity or
// authorization. An explicit miss/error must not fall back to a different key.
func fileRepresentation(r *http.Request) (string, error) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return "", fmt.Errorf("%w: invalid file read query", service.ErrInvalidArgs)
	}
	values, present := query["representation"]
	if !present {
		return presignedContentRepresentation(r), nil
	}
	if len(values) != 1 || (values[0] != "content" && values[0] != "thumbnail") {
		return "", fmt.Errorf("%w: representation must be exactly content or thumbnail", service.ErrInvalidArgs)
	}
	return values[0], nil
}

func presignedContentRepresentation(r *http.Request) string {
	principal, ok := access.PrincipalFrom(r.Context())
	if ok && principal.Capability != nil && principal.Capability.ID == "presigned-get" {
		// Auth already verified the signature and bound this capability to the
		// full object path. Do not infer trust from signature-looking query text.
		return "content"
	}
	return ""
}
