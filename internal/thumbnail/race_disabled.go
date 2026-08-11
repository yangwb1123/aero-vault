//go:build !race

package thumbnail

// raceEnabled reports whether the binary was built with -race (see
// race_enabled.go for the rationale).
const raceEnabled = false
