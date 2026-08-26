package rest

import (
	"fmt"
	"net/http"

	"github.com/aero-vault/aero-vault/internal/thumbnail"
)

func thumbValidatorETag(
	version uint8,
	identity thumbnail.SourceIdentity,
	sourceETag string,
	effW, effH int,
) string {
	if !identity.Complete() {
		return ""
	}
	token := thumbnail.DerivedValidatorToken(version, identity, sourceETag, effW, effH)
	return formatThumbValidator(version, token, effW, effH)
}

func thumbValidatorETagWithRepresentation(
	version uint8,
	representation string,
	identity thumbnail.SourceIdentity,
	sourceETag string,
	effW, effH int,
) string {
	if !identity.Complete() {
		return ""
	}
	token := thumbnail.DerivedValidatorTokenWithRepresentation(version, representation, identity, sourceETag, effW, effH)
	return formatThumbValidator(version, token, effW, effH)
}

func formatThumbValidator(version uint8, token string, effW, effH int) string {
	return fmt.Sprintf("av-thumb-v%d-%s-%dx%d", version, token, effW, effH)
}

func quotedThumbETag(token string) string {
	if token == "" {
		return ""
	}
	return `"` + token + `"`
}

func setThumbnailETag(w http.ResponseWriter, token string) {
	if quoted := quotedThumbETag(token); quoted != "" {
		w.Header().Set("ETag", quoted)
	}
}
