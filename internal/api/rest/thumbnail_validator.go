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
