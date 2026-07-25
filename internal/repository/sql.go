package repository

import (
	"context"
	crand "crypto/rand"
	"database/sql"
	"embed"
	"encoding/binary"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"
)

var (
	cryptoRandRead  = crand.Read
	binaryPutUint64 = binary.LittleEndian.PutUint64
)

//go:embed migrations/postgres/*.sql migrations/sqlite/*.sql
var migrationsFS embed.FS

type dialect int

const (
	dialectSQLite dialect = iota
	dialectPostgres
)

type sqlStore struct {
	db      *sql.DB
	dialect dialect
}

func (s *sqlStore) Close() error                   { return s.db.Close() }
func (s *sqlStore) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// rebind converts $N placeholders into ? for SQLite.
func (s *sqlStore) rebind(q string) string {
	if s.dialect == dialectPostgres {
		return q
	}
	var b strings.Builder
	b.Grow(len(q))
	for i := 0; i < len(q); i++ {
		if q[i] != '$' {
			b.WriteByte(q[i])
			continue
		}
		j := i + 1
		for j < len(q) && q[j] >= '0' && q[j] <= '9' {
			j++
		}
		if j == i+1 {
			b.WriteByte(q[i])
			continue
		}
		b.WriteByte('?')
		i = j - 1
	}
	return b.String()
}

func (s *sqlStore) Migrate(ctx context.Context) error {
	dir := "migrations/postgres"
	if s.dialect == dialectSQLite {
		dir = "migrations/sqlite"
	}
	files, err := listMigrationFiles(dir)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT '')`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	applied, err := s.loadAppliedVersions(ctx)
	if err != nil {
		return err
	}
	for _, f := range files {
		if applied[f.version] {
			continue
		}
		if err := s.applyMigration(ctx, f); err != nil {
			return err
		}
	}
	return nil
}

func (s *sqlStore) loadAppliedVersions(ctx context.Context) (map[string]bool, error) {
	applied := map[string]bool{}
	rows, err := s.db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			_ = rows.Close()
			return nil, err
		}
		applied[v] = true
	}
	_ = rows.Close()
	return applied, nil
}

func (s *sqlStore) applyMigration(ctx context.Context, f migrationFile) error {
	body, err := fs.ReadFile(migrationsFS, f.path)
	if err != nil {
		return fmt.Errorf("read %s: %w", f.path, err)
	}
	if _, err := s.db.ExecContext(ctx, string(body)); err != nil {
		return fmt.Errorf("apply %s: %w", f.version, err)
	}
	if _, err := s.db.ExecContext(ctx, s.rebind(`INSERT INTO schema_migrations (version, applied_at) VALUES ($1, $2)`), f.version, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("record %s: %w", f.version, err)
	}
	return nil
}

type migrationFile struct {
	version string
	path    string
}

func listMigrationFiles(dir string) ([]migrationFile, error) {
	entries, err := fs.ReadDir(migrationsFS, dir)
	if err != nil {
		return nil, err
	}
	var ups []migrationFile
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		ups = append(ups, migrationFile{
			version: strings.TrimSuffix(name, ".up.sql"),
			path:    path.Join(dir, name),
		})
	}
	sort.Slice(ups, func(i, j int) bool { return ups[i].version < ups[j].version })
	return ups, nil
}
