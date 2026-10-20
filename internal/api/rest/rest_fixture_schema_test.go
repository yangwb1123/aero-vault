package rest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// No I/O occurs until an actual fixture calls seed, inside the test timeout.
// Only closed, migrated schema bytes survive; no writable file/connection does.
var sharedRESTFixtureSchema restFixtureSchemaCache

type restFixtureSchemaCache struct {
	once  sync.Once
	image []byte
	err   error
}

func (c *restFixtureSchemaCache) seed(ctx context.Context, path string) error {
	c.once.Do(func() {
		c.image, c.err = buildRESTFixtureSchema(ctx, filepath.Dir(path))
	})
	if c.err != nil {
		return fmt.Errorf("build REST fixture schema: %w", c.err)
	}
	return writeRESTFixtureSchema(path, c.image)
}

func buildRESTFixtureSchema(ctx context.Context, parent string) (image []byte, err error) {
	dir, err := os.MkdirTemp(parent, "schema-")
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, os.RemoveAll(dir))
		if err != nil {
			image = nil
		}
	}()
	path := filepath.Join(dir, "schema.db")
	repo, err := repository.Open(ctx, "sqlite", "file:"+path)
	if err != nil {
		return nil, err
	}
	return captureRESTFixtureSchema(ctx, path, repo)
}

func captureRESTFixtureSchema(ctx context.Context, path string, repo repository.Repository) ([]byte, error) {
	migrateErr := repo.Migrate(ctx)
	closeErr := repo.Close()
	if err := errors.Join(migrateErr, closeErr); err != nil {
		return nil, err
	}
	if err := restFixtureSchemaSidecarsClosed(path); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

func restFixtureSchemaSidecarsClosed(path string) error {
	for _, suffix := range []string{"-wal", "-shm"} {
		_, err := os.Stat(path + suffix)
		if err == nil {
			return fmt.Errorf("REST fixture schema still has %s sidecar", suffix)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func writeRESTFixtureSchema(path string, image []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(image)
	err = errors.Join(writeErr, file.Sync(), file.Close())
	if err != nil {
		err = errors.Join(err, os.Remove(path))
	}
	return err
}
