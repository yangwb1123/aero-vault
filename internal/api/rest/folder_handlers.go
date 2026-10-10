package rest

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/service"
)

// folderItem represents a single entry in a folder listing.
type folderItem struct {
	Name         string `json:"name"`
	Type         string `json:"type"` // "file" or "folder"
	Size         int64  `json:"size,omitempty"`
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"last_modified,omitempty"`
}

// ListFolders returns the contents of a folder (files + sub-folders).
// GET /v1/folders?path=some/path
func (h *Handler) ListFolders(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("path")
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	if !h.checkBucketPolicy(w, r, "", "s3:ListBucket") {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Add("Vary", "Authorization, X-Api-Key, X-Aero-Tenant")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	page, err := h.svc.ListWithOptions(
		r.Context(), mw.TenantFrom(r.Context()), service.DefaultBucket, prefix, r.URL.Query().Get("marker"), limit,
		service.FileListOptions{View: service.FileListView(r.URL.Query().Get("view"))},
	)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	seen := map[string]bool{}
	items := make([]folderItem, 0, len(page.Objects))
	for _, obj := range page.Objects {
		remainder := strings.TrimPrefix(obj.Key, prefix)
		if remainder == "" {
			continue
		}
		if idx := strings.IndexByte(remainder, '/'); idx >= 0 {
			dirName := remainder[:idx]
			if !seen[dirName] {
				seen[dirName] = true
				items = append(items, folderItem{Name: dirName, Type: "folder"})
			}
		} else {
			items = append(items, folderItem{
				Name: remainder, Type: "file",
				Size: obj.Size, ETag: obj.ETag,
				LastModified: obj.UpdatedAt.Format(time.RFC3339),
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"prefix": prefix, "items": items, "has_more": page.HasMore, "next_marker": page.NextMarker,
	})
}

// CreateFolder creates a zero-byte directory marker object.
// POST /v1/folders/some/path
func (h *Handler) CreateFolder(w http.ResponseWriter, r *http.Request) {
	folderPath := keyFromPath(r)
	if folderPath == "" {
		h.writeError(w, r, fmt.Errorf("%w: folder path is required", service.ErrInvalidArgs))
		return
	}
	if !strings.HasSuffix(folderPath, "/") {
		folderPath += "/"
	}
	obj, err := h.svc.Put(r.Context(), mw.TenantFrom(r.Context()), service.DefaultBucket, folderPath, strings.NewReader(""), 0, service.PutOptions{
		ContentType: "application/x-directory",
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toObjectDTO(obj))
}

// DeleteFolder removes a folder and all objects under it.
// DELETE /v1/folders/some/path
func (h *Handler) DeleteFolder(w http.ResponseWriter, r *http.Request) {
	folderPath := keyFromPath(r)
	if folderPath == "" {
		h.writeError(w, r, fmt.Errorf("%w: folder path is required", service.ErrInvalidArgs))
		return
	}
	if !strings.HasSuffix(folderPath, "/") {
		folderPath += "/"
	}
	tenant := mw.TenantFrom(r.Context())
	allKeys := []string{}
	var marker string
	for {
		page, err := h.svc.List(r.Context(), tenant, service.DefaultBucket, folderPath, marker, 1000)
		if err != nil {
			h.writeError(w, r, err)
			return
		}
		for _, obj := range page.Objects {
			allKeys = append(allKeys, obj.Key)
		}
		if !page.HasMore {
			break
		}
		marker = page.NextMarker
	}
	results := h.svc.BatchDelete(r.Context(), tenant, service.DefaultBucket, allKeys)
	failCount := 0
	for _, res := range results {
		if !res.Deleted {
			failCount++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"deleted": len(allKeys), "failed": failCount,
	})
}
