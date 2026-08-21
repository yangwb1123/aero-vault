package snapshot

import (
	"archive/tar"
	"compress/gzip"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	_ "modernc.org/sqlite"
)

func TestCreate_InvalidDatabaseFails(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "bad.db")
	writeFile(t, dbPath, []byte("not sqlite"))
	err := Create(filepath.Join(dir, "snapshot.tar.gz"), "file:"+dbPath, dir)
	if err == nil {
		t.Fatal("Create accepted an invalid SQLite database")
	}
}

func TestCreate_LiveWALDB_ConcurrentWriter_Consistent(t *testing.T) {
	sourceDir, objectsDir := t.TempDir(), t.TempDir()
	dbPath := filepath.Join(sourceDir, "aero.db")
	dsn := "file:" + dbPath + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open source sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE records (id INTEGER PRIMARY KEY, payload TEXT NOT NULL)`); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO records(payload) VALUES ('seed')`); err != nil {
		t.Fatalf("insert seed: %v", err)
	}

	stop := make(chan struct{})
	started := make(chan struct{})
	var committed atomic.Int64
	var writerErr error
	var writerWG sync.WaitGroup
	writerWG.Add(1)
	go func() {
		defer writerWG.Done()
		for i := int64(0); ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := db.Exec(`INSERT INTO records(payload) VALUES (?)`, i); err != nil {
				writerErr = err
				return
			}
			count := committed.Add(1)
			if count == 1 {
				close(started)
			}
		}
	}()
	<-started
	committedBefore := committed.Load()
	snapshotPath := filepath.Join(t.TempDir(), "snapshot.tar.gz")
	createErr := Create(snapshotPath, dsn, objectsDir)
	close(stop)
	writerWG.Wait()
	if writerErr != nil {
		t.Fatalf("concurrent writer: %v", writerErr)
	}
	if createErr != nil {
		t.Fatalf("Create with live WAL database: %v", createErr)
	}

	destination := t.TempDir()
	restored := filepath.Join(destination, "aero.db")
	if err := Restore(snapshotPath, "file:"+restored, filepath.Join(destination, "objects")); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	assertSQLiteDB(t, restored)
	if got := sqliteRecordCount(t, restored); int64(got) < committedBefore+1 {
		t.Fatalf("restored rows = %d, want at least %d", got, committedBefore+1)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(restored + suffix); !os.IsNotExist(err) {
			t.Fatalf("restored snapshot unexpectedly created %s (err=%v)", suffix, err)
		}
	}
}

func TestCreate_ArchiveHasNoSidecarEntries(t *testing.T) {
	dir, objects := t.TempDir(), t.TempDir()
	dbPath := filepath.Join(dir, "aero.db")
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE records (id INTEGER PRIMARY KEY, payload TEXT)`); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO records(payload) VALUES ('archive')`); err != nil {
		t.Fatalf("insert row: %v", err)
	}
	snapshotPath := filepath.Join(t.TempDir(), "snapshot.tar.gz")
	if err := Create(snapshotPath, "file:"+dbPath+"?_pragma=journal_mode(WAL)", objects); err != nil {
		db.Close()
		t.Fatalf("Create: %v", err)
	}
	db.Close()

	file, err := os.Open(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	mainCount := 0
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(header.Name, "-wal") || strings.HasSuffix(header.Name, "-shm") {
			t.Fatalf("snapshot contains sidecar entry %q", header.Name)
		}
		if kind, _, err := classifySnapshotEntry(header); err != nil {
			t.Fatal(err)
		} else if kind == entryDBMain {
			mainCount++
			body, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(string(body), "SQLite format 3\x00") {
				t.Fatalf("database entry is not SQLite: %q", body[:min(16, len(body))])
			}
		}
	}
	if mainCount != 1 {
		t.Fatalf("database entries = %d, want exactly one", mainCount)
	}
}

func TestCreate_TrailerWriteError_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "aero.db")
	createSQLiteDB(t, dbPath)
	var count snapshotCountingWriter
	if err := createArchive(&count, dbPath, t.TempDir()); err != nil {
		t.Fatalf("baseline createArchive: %v", err)
	}
	if count.n < 2 {
		t.Fatalf("baseline archive length = %d, too small for trailer test", count.n)
	}
	failing := &snapshotFailingWriter{remaining: count.n - 1}
	if err := createArchive(failing, dbPath, t.TempDir()); err == nil {
		t.Fatal("createArchive with trailer write failure returned nil")
	}
}

type snapshotCountingWriter struct{ n int64 }

func (w *snapshotCountingWriter) Write(p []byte) (int, error) {
	w.n += int64(len(p))
	return len(p), nil
}

type snapshotFailingWriter struct{ remaining int64 }

func (w *snapshotFailingWriter) Write(p []byte) (int, error) {
	if int64(len(p)) <= w.remaining {
		w.remaining -= int64(len(p))
		return len(p), nil
	}
	if w.remaining > 0 {
		n := int(w.remaining)
		w.remaining = 0
		return n, io.ErrShortWrite
	}
	return 0, io.ErrShortWrite
}

func sqliteRecordCount(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open restored database: %v", err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM records`).Scan(&count); err != nil {
		t.Fatalf("count restored records: %v", err)
	}
	return count
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
