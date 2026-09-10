package guard

import (
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// pyEnumScript emits the interpreter's own resolvable-name set plus its version.
// Run under -S for the same reason extract.go generates under -S: dir(builtins)
// is process state, and sitecustomize.py/usercustomize.py mutate it. Without -S
// a machine with line_profiler installed makes this test demand that `profile`
// be added to pyBuiltins — turning the pin into an instruction to bake in a
// permanent false negative.
const pyEnumScript = `import keyword,builtins,sys
print("%d.%d" % sys.version_info[:2])
print("\n".join(sorted(set(keyword.kwlist)|set(dir(builtins)))))`

// pyNewerThanCI names every builtin pyBuiltins may carry that an older
// interpreter legitimately lacks, keyed by the version that INTRODUCED it.
//
// It is adjudicated against the running interpreter rather than accepted on
// sight: the excuse "this is from a newer Python" is only allowed when it is
// arithmetically true. Two reasons. A free-form escape hatch would let a
// polluted name be waved through by adding it here — the exact permanent false
// negative the bounding exists to prevent. And an unadjudicated list has no
// stale gate, so when CI's image moves to 3.13+ these entries would silently
// stop appearing in `extra` and the list would become fiction with nothing red,
// which is precisely what .github/expected-skips.txt and pyKnownGaps both
// refuse to allow.
var pyNewerThanCI = map[string]string{
	"PythonFinalizationError": "3.13",
	"_IncompleteInputError":   "3.13",
}

// pySentinelNames must appear in any real interpreter's enumeration, at every
// Python 3 version. They replace a magic count floor: a threshold has no
// derivation and a wrapper emitting half a list would sail past one, whereas a
// wrapper emitting garbage cannot produce these.
var pySentinelNames = []string{"len", "print", "ValueError", "import", "def", "None"}

// TestPyBuiltinsCoversInterpreter pins the generated pyBuiltins set (#387)
// against the interpreter on this machine, in BOTH directions.
//
//   - A name the interpreter defines that pyBuiltins lacks is a false positive
//     on ordinary correct code — the defect #387 was filed for.
//   - A name pyBuiltins carries that the interpreter does not define silences
//     the guard on that identifier forever. An earlier draft only LOGGED these,
//     which meant nothing in this repository could detect an arbitrary name
//     entering the set: injecting `fetch_user_data` and `validate_schema` left
//     `go test ./...` fully green.
//
// Coverage, not equality, is still the rule for the first direction — CI runs an
// older Python than the set is generated from, and asserting equality there
// would teach the next person to regenerate DOWNWARD and reintroduce exactly the
// false positives this closed.
func TestPyBuiltinsCoversInterpreter(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not on PATH — cannot pin pyBuiltins against an interpreter")
	}
	out, err := exec.Command(py, "-S", "-c", pyEnumScript).Output()
	if err != nil {
		t.Skipf("python3 -S could not enumerate builtins: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		t.Fatalf("interpreter produced no name list (output %q) — this pin measured nothing", string(out))
	}
	liveVer := strings.TrimSpace(lines[0])

	live := map[string]struct{}{}
	var missing []string
	for _, n := range lines[1:] {
		if n = strings.TrimSpace(n); n == "" {
			continue
		}
		live[n] = struct{}{}
		if _, ok := pyBuiltins[n]; !ok {
			missing = append(missing, n)
		}
	}

	// Vacuity gate. A python3 that exits 0 while printing a partial or garbage
	// list — a wrapper, a stub on PATH — would otherwise leave this test green
	// while pinning nothing, and it is not a skip, so check-skips.sh cannot see
	// it either. Sentinels rather than a count: no magic number to justify, and
	// unlike a floor they reject plausible-length garbage.
	for _, s := range pySentinelNames {
		if _, ok := live[s]; !ok {
			t.Fatalf("interpreter enumeration is missing the sentinel %q (%d names seen) — "+
				"python3 on PATH is not producing a real builtins list, so this pin measured nothing",
				s, len(live))
		}
	}

	var unexplained []string
	for n := range pyBuiltins {
		if _, ok := live[n]; ok {
			continue
		}
		if _, isSite := pySiteBuiltins[n]; isSite {
			continue // expected: -S drops these by design, see pySiteBuiltins
		}
		if since, ok := pyNewerThanCI[n]; ok && pyVersionLess(liveVer, since) {
			continue // genuinely newer than this interpreter
		}
		unexplained = append(unexplained, n)
	}
	sort.Strings(missing)
	sort.Strings(unexplained)

	t.Logf("interpreter=python%s (-S) live=%d pyBuiltins=%d (core=%d + site=%d)",
		liveVer, len(live), len(pyBuiltins), len(pyCoreBuiltins), len(pySiteBuiltins))

	if len(missing) > 0 {
		t.Errorf("pyBuiltins is missing %d name(s) this interpreter defines — each is a false positive "+
			"waiting on ordinary correct code (#387). Regenerate with the command in extract.go:\n"+
			"missing: %s", len(missing), strings.Join(missing, " "))
	}
	if len(unexplained) > 0 {
		t.Errorf("pyBuiltins carries %d name(s) this interpreter does not define, and that are neither "+
			"site-injected nor newer-than-this-version builtins: %s\n"+
			"Each one silences the guard on that identifier FOREVER. If it is a real builtin from a newer "+
			"Python, add it to pyNewerThanCI keyed by the version that introduced it. If it arrived from a "+
			"mutated builtins namespace (sitecustomize.py, usercustomize.py, gettext.install, "+
			"line_profiler), do NOT add it — regenerate under `python3 -S` on a clean interpreter.",
			len(unexplained), strings.Join(unexplained, " "))
	}
}

// pyVersionLess reports whether a "MAJOR.MINOR" version string is older than b.
func pyVersionLess(a, b string) bool {
	am, an := pyVersionParts(a)
	bm, bn := pyVersionParts(b)
	if am != bm {
		return am < bm
	}
	return an < bn
}

func pyVersionParts(v string) (int, int) {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return 0, 0
	}
	maj, _ := strconv.Atoi(parts[0])
	min, _ := strconv.Atoi(parts[1])
	return maj, min
}

