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
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	xwebdav "golang.org/x/net/webdav"

	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
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
			page, _ := f.svc.List(ctx, tenant, service.DefaultBucket, name+"/", "", 1)
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
	err := f.svc.Delete(ctx, f.tenant(ctx), service.DefaultBucket, name, true)
	if errors.Is(err, service.ErrNotFound) {
		return os.ErrNotExist
	}
	return err
}

func (f *davFS) Rename(ctx context.Context, oldName, newName string) error {
	// WebDAV-required for drag-and-drop renames in Finder. Implement via
	// copy-then-delete (atomic enough for MVP).
	tenant := f.tenant(ctx)
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
	opts := service.PutOptions{ContentType: src.ContentType}
	if len(src.Metadata) > 0 {
		opts.Metadata = make(map[string]string, len(src.Metadata))
		for k, v := range src.Metadata {
			opts.Metadata[k] = v
		}
	}
	if len(src.Tags) > 0 {
		opts.Tags = make(map[string]string, len(src.Tags))
		for k, v := range src.Tags {
			opts.Tags[k] = v
		}
	}
	if _, err := f.svc.Put(ctx, tenant, service.DefaultBucket, strings.TrimPrefix(newName, "/"),
		buf, buf.Len(), opts); err != nil {
		return err
	}
	return f.svc.Delete(ctx, tenant, service.DefaultBucket, strings.TrimPrefix(oldName, "/"), true)
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
		if page, _ := f.svc.List(ctx, tenant, service.DefaultBucket, name, "", 1); len(page.Objects) > 0 {
			return davDirInfo(name), nil
		}
		return nil, os.ErrNotExist
	}
	obj, err := f.svc.Stat(ctx, tenant, service.DefaultBucket, name)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			page, _ := f.svc.List(ctx, tenant, service.DefaultBucket, name+"/", "", 1)
			if len(page.Objects) > 0 {
				return davDirInfo(name), nil
			}
			return nil, os.ErrNotExist
		}
		return nil, err
	}
	return davFileInfo(obj), nil
}

// --- File adapters ---

type davReader struct {
	buf  *spillBuffer
	info os.FileInfo
}

func (r *davReader) Read(p []byte) (int, error)  { return r.buf.Read(p) }
func (r *davReader) Write(p []byte) (int, error) { return 0, fs.ErrPermission }
func (r *davReader) Close() error                { return r.buf.Close() }
func (r *davReader) Seek(offset int64, whence int) (int64, error) {
	return r.buf.Seek(offset, whence)
}
func (r *davReader) Readdir(count int) ([]os.FileInfo, error) { return nil, fs.ErrInvalid }
func (r *davReader) Stat() (os.FileInfo, error)               { return r.info, nil }

type davWriter struct {
	ctx    context.Context // request context, captured at OpenFile (Close has none)
	svc    *service.FileService
	tenant string
	key    string
	logger *slog.Logger
	buf    *spillBuffer
}

func (w *davWriter) Write(p []byte) (int, error) {
	if w.buf == nil {
		w.buf = newSpillBuffer()
	}
	return w.buf.Write(p)
}
func (w *davWriter) Read(p []byte) (int, error)                   { return 0, io.EOF }
func (w *davWriter) Seek(offset int64, whence int) (int64, error) { return 0, fs.ErrInvalid }
func (w *davWriter) Readdir(count int) ([]os.FileInfo, error)     { return nil, fs.ErrInvalid }
func (w *davWriter) Stat() (os.FileInfo, error) {
	var size int64
	if w.buf != nil {
		size = w.buf.Len()
	}
	return &davInfo{name: path.Base(w.key), size: size, mod: time.Now()}, nil
}
func (w *davWriter) Close() error {
	if w.svc == nil || w.key == "" {
		if w.buf != nil {
			return w.buf.Close()
		}
		return nil
	}
	buf := w.buf
	if buf == nil {
		// Zero-length upload (e.g. PUT with empty body).
		buf = newSpillBuffer()
	}
	defer buf.Close()
	size := buf.Len()
	// Rewind so Put streams the bytes we just wrote rather than starting at EOF.
	if _, err := buf.Seek(0, io.SeekStart); err != nil {
		return err
	}
	ctx := w.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	_, err := w.svc.Put(ctx, w.tenant, service.DefaultBucket, w.key,
		buf, size, service.PutOptions{})
	return err
}

type davDir struct {
	ctx    context.Context // request context, captured at OpenFile (Readdir has none)
	svc    *service.FileService
	tenant string
	prefix string
	cur    int
}

func (d *davDir) Read(p []byte) (int, error)     { return 0, io.EOF }
func (d *davDir) Write(p []byte) (int, error)    { return 0, fs.ErrPermission }
func (d *davDir) Seek(int64, int) (int64, error) { return 0, fs.ErrInvalid }
func (d *davDir) Close() error                   { return nil }
func (d *davDir) Stat() (os.FileInfo, error)     { return davDirInfo(d.prefix), nil }
func (d *davDir) Readdir(count int) ([]os.FileInfo, error) {
	prefix := strings.TrimPrefix(d.prefix, "/")
	ctx := d.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	page, err := d.svc.List(ctx, d.tenant, service.DefaultBucket, prefix, "", 1000)
	if err != nil {
		return nil, err
	}
	// Collapse common nested-dir prefixes to single virtual dir entries.
	seen := map[string]bool{}
	var out []os.FileInfo
	for _, obj := range page.Objects {
		rel := strings.TrimPrefix(obj.Key, prefix)
		if idx := strings.Index(rel, "/"); idx >= 0 {
			name := rel[:idx]
			if !seen[name] {
				seen[name] = true
				out = append(out, davDirInfo(prefix+name))
			}
			continue
		}
		out = append(out, davFileInfo(obj))
	}
	return out, nil
}

// --- FileInfo helpers ---

type davInfo struct {
	name        string
	size        int64
	mod         time.Time
	dir         bool
	contentType string
}

func (d *davInfo) Name() string { return path.Base(d.name) }
func (d *davInfo) Size() int64  { return d.size }
func (d *davInfo) Mode() os.FileMode {
	if d.dir {
		return os.ModeDir | 0o755
	}
	return 0o644
}
func (d *davInfo) ModTime() time.Time { return d.mod }
func (d *davInfo) IsDir() bool        { return d.dir }
func (d *davInfo) Sys() any           { return nil }

// ContentType implements golang.org/x/net/webdav.ContentTyper so PROPFIND
// reports the stored content-type rather than one sniffed from bytes. When no
// type is stored (unknown object / directory) it returns ErrNotImplemented so
// x/net/webdav falls back to its own DetectContentType behaviour.
func (d *davInfo) ContentType(ctx context.Context) (string, error) {
	if d.contentType != "" {
		return d.contentType, nil
	}
	return "", xwebdav.ErrNotImplemented
}

func davFileInfo(o repository.Object) os.FileInfo {
	return &davInfo{name: o.Key, size: o.Size, mod: o.UpdatedAt, contentType: o.ContentType}
}

func davDirInfo(name string) os.FileInfo {
	return &davInfo{name: strings.TrimSuffix(name, "/"), dir: true, mod: time.Now()}
}
