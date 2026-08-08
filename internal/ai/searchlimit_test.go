package ai

import "testing"

func TestClampSearchLimit(t *testing.T) {
	for _, tc := range []struct{ in, want int }{
		{0, 10}, {-5, 10}, {1, 1}, {50, 50}, {100, 100}, {101, 100}, {200, 100}, {99999, 100},
	} {
		if got := clampSearchLimit(tc.in); got != tc.want {
			t.Errorf("clampSearchLimit(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
