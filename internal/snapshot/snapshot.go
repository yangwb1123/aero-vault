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
	"path"
	"path/filepath"
	"strings"
)

// Create writes a tar.gz to outPath containing:
//
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

	if err := addDBFiles(tw, dbFile); err != nil {
		return err
	}
	if err := addObjectFiles(tw, objectsRoot); err != nil {
		return err
	}
	return nil
}

func addDBFiles(tw *tar.Writer, dbFile string) error {
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
	return nil
}

func addObjectFiles(tw *tar.Writer, objectsRoot string) error {
	err := filepath.Walk(objectsRoot, func(path string, info os.FileInfo, walkErr error) error {
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
	if err := validateSnapshot(snapPath); err != nil {
		return err
	}
	return unpackSnapshot(snapPath, dbFile, objectsRoot)
}

func validateSnapshot(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	seen := map[string]bool{}
	seenDatabaseKinds := map[snapshotEntry]bool{}
	for {
		header, nextErr := reader.Next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			return nextErr
		}
		kind, _, classifyErr := classifySnapshotEntry(header)
		if classifyErr != nil {
			return classifyErr
		}
		if seen[header.Name] {
			return fmt.Errorf("snapshot: duplicate entry %q", header.Name)
		}
		seen[header.Name] = true
		if kind != entryObject {
			if seenDatabaseKinds[kind] {
				return fmt.Errorf("snapshot: duplicate database component %q", header.Name)
			}
			seenDatabaseKinds[kind] = true
		}
		if _, err := io.Copy(io.Discard, reader); err != nil {
			return err
		}
	}
	if !seenDatabaseKinds[entryDBMain] {
		return errors.New("snapshot: main database entry is missing")
	}
	return nil
}

func unpackSnapshot(src, dbFile, objectsRoot string) error {
	if err := os.MkdirAll(filepath.Dir(dbFile), 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(objectsRoot, 0o700); err != nil {
		return err
	}
	dbRoot, err := os.OpenRoot(filepath.Dir(dbFile))
	if err != nil {
		return err
	}
	defer dbRoot.Close()
	objectRoot, err := os.OpenRoot(objectsRoot)
	if err != nil {
		return err
	}
	defer objectRoot.Close()
	return unpackToRoots(src, filepath.Base(dbFile), dbRoot, objectRoot)
}

func unpackToRoots(src, dbName string, dbRoot, objectRoot *os.Root) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		kind, relative, err := classifySnapshotEntry(header)
		if err != nil {
			return err
		}
		switch kind {
		case entryDBMain:
			err = writeRootFile(dbRoot, dbName, reader)
		case entryDBWAL:
			err = writeRootFile(dbRoot, dbName+"-wal", reader)
		case entryDBSHM:
			err = writeRootFile(dbRoot, dbName+"-shm", reader)
		case entryObject:
			err = writeRootFile(objectRoot, filepath.FromSlash(relative), reader)
		}
		if err != nil {
			return err
		}
	}
}

type snapshotEntry uint8

const (
	entryDBMain snapshotEntry = iota
	entryDBWAL
	entryDBSHM
	entryObject
)

func classifySnapshotEntry(header *tar.Header) (snapshotEntry, string, error) {
	if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
		return 0, "", fmt.Errorf("snapshot: unsupported entry type for %q", header.Name)
	}
	name := header.Name
	if name == "" || strings.Contains(name, `\`) || path.IsAbs(name) || path.Clean(name) != name {
		return 0, "", fmt.Errorf("snapshot: unsafe entry name %q", name)
	}
	if relative, ok := strings.CutPrefix(name, "objects/"); ok {
		if relative == "" || relative == "." || strings.HasPrefix(relative, "../") {
			return 0, "", fmt.Errorf("snapshot: unsafe object entry %q", name)
		}
		return entryObject, relative, nil
	}
	relative, ok := strings.CutPrefix(name, "db/")
	if !ok || relative == "" || strings.Contains(relative, "/") {
		return 0, "", fmt.Errorf("snapshot: unexpected entry %q", name)
	}
	if strings.HasSuffix(relative, "-wal") {
		return entryDBWAL, relative, nil
	}
	if strings.HasSuffix(relative, "-shm") {
		return entryDBSHM, relative, nil
	}
	return entryDBMain, relative, nil
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

func writeRootFile(root *os.Root, name string, reader io.Reader) error {
	if name == "" || filepath.IsAbs(name) {
		return fmt.Errorf("snapshot: unsafe output path %q", name)
	}
	if dir := filepath.Dir(name); dir != "." {
		if err := root.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	file, err := root.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(file, reader); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
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
