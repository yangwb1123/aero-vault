package service

import "testing"

func TestParseByteRange(t *testing.T) {
	const size = 100
	cases := []struct {
		hdr                   string
		wantOff, wantLen      int64
		wantOK, wantUnsatisfy bool
	}{
		{"bytes=0-49", 0, 50, true, false},
		{"bytes=10-19", 10, 10, true, false},
		{"bytes=90-", 90, 10, true, false},      // open-ended
		{"bytes=-20", 80, 20, true, false},      // suffix
		{"bytes=-200", 0, 100, true, false},     // suffix larger than object → whole
		{"bytes=50-1000", 50, 50, true, false},  // end clamped
		{"bytes=0-0", 0, 1, true, false},        // single byte
		{"bytes=100-200", 0, 0, false, true},    // start past end → unsatisfiable
		{"bytes=80-20", 0, 0, false, true},      // end < start → unsatisfiable
		{"", 0, 0, false, false},                // no header
		{"bytes=abc", 0, 0, false, false},       // garbage
		{"items=0-9", 0, 0, false, false},       // wrong unit
		{"bytes=0-9,20-29", 0, 10, true, false}, // multi-range → first only
	}
	for _, c := range cases {
		off, length, ok, unsat := ParseByteRange(c.hdr, size)
		if off != c.wantOff || length != c.wantLen || ok != c.wantOK || unsat != c.wantUnsatisfy {
			t.Errorf("ParseByteRange(%q,%d) = (%d,%d,%v,%v); want (%d,%d,%v,%v)",
				c.hdr, size, off, length, ok, unsat, c.wantOff, c.wantLen, c.wantOK, c.wantUnsatisfy)
		}
	}
}

func TestParseByteRangeEmptyObject(t *testing.T) {
	if _, _, _, unsat := ParseByteRange("bytes=-5", 0); !unsat {
		t.Fatalf("suffix range on empty object should be unsatisfiable")
	}
	if _, _, ok, _ := ParseByteRange("bytes=0-0", 0); ok {
		t.Fatalf("any range on empty object should not be ok")
	}
}
