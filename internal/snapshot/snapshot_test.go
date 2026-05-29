package snapshot

import (
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
