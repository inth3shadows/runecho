package guard

import (
	"os/exec"
	"sort"
	"strings"
	"testing"
)

// TestPyBuiltinsCoversInterpreter pins the generated pyBuiltins set (#387)
// against the interpreter actually on this machine.
//
// It asserts COVERAGE, not equality, and the asymmetry is the whole point. A
// name the interpreter defines but pyBuiltins lacks is a false positive on
// ordinary correct code — the defect #387 was filed for, and the one this test
// must fail on. A name pyBuiltins carries that this interpreter lacks is the
// expected consequence of generating from the newest Python available: the set
// is deliberately a superset, so those are logged, never failed. Asserting
// equality would make the test fail on CI (ubuntu 3.12) against a set generated
// on a newer local interpreter, which would teach people to regenerate DOWNWARD
// and reintroduce exactly the false positives this closes.
func TestPyBuiltinsCoversInterpreter(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not on PATH — cannot pin pyBuiltins against an interpreter")
	}
	out, err := exec.Command(py, "-c",
		`import keyword,builtins; print("\n".join(sorted(set(keyword.kwlist)|set(dir(builtins)))))`).Output()
	if err != nil {
		t.Skipf("python3 could not enumerate builtins: %v", err)
	}

	var missing, extra []string
	live := map[string]struct{}{}
	for _, n := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if n = strings.TrimSpace(n); n == "" {
			continue
		}
		live[n] = struct{}{}
		if _, ok := pyBuiltins[n]; !ok {
			missing = append(missing, n)
		}
	}
	for n := range pyBuiltins {
		if _, ok := live[n]; !ok {
			extra = append(extra, n)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)

	pyVer, _ := exec.Command(py, "--version").Output()
	t.Logf("interpreter=%s live=%d pyBuiltins=%d",
		strings.TrimSpace(string(pyVer)), len(live), len(pyBuiltins))
	if len(extra) > 0 {
		// Informational: expected whenever this interpreter is older than the
		// one the set was generated from.
		t.Logf("in pyBuiltins but not in this interpreter (%d, expected on an older Python): %s",
			len(extra), strings.Join(extra, " "))
	}
	if len(missing) > 0 {
		t.Errorf("pyBuiltins is missing %d name(s) this interpreter defines — each is a false positive "+
			"waiting on ordinary correct code (#387). Regenerate:\n"+
			"  python3 -c 'import keyword,builtins; print(\"\\n\".join(sorted(set(keyword.kwlist)|set(dir(builtins)))))'\n"+
			"missing: %s", len(missing), strings.Join(missing, " "))
	}
}

// TestPyBuiltinsExcludesSoftKeywords pins the one deliberate omission (#387).
// match/case/type/_ are valid identifiers, so folding them in would silence a
// genuine hallucination named `match` — a false negative bought for no
// false-positive gain, since a soft keyword in call position is a real call.
func TestPyBuiltinsExcludesSoftKeywords(t *testing.T) {
	for _, soft := range []string{"match", "case", "type_", "_"} {
		if soft == "type_" {
			continue // `type` IS a builtin; only the soft-keyword spelling is excluded
		}
		if _, ok := pyBuiltins[soft]; ok {
			t.Errorf("soft keyword %q must NOT be in pyBuiltins: it is a valid identifier, so "+
				"including it masks a real hallucination of that name", soft)
		}
	}
}
