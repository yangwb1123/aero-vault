package service

import (
	"math"
	"strings"
	"testing"
)

func TestParseByteRangeDecimalRejectsSigns(t *testing.T) {
	for _, header := range []string{"bytes=+1-3", "bytes=1-+3", "bytes=0--1", "bytes=0--2", "bytes=-+3", "bytes=+1-3,0-2"} {
		offset, length, ok, unsatisfiable := ParseByteRange(header, 10)
		if offset != 0 || length != 0 || ok || unsatisfiable {
			t.Errorf("signed decimal became a usable range: %q => %d,%d,%v,%v", header, offset, length, ok, unsatisfiable)
		}
	}
}

func TestParseByteRangeDecimalOversizedValuesStayRanges(t *testing.T) {
	huge := strings.Repeat("9", 128)
	for _, check := range []struct {
		header string
		offset int64
		length int64
		unsat  bool
	}{
		{"bytes=1-9223372036854775808", 1, 9, false},
		{"bytes=1-" + huge, 1, 9, false},
		{"bytes=-" + huge, 0, 10, false},
		{"bytes=" + huge + "-", 0, 0, true},
		{"bytes=" + huge + "-" + huge, 0, 0, true},
		{"bytes=000000000000000000001-000000000000000000003", 1, 3, false},
	} {
		offset, length, ok, unsat := ParseByteRange(check.header, 10)
		if offset != check.offset || length != check.length || ok != !check.unsat || unsat != check.unsat {
			t.Errorf("large decimal lost range semantics: %q => %d,%d,%v,%v want %d,%d,%v", check.header, offset, length, ok, unsat, check.offset, check.length, check.unsat)
		}
	}
}

func TestParseByteRangeDecimalOverflowStillValidatesRemainingDigits(t *testing.T) {
	huge := strings.Repeat("9", 128)
	for _, spec := range []string{"1-" + huge + "x", huge + "x-", "-" + huge + "x", "1-" + huge + "-1", "1-" + huge + "+1"} {
		if _, _, ok, unsat := ParseByteRange("bytes="+spec, 10); ok || unsat {
			t.Errorf("overflow hid trailing invalid syntax: %q", spec)
		}
	}
}

func TestParseByteRangeDecimalMaxSizeDoesNotOverflow(t *testing.T) {
	for _, header := range []string{"bytes=0-9223372036854775808", "bytes=-9223372036854775808", "bytes=0-9223372036854775807"} {
		offset, length, ok, unsat := ParseByteRange(header, math.MaxInt64)
		if offset != 0 || length != math.MaxInt64 || !ok || unsat {
			t.Errorf("max-size range overflow: %q => %d,%d,%v,%v", header, offset, length, ok, unsat)
		}
	}
}

func TestParseByteRangeDecimalEmptyObjectAndFirstRangeCompatibility(t *testing.T) {
	for _, header := range []string{"bytes=0-0", "bytes=0-9223372036854775808", "bytes=-9223372036854775808"} {
		if _, _, ok, unsat := ParseByteRange(header, 0); ok || !unsat {
			t.Errorf("valid range on empty object not unsatisfiable: %q", header)
		}
	}
	for _, header := range []string{"bytes=0-1,8-9", "bytes=0-1,invalid", "bytes= 0 - 1 "} {
		offset, length, ok, unsat := ParseByteRange(header, 10)
		if offset != 0 || length != 2 || !ok || unsat {
			t.Errorf("first-range/whitespace compatibility changed: %q => %d,%d,%v,%v", header, offset, length, ok, unsat)
		}
	}
}
