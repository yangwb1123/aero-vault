// Package snapshot packs the database + object storage into a single tar.gz
// for backup/restore. It is intended for SQLite + local-FS development
// instances and small production deployments. For large Postgres+S3 stacks,
// fall back to pg_dump + s3 lifecycle copies.
package snapshot

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Create writes a tar.gz to outPath containing:
//
//	./manifest.json
//	./db/aero.db (+ -wal, -shm)
//	./objects/...
//
// dbPath is the SQLite DSN path (`file:./var/aero.db?...` is parsed).
// objectsRoot is the local-FS storage root.
func Create(outPath, dbPath, objectsRoot string) error {
	dbFile := dbFileFromDSN(dbPath)
	if dbFile == "" {
		return errors.New("snapshot: cannot derive sqlite file from DSN; only sqlite local snapshots are supported")
	}
	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	// DB files: the main database is mandatory — a snapshot without it is a silent
	// empty backup. The WAL/SHM sidecars are optional (may be checkpointed away).
	if _, err := os.Stat(dbFile); err != nil {
		return fmt.Errorf("snapshot: database file %q not found: %w", dbFile, err)
	}
	if err := addFile(tw, dbFile, "db/"+filepath.Base(dbFile)); err != nil {
		return err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		path := dbFile + suffix
		if _, err := os.Stat(path); err != nil {
			continue
		}
		if err := addFile(tw, path, "db/"+filepath.Base(path)); err != nil {
			return err
		}
	}
	// Object root
	err = filepath.Walk(objectsRoot, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(objectsRoot, path)
		if err != nil {
			return err
		}
		return addFile(tw, path, "objects/"+filepath.ToSlash(rel))
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Restore unpacks a snapshot into dbPath/objectsRoot. Existing files are
// overwritten — caller is responsible for any backup.
func Restore(snapPath, dbPath, objectsRoot string) error {
	dbFile := dbFileFromDSN(dbPath)
	if dbFile == "" {
		return errors.New("snapshot: cannot derive sqlite file from DSN")
	}
	f, err := os.Open(snapPath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		switch {
		case strings.HasPrefix(hdr.Name, "db/"):
			base := strings.TrimPrefix(hdr.Name, "db/")
			out := filepath.Join(filepath.Dir(dbFile), base)
			if err := writeOut(tr, out); err != nil {
				return err
			}
		case strings.HasPrefix(hdr.Name, "objects/"):
			rel := strings.TrimPrefix(hdr.Name, "objects/")
			out := filepath.Join(objectsRoot, filepath.FromSlash(rel))
			if err := writeOut(tr, out); err != nil {
				return err
			}
		}
	}
	return nil
}

func addFile(tw *tar.Writer, fsPath, tarName string) error {
	st, err := os.Stat(fsPath)
	if err != nil {
		return err
	}
	hdr := &tar.Header{
		Name:    tarName,
		Size:    st.Size(),
		Mode:    int64(st.Mode().Perm()),
		ModTime: st.ModTime(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	f, err := os.Open(fsPath)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(tw, f)
	return err
}

func writeOut(r io.Reader, out string) error {
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, r)
	return err
}

// dbFileFromDSN extracts `./var/aero.db` from `file:./var/aero.db?_pragma=…`.
func dbFileFromDSN(dsn string) string {
	dsn = strings.TrimPrefix(dsn, "file:")
	if i := strings.Index(dsn, "?"); i >= 0 {
		dsn = dsn[:i]
	}
	return dsn
}

// FormatBytes is exported for the CLI to print compact size strings.
func FormatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
