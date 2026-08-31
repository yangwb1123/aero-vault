package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const startupValidationHelperEnv = "AERO_VAULT_STARTUP_VALIDATION_HELPER"

// TestStartupRejectsInvalidOrphanGraceBeforeInfrastructure exercises both
// entrypoints in a clean helper process. The helper uses a positive reconcile
// interval and enabled orphan cleanup so a future startup-order regression
// cannot hide behind disabled reconciliation.
func TestStartupRejectsInvalidOrphanGraceBeforeInfrastructure(t *testing.T) {
	if mode := os.Getenv(startupValidationHelperEnv); mode != "" {
		testStartupValidationHelper(t, mode)
		return
	}

	for _, mode := range []string{"server", "mcp"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			dbPath := filepath.Join(root, "aero.db")
			objectRoot := filepath.Join(root, "objects")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := startupValidationCommand(t, ctx)
			cmd.Dir = t.TempDir()
			cmd.Env = startupValidationEnv(mode, dbPath, objectRoot)

			output, err := cmd.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("%s startup helper timed out: %v\n%s", mode, ctx.Err(), output)
			}
			if err != nil {
				t.Fatalf("%s startup helper failed: %v\n%s", mode, err, output)
			}
			assertPathMissing(t, dbPath)
			assertPathMissing(t, objectRoot)
		})
	}
}

func startupValidationCommand(t *testing.T, ctx context.Context) *exec.Cmd {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test executable: %v", err)
	}
	return exec.CommandContext(ctx, executable,
		"-test.run", "^TestStartupRejectsInvalidOrphanGraceBeforeInfrastructure$",
		"-test.v")
}

func startupValidationEnv(mode, dbPath, objectRoot string) []string {
	return []string{
		startupValidationHelperEnv + "=" + mode,
		"APP_LOG_LEVEL=info",
		"APP_ADDR=127.0.0.1:0",
		"STORAGE_BACKEND=local",
		"STORAGE_LOCAL_ROOT=" + objectRoot,
		"DB_DRIVER=sqlite",
		"DB_DSN=file:" + dbPath,
		"RECONCILE_INTERVAL_MINUTES=1",
		"RECONCILE_DELETE_ORPHAN_BLOBS=true",
		"RECONCILE_ORPHAN_GRACE_MINUTES=-1",
		"JOBS_WORKERS=0",
		"EVENT_OUTBOX_ENABLED=false",
	}
}

func testStartupValidationHelper(t *testing.T, mode string) {
	t.Helper()
	var err error
	switch mode {
	case "server":
		err = run()
	case "mcp":
		err = runMCP()
	default:
		t.Fatalf("unknown startup helper mode %q", mode)
	}
	if err == nil {
		t.Fatal("invalid orphan grace unexpectedly allowed startup")
	}
	if !strings.Contains(err.Error(), "RECONCILE_ORPHAN_GRACE_MINUTES") ||
		!strings.Contains(err.Error(), "must be >= 0") {
		t.Fatalf("startup error = %q, want orphan-grace validation", err)
	}
}

func assertPathMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("path %q exists after rejected startup (stat error: %v)", path, err)
	}
}
