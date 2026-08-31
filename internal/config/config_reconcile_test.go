package config

import (
	"strconv"
	"strings"
	"testing"
)

func TestValidateOrphanGraceMinutes(t *testing.T) {
	tests := []struct {
		name     string
		value    int
		interval int
		delete   bool
		wantErr  bool
	}{
		{"negative while disabled", -1, 0, false, true},
		{"negative with interval", -1, 1, false, true},
		{"negative with cleanup", -60, 0, true, true},
		{"negative while active", -60, 1, true, true},
		{"zero is valid", 0, 1, true, false},
		{"positive is valid", 60, 1, true, false},
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
					t.Fatal("negative orphan grace must be rejected")
				}
				if !strings.Contains(err.Error(), "RECONCILE_ORPHAN_GRACE_MINUTES") ||
					!strings.Contains(err.Error(), "must be >= 0") {
					t.Fatalf("error = %q, want variable name and non-negative requirement", err)
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
	tests := []struct {
		name        string
		value       string
		interval    int
		delete      bool
		want        int
		wantErr     bool
		wantErrText string
	}{
		{"negative disabled cleanup", "-1", 0, false, 0, true, "must be >= 0"},
		{"negative disabled reconciliation", "-1", 0, true, 0, true, "must be >= 0"},
		{"negative active cleanup off", "-1", 1, false, 0, true, "must be >= 0"},
		{"negative active cleanup on", "-1", 1, true, 0, true, "must be >= 0"},
		{"zero", "0", 1, true, 0, false, ""},
		{"positive", "90", 1, true, 90, false, ""},
		{"empty uses default", "", 0, false, 60, false, ""},
		{"malformed preserves parse error", "not-a-number", 0, false, 0, true, "not-a-number"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("RECONCILE_INTERVAL_MINUTES", strconv.Itoa(tc.interval))
			t.Setenv("RECONCILE_DELETE_ORPHAN_BLOBS", strconv.FormatBool(tc.delete))
			t.Setenv("RECONCILE_ORPHAN_GRACE_MINUTES", tc.value)

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
