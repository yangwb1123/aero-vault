package snapshot

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestDBFileFromDSN(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"file:./var/aero.db?_pragma=foreign_keys(1)", "./var/aero.db"},
		{"file:/tmp/x.db", "/tmp/x.db"},
		{"file:aero.db", "aero.db"},
		{"/abs/p.db", "/abs/p.db"}, // no file: prefix is left as-is
		{"file:./db.sqlite?a=1&b=2", "./db.sqlite"},
		{"file:", ""}, // empty after stripping prefix
		{"", ""},
	}
	for _, c := range cases {
		if got := dbFileFromDSN(c.in); got != c.want {
			t.Errorf("dbFileFromDSN(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1023, "1023 B"},
		{1024, "1.00 KiB"},
		{1536, "1.50 KiB"},
		{1024 * 1024, "1.00 MiB"},
		{5 * 1024 * 1024, "5.00 MiB"},
		{1024 * 1024 * 1024, "1.00 GiB"},
		{1024 * 1024 * 1024 * 1024, "1.00 TiB"},
	}
	for _, c := range cases {
		if got := FormatBytes(c.in); got != c.want {
			t.Errorf("FormatBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCreate_BadDSNErrors(t *testing.T) {
	out := filepath.Join(t.TempDir(), "snap.tar.gz")
	if err := Create(out, "file:", t.TempDir()); err == nil {
		t.Fatal("Create with underivable DSN should error")
	}
}

func TestRestore_BadDSNErrors(t *testing.T) {
	// Build a minimal valid archive first so the failure is clearly the DSN.
	dir := t.TempDir()
	snap := filepath.Join(dir, "snap.tar.gz")
	dbDir := t.TempDir()
	writeFile(t, filepath.Join(dbDir, "aero.db"), []byte("x"))
	if err := Create(snap, "file:"+filepath.Join(dbDir, "aero.db"), t.TempDir()); err != nil {
		t.Fatalf("setup Create: %v", err)
	}
	if err := Restore(snap, "file:", t.TempDir()); err == nil {
		t.Fatal("Restore with underivable DSN should error")
	}
}

// A snapshot whose main database file is missing must FAIL, not silently produce
// an archive with no database (which would be an undetected empty backup).
func TestCreate_MissingMainDBFails(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "snap.tar.gz")
	if err := Create(out, "file:"+filepath.Join(dir, "missing.db"), dir); err == nil {
		t.Fatal("Create must fail when the main database file is missing")
	}
}

func TestCreate_MissingObjectsRootIsOK(t *testing.T) {
	// objectsRoot that does not exist must not fail Create (filepath.Walk's
	// ErrNotExist is swallowed); the DB file should still be archived.
	dir := t.TempDir()
	dbDir := t.TempDir()
	writeFile(t, filepath.Join(dbDir, "aero.db"), []byte("dbcontent"))

	out := filepath.Join(dir, "snap.tar.gz")
	missingObjs := filepath.Join(dir, "does-not-exist")
	if err := Create(out, "file:"+filepath.Join(dbDir, "aero.db"), missingObjs); err != nil {
		t.Fatalf("Create with missing objects root should succeed, got: %v", err)
	}
	if fi, err := os.Stat(out); err != nil || fi.Size() == 0 {
		t.Fatalf("snapshot file not written: err=%v", err)
	}
}

// TestRoundTrip is the core test: snapshot a DB (+WAL/SHM sidecars) and an
// objects tree, restore into fresh directories, and verify byte-for-byte.
func TestRoundTrip(t *testing.T) {
	srcDB := t.TempDir()
	srcObjs := t.TempDir()

	dbFile := filepath.Join(srcDB, "aero.db")
	writeFile(t, dbFile, []byte("SQLite format 3\x00main-db-bytes"))
	writeFile(t, dbFile+"-wal", []byte("wal-journal-data"))
	writeFile(t, dbFile+"-shm", []byte("shared-mem"))

	// A nested objects tree with a couple of files.
	objFiles := map[string][]byte{
		"tenantA/bucket1/photo.jpg":      []byte("\xff\xd8\xff binary jpeg-ish"),
		"tenantA/bucket1/nested/doc.txt": []byte("hello world"),
		"tenantB/empty.bin":              {}, // zero-byte file
	}
	for rel, data := range objFiles {
		writeFile(t, filepath.Join(srcObjs, filepath.FromSlash(rel)), data)
	}

	snap := filepath.Join(t.TempDir(), "snap.tar.gz")
	dsn := "file:" + dbFile + "?_pragma=foreign_keys(1)"
	if err := Create(snap, dsn, srcObjs); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Restore into fresh dirs. Use a destination DB file with the SAME basename
	// (aero.db) so sidecars line up; the directory differs.
	dstDB := t.TempDir()
	dstObjs := t.TempDir()
	dstDBFile := filepath.Join(dstDB, "aero.db")
	if err := Restore(snap, "file:"+dstDBFile, dstObjs); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// DB + sidecars restored correctly.
	assertFileEquals(t, dstDBFile, []byte("SQLite format 3\x00main-db-bytes"))
	assertFileEquals(t, dstDBFile+"-wal", []byte("wal-journal-data"))
	assertFileEquals(t, dstDBFile+"-shm", []byte("shared-mem"))

	// Objects restored correctly, preserving the nested layout.
	for rel, data := range objFiles {
		assertFileEquals(t, filepath.Join(dstObjs, filepath.FromSlash(rel)), data)
	}
}

func TestRoundTrip_OnlyDBNoSidecars(t *testing.T) {
	srcDB := t.TempDir()
	dbFile := filepath.Join(srcDB, "aero.db")
	writeFile(t, dbFile, []byte("just-the-db"))

	srcObjs := t.TempDir() // empty objects dir

	snap := filepath.Join(t.TempDir(), "s.tar.gz")
	if err := Create(snap, "file:"+dbFile, srcObjs); err != nil {
		t.Fatalf("Create: %v", err)
	}

	dstDB := t.TempDir()
	dstObjs := t.TempDir()
	dstDBFile := filepath.Join(dstDB, "aero.db")
	if err := Restore(snap, "file:"+dstDBFile, dstObjs); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	assertFileEquals(t, dstDBFile, []byte("just-the-db"))

	// No sidecars should have been created.
	if _, err := os.Stat(dstDBFile + "-wal"); !os.IsNotExist(err) {
		t.Fatalf("unexpected -wal file restored (err=%v)", err)
	}
}

func TestRestore_OverwritesExisting(t *testing.T) {
	srcDB := t.TempDir()
	dbFile := filepath.Join(srcDB, "aero.db")
	writeFile(t, dbFile, []byte("new-contents"))
	srcObjs := t.TempDir()
	writeFile(t, filepath.Join(srcObjs, "k.txt"), []byte("fresh"))

	snap := filepath.Join(t.TempDir(), "s.tar.gz")
	if err := Create(snap, "file:"+dbFile, srcObjs); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Pre-populate destination with stale data that must be overwritten.
	dstDB := t.TempDir()
	dstObjs := t.TempDir()
	dstDBFile := filepath.Join(dstDB, "aero.db")
	writeFile(t, dstDBFile, []byte("STALE-DB-SHOULD-BE-REPLACED"))
	writeFile(t, filepath.Join(dstObjs, "k.txt"), []byte("STALE-OBJECT"))

	if err := Restore(snap, "file:"+dstDBFile, dstObjs); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	assertFileEquals(t, dstDBFile, []byte("new-contents"))
	assertFileEquals(t, filepath.Join(dstObjs, "k.txt"), []byte("fresh"))
}

func TestRestoreRejectsUnsafeArchiveEntries(t *testing.T) {
	tests := []snapshotTestEntry{
		{name: "objects/../../escape.txt", body: "escape"},
		{name: `objects\..\escape.txt`, body: "escape"},
		{name: "/objects/absolute.txt", body: "escape"},
		{name: "objects/link", typeflag: tar.TypeSymlink, linkname: "../../outside"},
		{name: "db/nested/aero.db", body: "escape"},
		{name: "unexpected.txt", body: "escape"},
	}
	for _, malicious := range tests {
		t.Run(malicious.name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "malicious.tar.gz")
			writeSnapshotEntries(t, archive, []snapshotTestEntry{
				{name: "db/aero.db", body: "database"}, malicious,
			})
			destination := t.TempDir()
			dbFile := filepath.Join(destination, "aero.db")
			if err := Restore(archive, "file:"+dbFile, filepath.Join(destination, "objects")); err == nil {
				t.Fatal("Restore accepted unsafe snapshot entry")
			}
			if _, err := os.Stat(dbFile); !os.IsNotExist(err) {
				t.Fatalf("validation failure wrote database: %v", err)
			}
		})
	}
}

func TestRestoreRejectsMissingDatabaseAndDuplicateSidecar(t *testing.T) {
	for name, entries := range map[string][]snapshotTestEntry{
		"missing database": {{name: "objects/file.txt", body: "file"}},
		"duplicate wal": {
			{name: "db/aero.db", body: "db"},
			{name: "db/aero.db-wal", body: "one"},
			{name: "db/other.db-wal", body: "two"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "invalid.tar.gz")
			writeSnapshotEntries(t, archive, entries)
			if err := Restore(archive, "file:"+filepath.Join(t.TempDir(), "db.sqlite"), t.TempDir()); err == nil {
				t.Fatal("Restore accepted structurally invalid snapshot")
			}
		})
	}
}

