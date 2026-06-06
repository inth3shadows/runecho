package snapshot

import "testing"

// Regression: coverage was computed with integer division at two call sites,
// so 199/200 reported 99 and anything under 1% reported 0.
func TestCoveragePercent(t *testing.T) {
	cases := []struct {
		indexed, supported int
		want               float64
	}{
		{199, 200, 99.5},
		{1, 250, 0.4}, // sub-1% must not read as "fully uncovered"
		{200, 200, 100},
		{0, 200, 0},
		{0, 0, 0}, // unmeasured denominator; callers gate on supported > 0
		{1, 3, 33.3},
	}
	for _, tc := range cases {
		if got := CoveragePercent(tc.indexed, tc.supported); got != tc.want {
			t.Errorf("CoveragePercent(%d, %d) = %v, want %v", tc.indexed, tc.supported, got, tc.want)
		}
	}
}
