package rest

import (
	"net/http"
	"strconv"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// All raw content responses describe their authorized object, including stored
// attachment/encoding declarations. Never fetch current metadata for a pin.
func writeContentHeaders(w http.ResponseWriter, obj repository.Object, length int64, pinned bool) {
	writeContentIdentityHeaders(w, obj, pinned)
	w.Header().Set("Accept-Ranges", "bytes")
	if obj.ContentType != "" {
		w.Header().Set("Content-Type", obj.ContentType)
	}
	// Unconditional length keeps empty GET/HEAD parity; ranges supply their length.
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
}

func writeContentNotModified(w http.ResponseWriter, obj repository.Object, pinned bool) {
	writeContentIdentityHeaders(w, obj, pinned)
	if obj.ContentType != "" {
		w.Header().Set("Content-Type", obj.ContentType)
	}
	w.WriteHeader(http.StatusNotModified)
}

func writeContentIdentityHeaders(w http.ResponseWriter, obj repository.Object, pinned bool) {
	w.Header().Set("ETag", `"`+obj.ETag+`"`)
	w.Header().Set("Last-Modified", obj.UpdatedAt.UTC().Format(http.TimeFormat))
	if pinned {
		w.Header().Set("X-Version-Id", obj.VersionID)
	}
	writeMetadataHeaders(w, obj.Metadata)
	writeContentMD5(w, obj.Metadata)
	writeContentResponseHeaders(w, obj.Metadata)
	writeStorageClass(w, obj.StorageClass)
}
