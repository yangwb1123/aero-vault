package s3compat

import "testing"

func TestParseCopySourceRange(t *testing.T) {
	tests := []struct {
		value      string
		wantOffset int64
		wantLength int64
		valid      bool
	}{
		{value: "bytes=0-0", wantOffset: 0, wantLength: 1, valid: true},
		{value: "bytes=5-9", wantOffset: 5, wantLength: 5, valid: true},
		{value: "bytes=9-5"},
		{value: "bytes=-5"},
		{value: "bytes=5-"},
		{value: "bytes=1-2junk"},
		{value: "items=1-2"},
	}
	for _, test := range tests {
		offset, length, err := parseCopySourceRange(test.value)
		if test.valid && (err != nil || offset != test.wantOffset || length != test.wantLength) {
			t.Fatalf(
				"parse %q = (%d,%d,%v), want (%d,%d,nil)",
				test.value, offset, length, err, test.wantOffset, test.wantLength,
			)
		}
		if !test.valid && err == nil {
			t.Fatalf("parse %q unexpectedly succeeded", test.value)
		}
	}
}
