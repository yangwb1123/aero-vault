package webdav

import (
	"context"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"strings"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

// ── davReader ────────────────────────────────────────────────────────────────

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

// ── davWriter ────────────────────────────────────────────────────────────────

type davWriter struct {
	ctx    context.Context
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
		buf = newSpillBuffer()
	}
	defer buf.Close()
	size := buf.Len()
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

// ── davDir ───────────────────────────────────────────────────────────────────

type davDir struct {
	ctx     context.Context
	svc     *service.FileService
	tenant  string
	prefix  string
	cur     string
	eof     bool
	seen    map[string]bool
	pending []os.FileInfo
}

func (d *davDir) Read(p []byte) (int, error)     { return 0, io.EOF }
func (d *davDir) Write(p []byte) (int, error)    { return 0, fs.ErrPermission }
func (d *davDir) Seek(int64, int) (int64, error) { return 0, fs.ErrInvalid }
func (d *davDir) Close() error                   { return nil }
func (d *davDir) Stat() (os.FileInfo, error)     { return davDirInfo(d.prefix), nil }

func (d *davDir) Readdir(count int) ([]os.FileInfo, error) {
	if d.eof && len(d.pending) == 0 {
		return nil, io.EOF
	}
	// count <= 0 means "return all entries" (PROPFIND Depth:1 contract)
	// count > 0 means "return at most count entries"
	wantAll := count <= 0
	if wantAll {
		count = maxListPage // used per-page, loop reads until eof
	}
	var out []os.FileInfo
	if len(d.pending) > 0 {
		out = d.pending
		d.pending = nil
	}
	for (wantAll || len(out) < count) && !d.eof {
		ctx := d.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		entries, err := d.nextPage(ctx, d.prefix)
		if err != nil {
			return out, err
		}
		if len(entries) == 0 {
			d.eof = true
			break
		}
		out = append(out, entries...)
	}
	if !wantAll && len(out) > count {
		d.pending = out[count:]
		out = out[:count]
	}
	if len(out) == 0 {
		return nil, io.EOF
	}
	return out, nil
}

func (d *davDir) nextPage(ctx context.Context, prefix string) ([]os.FileInfo, error) {
	page, err := d.svc.List(ctx, d.tenant, service.DefaultBucket, prefix, d.cur, maxListPage)
	if err != nil {
		return nil, err
	}
	if d.seen == nil {
		d.seen = map[string]bool{}
	}
	var out []os.FileInfo
	for _, obj := range page.Objects {
		rel := strings.TrimPrefix(obj.Key, prefix)
		if idx := strings.Index(rel, "/"); idx >= 0 {
			name := rel[:idx]
			if !d.seen[name] {
				d.seen[name] = true
				out = append(out, davDirInfo(prefix+name))
			}
			continue
		}
		out = append(out, davFileInfo(obj))
	}
	if page.HasMore {
		d.cur = page.NextMarker
	} else {
		d.eof = true
	}
	return out, nil
}

// ── FileInfo adapters ────────────────────────────────────────────────────────

type davInfo struct {
	name string
	size int64
	mod  time.Time
	dir  bool
}

func (f *davInfo) Name() string { return f.name }
func (f *davInfo) Size() int64  { return f.size }
func (f *davInfo) Mode() os.FileMode {
	if f.dir {
		return os.ModeDir | 0555
	}
	return 0444
}
func (f *davInfo) ModTime() time.Time { return f.mod }
func (f *davInfo) IsDir() bool        { return f.dir }
func (f *davInfo) Sys() any           { return nil }

func davFileInfo(obj repository.Object) os.FileInfo {
	return &davInfo{
		name: path.Base(obj.Key),
		size: obj.Size,
		mod:  obj.UpdatedAt,
		dir:  false,
	}
}

func davDirInfo(name string) os.FileInfo {
	return &davInfo{name: path.Base(name), dir: true, mod: time.Now()}
}
