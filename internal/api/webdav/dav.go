// Package webdav exposes aero-vault as a WebDAV file system, suitable for
// mounting from macOS Finder ("Connect to Server"), Windows Explorer, or any
// WebDAV client (rclone, cyberduck, davs2fuse).
//
// The implementation adapts the FileService to golang.org/x/net/webdav.
// Tenant is read from the X-Aero-Tenant header (defaults to "default").
package webdav

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"

	xwebdav "golang.org/x/net/webdav"

	"github.com/aero-vault/aero-vault/internal/access"
	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/service"
)

// Handler returns an http.Handler implementing WebDAV at the given prefix.
func Handler(prefix string, svc *service.FileService, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	fsys := &davFS{svc: svc, logger: logger}
	dav := &xwebdav.Handler{
		Prefix:     prefix,
		FileSystem: fsys,
		LockSystem: xwebdav.NewMemLS(),
		Logger: func(r *http.Request, err error) {
			if err != nil {
				logger.Debug("webdav", "method", r.Method, "path", r.URL.Path, "err", err)
			}
		},
	}
	// x/net/webdav serves GET/HEAD via http.ServeContent, which sniffs the
	// Content-Type itself and never consults the FileSystem's ContentTyper.
	// Pre-set the stored Content-Type so ServeContent honours it instead of
	// sniffing; PROPFIND already reads ContentTyper directly.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			name := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, prefix), "/")
			if err := fsys.svc.AuthorizeDelete(r.Context(), fsys.tenant(r.Context()), service.DefaultBucket, name, true); err != nil && errors.Is(err, service.ErrForbidden) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			if name, ok := strings.CutPrefix(r.URL.Path, prefix); ok {
				if ct := fsys.storedContentType(r.Context(), name); ct != "" {
					w.Header().Set("Content-Type", ct)
				}
			}
		}
		dav.ServeHTTP(w, r)
	})
}

// maxListPage caps the number of object rows requested per List() page. The
// repository clamps any larger limit to this same value, so it is also the
// natural page size to paginate by when walking a directory.
const maxListPage = 1000

// probeLimit is the page size used when List() is called only to test whether
// any object exists under a prefix (implicit/virtual directory detection): one
// row is enough to answer the existence question.
const probeLimit = 1

// davFS is the FileSystem implementation backed by the FileService.
type davFS struct {
	svc    *service.FileService
	logger *slog.Logger
}

func (f *davFS) tenant(ctx context.Context) string {
	if t := mw.TenantFrom(ctx); t != "" {
		return t
	}
	return "default"
}

// storedContentType returns the content-type recorded for the object at name,
// or "" when there is none (directory, missing object, or any error) so the
// caller leaves x/net/webdav's own sniffing untouched.
func (f *davFS) storedContentType(ctx context.Context, name string) string {
	name = strings.TrimPrefix(name, "/")
	if name == "" || strings.HasSuffix(name, "/") {
		return ""
	}
	obj, err := f.svc.Stat(ctx, f.tenant(ctx), service.DefaultBucket, name)
	if err != nil {
		return ""
	}
	return obj.ContentType
}

func (f *davFS) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	// WebDAV directories are virtual — no-op. Files create their own implicit dirs.
	return nil
}

func (f *davFS) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (xwebdav.File, error) {
	name = strings.TrimPrefix(name, "/")
	tenant := f.tenant(ctx)
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE) != 0 {
		return &davWriter{ctx: ctx, svc: f.svc, tenant: tenant, key: name, logger: f.logger}, nil
	}
	if name == "" || strings.HasSuffix(name, "/") {
		return &davDir{ctx: ctx, svc: f.svc, tenant: tenant, prefix: name}, nil
	}
	obj, err := f.svc.Stat(ctx, tenant, service.DefaultBucket, name)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			// Treat as a directory listing if any objects exist under that prefix.
			// Propagate a real repository error rather than masking it as a 404.
			page, perr := f.svc.List(ctx, tenant, service.DefaultBucket, name+"/", "", probeLimit)
			if perr != nil {
				return nil, perr
			}
			if len(page.Objects) > 0 {
				return &davDir{ctx: ctx, svc: f.svc, tenant: tenant, prefix: name + "/"}, nil
			}
			return nil, os.ErrNotExist
		}
		return nil, err
	}
	rc, _, err := f.svc.Get(ctx, tenant, service.DefaultBucket, name)
	if err != nil {
		return nil, err
	}
	// Stream the body into a bounded spill buffer so Seek is supported
	// (golang.org/x/net/webdav requires it for serving Ranges and computing
	// PROPFIND getcontentlength) without holding the whole object in memory:
	// payloads larger than spillThreshold spill to a temp file.
	buf := newSpillBuffer()
	err = buf.fill(rc)
	_ = rc.Close()
	if err != nil {
		_ = buf.Close()
		return nil, err
	}
	return &davReader{buf: buf, info: davFileInfo(obj)}, nil
}

