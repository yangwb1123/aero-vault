package config

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestValidateOrphanGraceMinutes(t *testing.T) {
	maxGrace := int(maxOrphanGraceMinutes)
	tests := []struct {
		name        string
		value       int
		interval    int
		delete      bool
		wantErr     bool
		wantErrText string
	}{
		{"negative while disabled", -1, 0, false, true, "must be >= 0"},
		{"negative with interval", -1, 1, false, true, "must be >= 0"},
		{"negative with cleanup", -60, 0, true, true, "must be >= 0"},
		{"negative while active", -60, 1, true, true, "must be >= 0"},
		{"zero is valid", 0, 1, true, false, ""},
		{"one minute is valid", 1, 1, true, false, ""},
		{"positive is valid", 60, 1, true, false, ""},
		{"maximum duration-safe value", maxGrace, 1, true, false, ""},
		{"above duration-safe maximum", maxGrace + 1, 1, true, true, "must be <="},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseValid()
			cfg.Reconcile.OrphanGraceMinutes = tc.value
			cfg.Reconcile.IntervalMinutes = tc.interval
			cfg.Reconcile.DeleteOrphanBlobs = tc.delete

			err := cfg.Validate()
			if tc.wantErr {
				if err == nil {
					t.Fatal("invalid orphan grace must be rejected")
				}
				if !strings.Contains(err.Error(), "RECONCILE_ORPHAN_GRACE_MINUTES") ||
					!strings.Contains(err.Error(), tc.wantErrText) {
					t.Fatalf("error = %q, want variable name and %q", err, tc.wantErrText)
				}
				return
			}
			if err != nil {
				t.Fatalf("valid orphan grace rejected: %v", err)
			}
		})
	}
}

func TestLoadOrphanGraceMinutes(t *testing.T) {
	t.Chdir(t.TempDir())
	maxGrace := strconv.FormatInt(maxOrphanGraceMinutes, 10)
	overMaxGrace := strconv.FormatInt(maxOrphanGraceMinutes+1, 10)
	tests := []struct {
		name        string
		value       string
		unset       bool
		interval    int
		delete      bool
		want        int
		wantErr     bool
		wantErrText string
	}{
		{"negative disabled cleanup", "-1", false, 0, false, 0, true, "must be >= 0"},
		{"negative disabled reconciliation", "-1", false, 0, true, 0, true, "must be >= 0"},
		{"negative active cleanup off", "-1", false, 1, false, 0, true, "must be >= 0"},
		{"negative active cleanup on", "-1", false, 1, true, 0, true, "must be >= 0"},
		{"zero", "0", false, 1, true, 0, false, ""},
		{"one minute", "1", false, 1, true, 1, false, ""},
		{"positive", "90", false, 1, true, 90, false, ""},
		{"maximum duration-safe value", maxGrace, false, 1, true, int(maxOrphanGraceMinutes), false, ""},
		{"above duration-safe maximum", overMaxGrace, false, 1, true, 0, true, "must be <="},
		{"empty uses default", "", false, 0, false, 60, false, ""},
		{"unset uses default", "", true, 0, false, 60, false, ""},
		{"malformed preserves parse error", "not-a-number", false, 0, false, 0, true, "not-a-number"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("RECONCILE_INTERVAL_MINUTES", strconv.Itoa(tc.interval))
			t.Setenv("RECONCILE_DELETE_ORPHAN_BLOBS", strconv.FormatBool(tc.delete))
			if tc.unset {
				if err := os.Unsetenv("RECONCILE_ORPHAN_GRACE_MINUTES"); err != nil {
					t.Fatalf("unset orphan grace: %v", err)
				}
			} else {
				t.Setenv("RECONCILE_ORPHAN_GRACE_MINUTES", tc.value)
			}

			cfg, err := Load()
			if tc.wantErr {
				if err == nil {
					t.Fatal("Load should reject invalid orphan grace")
				}
				if cfg != nil {
					t.Fatal("Load should not return a config after validation failure")
				}
				if !strings.Contains(err.Error(), "RECONCILE_ORPHAN_GRACE_MINUTES") ||
					!strings.Contains(err.Error(), tc.wantErrText) {
					t.Fatalf("error = %q, want target variable and %q", err, tc.wantErrText)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load failed: %v", err)
			}
			if cfg.Reconcile.OrphanGraceMinutes != tc.want {
				t.Fatalf("OrphanGraceMinutes = %d, want %d", cfg.Reconcile.OrphanGraceMinutes, tc.want)
			}
		})
	}
}

func TestLoadOrphanGraceIsolatedFromUnrelatedEnvironment(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("AI_EMBED_DIM", "not-an-integer")
	clearEnv(t)
	t.Setenv("RECONCILE_ORPHAN_GRACE_MINUTES", "-1")

	cfg, err := Load()
	if cfg != nil {
		t.Fatal("Load should not return a config after validation failure")
	}
	if err == nil || !strings.Contains(err.Error(), "RECONCILE_ORPHAN_GRACE_MINUTES") {
		t.Fatalf("Load error = %v, want orphan grace validation after clearing host values", err)
	}
}
