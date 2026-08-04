package rest

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aero-vault/aero-vault/internal/access"
	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

type exportManifest struct {
	Version   int               `json:"version"`
	TenantID  string            `json:"tenant_id"`
	Bucket    string            `json:"bucket"`
	Prefix    string            `json:"prefix,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	Objects   []exportObjectRef `json:"objects"`
}

type exportObjectRef struct {
	Key         string            `json:"key"`
	Size        int64             `json:"size"`
	ETag        string            `json:"etag"`
	ContentType string            `json:"content_type,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

// ExportArchive streams a portable tar.gz containing manifest.json and the
// authorized object bytes. It works for local and cloud storage backends.
func (h *Handler) ExportArchive(w http.ResponseWriter, r *http.Request) {
	tenant := mw.TenantFrom(r.Context())
	bucket := r.URL.Query().Get("bucket")
	if bucket == "" {
		bucket = service.DefaultBucket
	}
	prefix := strings.TrimPrefix(r.URL.Query().Get("prefix"), "/")
	objects, err := h.exportObjects(r, tenant, bucket, prefix)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	manifest := buildExportManifest(tenant, bucket, prefix, objects)
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(
		`attachment; filename="aero-%s-%s.tar.gz"`, safeArchiveName(bucket), time.Now().UTC().Format("20060102T150405Z"),
	))
	w.WriteHeader(http.StatusOK)
	if err := h.writeExportArchive(r, w, manifest, objects); err != nil {
		h.logger.Warn("archive export failed", "tenant", tenant, "bucket", bucket, "err", err)
	}
}

func (h *Handler) exportObjects(
	r *http.Request,
	tenant, bucket, prefix string,
) ([]repository.Object, error) {
	objects := make([]repository.Object, 0)
	marker := ""
	for {
		page, err := h.svc.List(r.Context(), tenant, bucket, prefix, marker, 1000)
		if err != nil {
			return nil, err
		}
		for _, obj := range page.Objects {
			if err := h.svc.AuthorizeObjectAction(r.Context(), access.ActionExport, obj); err != nil {
				if errors.Is(err, service.ErrForbidden) {
					continue
				}
				return nil, err
			}
			objects = append(objects, obj)
		}
		if !page.HasMore {
			return objects, nil
		}
		marker = page.NextMarker
	}
}

func buildExportManifest(
	tenant, bucket, prefix string,
	objects []repository.Object,
) exportManifest {
	manifest := exportManifest{
		Version: 1, TenantID: tenant, Bucket: bucket, Prefix: prefix,
		CreatedAt: time.Now().UTC(), Objects: make([]exportObjectRef, 0, len(objects)),
	}
	for _, obj := range objects {
		manifest.Objects = append(manifest.Objects, exportObjectRef{
			Key: obj.Key, Size: obj.Size, ETag: obj.ETag, ContentType: obj.ContentType,
			Metadata: userVisibleMetadata(obj.Metadata), Tags: obj.Tags, UpdatedAt: obj.UpdatedAt,
		})
	}
	return manifest
}

func (h *Handler) writeExportArchive(
	r *http.Request,
	w io.Writer,
	manifest exportManifest,
	objects []repository.Object,
) error {
	gzipWriter := gzip.NewWriter(w)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := writeManifestEntry(tarWriter, manifest); err != nil {
		return closeArchiveWriters(tarWriter, gzipWriter, err)
	}
	for _, obj := range objects {
		if err := h.writeExportObject(r, tarWriter, obj); err != nil {
			return closeArchiveWriters(tarWriter, gzipWriter, err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		_ = gzipWriter.Close()
		return err
	}
	return gzipWriter.Close()
}

func writeManifestEntry(writer *tar.Writer, manifest exportManifest) error {
	payload, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	header := &tar.Header{
		Name: "manifest.json", Mode: 0o600, Size: int64(len(payload)),
		ModTime: manifest.CreatedAt,
	}
	if err := writer.WriteHeader(header); err != nil {
		return err
	}
	_, err = writer.Write(payload)
	return err
}

func (h *Handler) writeExportObject(
	r *http.Request,
	writer *tar.Writer,
	obj repository.Object,
) error {
	reader, _, err := h.svc.Get(r.Context(), obj.TenantID, obj.Bucket, obj.Key)
	if err != nil {
		return err
	}
	defer reader.Close()
	header := &tar.Header{
		Name: "objects/" + obj.Key, Mode: 0o600, Size: obj.Size, ModTime: obj.UpdatedAt,
		PAXRecords: map[string]string{
			"AERO.etag": obj.ETag, "AERO.content_type": obj.ContentType,
		},
	}
	if err := writer.WriteHeader(header); err != nil {
		return err
	}
	written, err := io.CopyN(writer, reader, obj.Size)
	if err != nil {
		return err
	}
	if written != obj.Size {
		return fmt.Errorf("export %q: copied %d of %d bytes", obj.Key, written, obj.Size)
	}
	return nil
}

func closeArchiveWriters(tw *tar.Writer, gz *gzip.Writer, source error) error {
	_ = tw.Close()
	_ = gz.Close()
	return source
}

func safeArchiveName(value string) string {
	var builder strings.Builder
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' || char == '-' || char == '_' {
			builder.WriteRune(char)
		}
	}
	if builder.Len() == 0 {
		return "default"
	}
	return builder.String()
}
