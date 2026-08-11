//go:build race

package thumbnail

// raceEnabled reports whether the binary was built with -race. Allocation-
// delta tests (TotalAlloc) must skip under race: the detector's bookkeeping
// inflates the measured deltas (see large_transparent_allocation_test.go for
// the measured figures), which would collapse the discrimination margins.
const raceEnabled = true