func TestRestoreMapsDatabaseToRequestedBasename(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "renamed.tar.gz")
	writeSnapshotEntries(t, archive, []snapshotTestEntry{{name: "db/source.db", body: "database"}})
	destination := t.TempDir()
	target := filepath.Join(destination, "renamed.db")
	if err := Restore(archive, "file:"+target, filepath.Join(destination, "objects")); err != nil {
		t.Fatal(err)
	}
	assertFileEquals(t, target, []byte("database"))
}

func TestRestoreRejectsTruncatedArchiveBeforeWriting(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "truncated.tar.gz")
	writeSnapshotEntries(t, archive, []snapshotTestEntry{
		{name: "db/aero.db", body: "database-content"},
		{name: "objects/file.txt", body: "object-content"},
	})
	payload, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archive, payload[:len(payload)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	target := filepath.Join(destination, "aero.db")
	if err := Restore(archive, "file:"+target, filepath.Join(destination, "objects")); err == nil {
		t.Fatal("Restore accepted a truncated archive")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("truncated archive wrote database: %v", err)
	}
}

func TestRestoreDoesNotFollowEscapingDestinationSymlink(t *testing.T) {
	sourceDB, sourceObjects := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(sourceDB, "aero.db"), []byte("database"))
	writeFile(t, filepath.Join(sourceObjects, "linked", "outside.txt"), []byte("object"))
	archive := filepath.Join(t.TempDir(), "snapshot.tar.gz")
	if err := Create(archive, "file:"+filepath.Join(sourceDB, "aero.db"), sourceObjects); err != nil {
		t.Fatal(err)
	}
	destination, outside := t.TempDir(), t.TempDir()
	objects := filepath.Join(destination, "objects")
	if err := os.MkdirAll(objects, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(objects, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := Restore(archive, "file:"+filepath.Join(destination, "aero.db"), objects); err == nil {
		t.Fatal("Restore followed a destination symlink outside the object root")
	}
	if _, err := os.Stat(filepath.Join(outside, "outside.txt")); !os.IsNotExist(err) {
		t.Fatalf("restore wrote outside object root: %v", err)
	}
}

type snapshotTestEntry struct {
	name, body, linkname string
	typeflag             byte
}

func writeSnapshotEntries(t *testing.T, destination string, entries []snapshotTestEntry) {
	t.Helper()
	file, err := os.Create(destination)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		typeflag := entry.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		header := &tar.Header{
			Name: entry.name, Mode: 0o600, Size: int64(len(entry.body)),
			Typeflag: typeflag, Linkname: entry.linkname,
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if entry.body != "" {
			if _, err := tarWriter.Write([]byte(entry.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

// --- helpers ---

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertFileEquals(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != string(want) {
		t.Fatalf("file %s = %q, want %q", path, got, want)
	}
}