func (f *davFS) RemoveAll(ctx context.Context, name string) error {
	name = strings.TrimPrefix(name, "/")
	err := f.svc.DeleteWithAction(ctx, f.tenant(ctx), service.DefaultBucket, name, true, access.ActionAdminDelete)
	if errors.Is(err, service.ErrNotFound) {
		return os.ErrNotExist
	}
	return err
}

func (f *davFS) Rename(ctx context.Context, oldName, newName string) error {
	// WebDAV-required for drag-and-drop renames in Finder. Implement via
	// copy-then-delete (atomic enough for MVP).
	tenant := f.tenant(ctx)
	if err := f.svc.AuthorizeDelete(ctx, tenant, service.DefaultBucket, strings.TrimPrefix(oldName, "/"), true); err != nil {
		return err
	}
	rc, src, err := f.svc.Get(ctx, tenant, service.DefaultBucket, strings.TrimPrefix(oldName, "/"))
	if err != nil {
		return err
	}
	// Stream the source through a bounded spill buffer (same as the read path)
	// so a large object is not pinned in memory during the copy.
	buf := newSpillBuffer()
	defer buf.Close()
	err = buf.fill(rc)
	_ = rc.Close()
	if err != nil {
		return err
	}
	// Carry the source object's ContentType, user metadata, and tags across the
	// move; copy the maps so the new object never aliases the source's.
	opts := service.PutOptions{
		ContentType:        src.ContentType,
		ContentDisposition: src.Metadata["_aero_content_disposition"],
		ContentEncoding:    src.Metadata["_aero_content_encoding"],
		ContentMD5:         src.Metadata["_aero_content_md5"],
	}
	if len(src.Metadata) > 0 {
		opts.Metadata = make(map[string]string, len(src.Metadata))
		for k, v := range src.Metadata {
			if !strings.HasPrefix(strings.ToLower(k), "_aero_") {
				opts.Metadata[k] = v
			}
		}
	}
	if len(src.Tags) > 0 {
		opts.Tags = make(map[string]string, len(src.Tags))
		for k, v := range src.Tags {
			opts.Tags[k] = v
		}
	}
	dst := strings.TrimPrefix(newName, "/")
	src2 := strings.TrimPrefix(oldName, "/")
	if _, err := f.svc.Put(ctx, tenant, service.DefaultBucket, dst,
		buf, buf.Len(), opts); err != nil {
		return err
	}
	// copy-then-delete: if the delete of the source fails after the destination
	// is written, both names would otherwise exist (a duplicate). Roll back by
	// removing the just-written destination before surfacing the error.
	if err := f.svc.DeleteWithAction(ctx, tenant, service.DefaultBucket, src2, true, access.ActionAdminDelete); err != nil {
		if delErr := f.svc.Delete(ctx, tenant, service.DefaultBucket, dst, true); delErr != nil {
			f.logger.Warn("webdav rename rollback failed", "dst", dst, "err", delErr)
		}
		return err
	}
	return nil
}

func (f *davFS) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	name = strings.TrimPrefix(name, "/")
	tenant := f.tenant(ctx)
	if name == "" {
		return davDirInfo("/"), nil
	}
	if strings.HasSuffix(name, "/") {
		// A trailing slash denotes a directory (mirrors OpenFile). Probe with the
		// name as-is; appending another "/" here would build a "dir//" prefix that
		// matches nothing and wrongly 404s a PROPFIND on a subdirectory.
		page, err := f.svc.List(ctx, tenant, service.DefaultBucket, name, "", probeLimit)
		if err != nil {
			return nil, err
		}
		if len(page.Objects) > 0 {
			return davDirInfo(name), nil
		}
		return nil, os.ErrNotExist
	}
	obj, err := f.svc.Stat(ctx, tenant, service.DefaultBucket, name)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			page, perr := f.svc.List(ctx, tenant, service.DefaultBucket, name+"/", "", probeLimit)
			if perr != nil {
				return nil, perr
			}
			if len(page.Objects) > 0 {
				return davDirInfo(name), nil
			}
			return nil, os.ErrNotExist
		}
		return nil, err
	}
	return davFileInfo(obj), nil
}
