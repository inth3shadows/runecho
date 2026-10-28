package doctor

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A log of only denies (RUNECHO_GUARD_POSTURE=deny, #528) is guard activity:
// counted as asks, so doctor does not report the unwired-outcome signature.
func TestCountRecentDecisionsCountsDeny(t *testing.T) {
	p := filepath.Join(t.TempDir(), "decisions.jsonl")
	ts := time.Now().UTC().Format(time.RFC3339)
	line := `{"v":1,"ts":"` + ts + `","mode":"hook","decision":"deny","reason":"violations"}` + "\n"
	if err := os.WriteFile(p, []byte(line+line), 0o600); err != nil {
		t.Fatal(err)
	}
	asks, outcomes, err := countRecentDecisions(p, time.Hour)
	if err != nil || asks != 2 || outcomes != 0 {
		t.Errorf("countRecentDecisions = %d asks, %d outcomes, %v; want 2, 0, nil", asks, outcomes, err)
	}
}
