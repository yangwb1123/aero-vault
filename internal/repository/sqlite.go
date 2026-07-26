package repository

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

func openSQLite(ctx context.Context, dsn string) (Repository, error) {
	if p := sqliteFilePath(dsn); p != "" {
		if dir := filepath.Dir(p); dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("create sqlite dir: %w", err)
			}
		}
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1) // serialize writes to avoid SQLITE_BUSY
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = ON; PRAGMA journal_mode = WAL;`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set sqlite pragmas: %w", err)
	}
	return newSQLStore(db, dialectSQLite), nil
}

// sqliteFilePath extracts the on-disk path from a "file:..." DSN, "" if not a file URL.
func sqliteFilePath(dsn string) string {
	rest, ok := strings.CutPrefix(dsn, "file:")
	if !ok {
		return ""
	}
	if i := strings.IndexAny(rest, "?#"); i >= 0 {
		rest = rest[:i]
	}
	return rest
}
