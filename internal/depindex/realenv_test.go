package depindex

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
)

// Ground-truth false-positive harness. Opt-in: it needs a real virtualenv and a
// truth file captured from that env's live interpreter, both supplied by
// scripts/pydep-truth.py. It is NOT part of the default `go test ./...` run,
// because it depends on an environment CI does not have — but it is the only
// check that answers the question the fixtures cannot:
//
//	does the resolver's export set contain everything the interpreter reports?
//
// A name present in dir(module) but absent from a Resolved set is a false
// positive waiting to happen: valid code calling a real attribute the guard would
// flag. That is the one defect class this whole package exists to prevent, so it
// is asserted against reality rather than against hand-written fixtures.
//
// Running it against 15 popular packages is what surfaced all three real defects
// in the first implementation (see TestLookup_BindingsRealWorldShapes) — the
// fixtures were green throughout. Re-run it after ANY change to the extraction
// rules, and whenever adding a package shape the fixtures don't model.
//
//	python3 scripts/pydep-truth.py /path/to/.venv /tmp/truth.json
//	SCRATCH_VENV=/path/to/.venv SCRATCH_TRUTH=/tmp/truth.json \
//	  go test ./internal/depindex -run RealEnvGroundTruth -v
func TestRealEnvGroundTruth(t *testing.T) {
	venv, truthPath := os.Getenv("SCRATCH_VENV"), os.Getenv("SCRATCH_TRUTH")
	if venv == "" || truthPath == "" {
		t.Skip("set SCRATCH_VENV and SCRATCH_TRUTH to run (see scripts/pydep-truth.py)")
	}
	raw, err := os.ReadFile(truthPath)
	if err != nil {
		t.Fatal(err)
	}
	var truth map[string][]string
	if err := json.Unmarshal(raw, &truth); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VIRTUAL_ENV", venv)
	idx := NewPythonIndex(t.TempDir())

	mods := make([]string, 0, len(truth))
	for m := range truth {
		mods = append(mods, m)
	}
	sort.Strings(mods)

	totalFP, resolved := 0, 0
	for _, m := range mods {
		ps := idx.Lookup(m)
		if ps.Res != Resolved {
			t.Logf("%-12s %-8s (abstains — cannot false-positive)", m, ps.Res)
			continue
		}
		resolved++
		var missing []string
		for _, n := range truth[m] {
			// The guard abstains on underscore-prefixed selectors, so a private
			// attribute missing from the set can never become a violation.
			if strings.HasPrefix(n, "_") {
				continue
			}
			if !ps.Has(n) {
				missing = append(missing, n)
			}
		}
		totalFP += len(missing)
		if len(missing) > 0 {
			t.Errorf("%-12s FALSE-POSITIVE RISK: %d real attrs absent from a Resolved set: %v",
				m, len(missing), missing)
			continue
		}
		t.Logf("%-12s resolved, 0 FP across %d real attrs", m, len(truth[m]))
	}
	t.Logf("resolved=%d/%d modules; false-positive-risk names=%d", resolved, len(mods), totalFP)
}