// TestPyBuiltinsExcludesSoftKeywords pins the soft-keyword rule and its one
// exception (#387).
func TestPyBuiltinsExcludesSoftKeywords(t *testing.T) {
	// match/case/_ are ordinary identifiers outside their grammatical position,
	// so folding them in would silence a genuine hallucination named `match` —
	// a false negative bought for no false-positive gain.
	for _, soft := range []string{"match", "case", "_"} {
		if _, ok := pyBuiltins[soft]; ok {
			t.Errorf("soft keyword %q must NOT be in pyBuiltins: it is a valid identifier, so "+
				"including it masks a real hallucination of that name", soft)
		}
	}
	// `type` is asserted POSITIVELY because its danger runs the other way: a real
	// builtin that merely also appears in keyword.softkwlist, so someone
	// "fixing" the set to match softkwlist would delete it and false-positive on
	// every type(...) call in Python. An earlier draft skipped it through a dead
	// branch and asserted nothing about it at all.
	if _, ok := pyBuiltins["type"]; !ok {
		t.Error("`type` MUST be in pyBuiltins: it is a real builtin (type(x), " +
			"type(name, bases, dict)) that only incidentally appears in keyword.softkwlist. " +
			"Removing it false-positives on every type(...) call — the defect #387 closed")
	}
}

// TestPyCallShapeBuiltinsKeepRedeclarable pins the second consumer's different
// contract: callshape must NOT skip builtins a project realistically redeclares,
// because a real declaration makes the builtin irrelevant and the arity check
// meaningful. See pyCallShapeRedeclarable.
func TestPyCallShapeBuiltinsKeepRedeclarable(t *testing.T) {
	cs := callshapeBuiltinsFor(LangPython)
	for _, n := range []string{"help", "license", "credits", "copyright", "exit", "quit", "aiter", "anext"} {
		if _, ok := cs[n]; ok {
			t.Errorf("callshape must not skip %q: a repo that declares its own %q gets no arity "+
				"check on calls to it", n, n)
		}
		if _, ok := pyBuiltins[n]; !ok {
			t.Errorf("%q must still be in pyBuiltins for the RESOLVE check — it does resolve", n)
		}
	}
	// The control: names nobody usefully redeclares stay skipped, so the scan
	// budget is untouched.
	for _, n := range []string{"print", "len", "type", "range"} {
		if _, ok := cs[n]; !ok {
			t.Errorf("callshape should still skip %q", n)
		}
	}
}
