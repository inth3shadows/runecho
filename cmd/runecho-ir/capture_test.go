package main

import (
	"testing"
	"time"

	"github.com/inth3shadows/runecho/internal/ir"
)

// cliGenerateTimeout maps RUNECHO_GENERATE_TIMEOUT onto an ir.GeneratorConfig
// value: unset → 0 (package default), off-ish → ir.Unbounded, a duration →
// itself, garbage → 0 with a warning. t.Setenv isolates each case.
func TestCLIGenerateTimeout(t *testing.T) {
	cases := []struct {
		name string
		val  string // value set for RUNECHO_GENERATE_TIMEOUT; "" = unset/empty
		want time.Duration
	}{
		{"unset", "", 0},
		{"whitespace", "   ", 0},
		{"off", "off", ir.Unbounded},
		{"none", "none", ir.Unbounded},
		{"zero", "0", ir.Unbounded},
		{"duration", "5m", 5 * time.Minute},
		{"seconds", "90s", 90 * time.Second},
		{"garbage", "soon", 0},
		{"negative", "-5s", 0}, // <=0 durations are rejected, not treated as unbounded
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(generateTimeoutEnv, c.val)
			if got := cliGenerateTimeout(); got != c.want {
				t.Errorf("cliGenerateTimeout() = %v, want %v", got, c.want)
			}
		})
	}
}
