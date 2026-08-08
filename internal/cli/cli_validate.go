package cli

import (
	"fmt"
	"math"
	"strconv"
)

// Full-string numeric parsing + non-negative gate for CLI arguments that are
// written into server configuration (tenant/bucket quota, budget, lifecycle
// days, search k). fmt.Sscanf was previously used and silently coerced garbage
// input to 0 — e.g. `quota t abc xyz` removed a production tenant's quota.
// These helpers reject non-numeric input (including trailing garbage, base
// prefixes, NaN/Inf — all of which Sscanf accepts with err == nil or partial
// scans) and negative values. Zero remains legal: 0 = unlimited/clear per
// server contracts (file_crud.go unlimited gate, admin.go clear-override).
// Error messages carry the parameter role name, the offending value, and the
// expected format (spec FR-1 three-element contract).

// requireNonNegInt64 parses s as a full-string decimal int64 and rejects
// negative values.
func requireNonNegInt64(name, s string) (int64, error) {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("invalid %s %q: expected a non-negative integer", name, s)
	}
	return v, nil
}

// requireNonNegFloat parses s as a full-string float64 and rejects negative
// values, NaN, and ±Inf (strconv.ParseFloat accepts NaN/Inf with err == nil,
// and NaN < 0 is false — an explicit IsNaN/IsInf check is required).
func requireNonNegFloat(name, s string) (float64, error) {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0, fmt.Errorf("invalid %s %q: expected a non-negative number", name, s)
	}
	return v, nil
}

// requireNonNegInt parses s as a full-string decimal int and rejects negative
// values.
func requireNonNegInt(name, s string) (int, error) {
	v, err := strconv.Atoi(s)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("invalid %s %q: expected a non-negative integer", name, s)
	}
	return v, nil
}
