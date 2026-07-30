package guard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The #243 step-2 spike measured reach with a perfect AST on BOTH sides, so it
// could not measure this check's own false-positive rate — the same gap
// TestCallShapeDifferential closed for the extractor. This closes it for the
// comparison: the whole pipeline (call-side extractor → declaration-side scanner →
// keyword comparison) is run over real committed Python, and every mismatch it
// reports is adjudicated by CPython's own `ast`.
//
// Working, committed code should produce ZERO reports. Anything reported is either
// a genuine defect in the corpus (the oracle confirms the keyword really is not
// accepted) or a false positive — and a false positive fails the test.
//
// The zero alone would be vacuous, so the second half mutates a keyword at every
// site the oracle says is checkable and requires the check to fire. A silent
// detector and a correct one produce the same zero on clean code.
//
// Corpus: internal/guard/testdata/callshape by default. Point
// RUNECHO_CALLSHAPE_CORPUS at a real repository to run it at scale.

// declOracleScript emits, per file, the accepted-keyword set for every
// unqualified kwarg-bearing call site whose callee has exactly one module-level
// `def` IN THAT FILE — the population this check restricts itself to.
//
// A `**splat` among the call's keywords is NOT skipped: an explicitly named
// keyword must be accepted whether or not a splat sits beside it, so those sites
// are adjudicated like any other.
//
// It models FEWER abstentions than the Go side wherever doing so only costs
// recall, but it MUST model everything that affects which declaration a call
// reaches — otherwise a false positive is scored as a CONFIRMED true positive and
// the headline number means nothing. An adversarial review found exactly that hole:
// the oracle ignored rebinding, so a `for handler in fns: handler(...)` false
// positive was counted as a real defect. It therefore now skips any callee that is
// rebound by ANY store-context binding (assignment, for/comprehension target,
// with/except as, walrus, parameter, import) or that has a non-column-zero
// declaration. Over-skipping only shrinks the population, which keeps the claim
// conservative; under-skipping would corrupt it.
const declOracleScript = `
import ast, json, os, sys

def kwshape(fn):
    a = fn.args
    # posonlyargs are NOT keyword-callable; args + kwonlyargs are.
    return [p.arg for p in a.args] + [p.arg for p in a.kwonlyargs], a.kwarg is not None

def module_defs(tree):
    """name -> list of (accepted, has_kwstar, decorated) for module-level defs.
    Recurses through if/try/with wrappers, never into class or function bodies."""
    out = {}
    def walk(body):
        for n in body:
            if isinstance(n, (ast.FunctionDef, ast.AsyncFunctionDef)):
                acc, star = kwshape(n)
                out.setdefault(n.name, []).append((tuple(acc), star, bool(n.decorator_list)))
            elif isinstance(n, ast.ClassDef):
                pass
            else:
                for f in ("body", "orelse", "finalbody"):
                    sub = getattr(n, f, None)
                    if isinstance(sub, list):
                        walk(sub)
                for h in getattr(n, "handlers", []) or []:
                    walk(h.body)
    walk(tree.body)
    return out

out = {}
for root, dirs, files in os.walk(sys.argv[1]):
    dirs[:] = [d for d in dirs if d not in
               (".venv", "venv", "env", "node_modules", ".git", "__pycache__",
                "build", "dist", ".tox", ".mypy_cache", "site-packages", "vendor")]
    for fn in files:
        if not fn.endswith(".py"):
            continue
        p = os.path.join(root, fn)
        try:
            with open(p, encoding="utf-8") as fh:
                tree = ast.parse(fh.read(), filename=p)
        except (SyntaxError, UnicodeDecodeError, OSError, ValueError, RecursionError):
            continue
        defs = module_defs(tree)
        # Everything that could make a bare call reach something other than the
        # module-level def. Over-inclusive on purpose.
        rebound = set()
        for n in ast.walk(tree):
            if isinstance(n, ast.Name) and isinstance(n.ctx, ast.Store):
                rebound.add(n.id)
            elif isinstance(n, ast.ExceptHandler) and n.name:
                rebound.add(n.name)
            elif isinstance(n, ast.arg):
                rebound.add(n.arg)
            elif isinstance(n, ast.alias):
                rebound.add((n.asname or n.name).split(".")[0])
            elif isinstance(n, (ast.FunctionDef, ast.AsyncFunctionDef)) and n.col_offset != 0:
                rebound.add(n.name)
        rows = []
        for n in ast.walk(tree):
            if not isinstance(n, ast.Call) or not isinstance(n.func, ast.Name):
                continue
            named = [k.arg for k in n.keywords if k.arg is not None]
            if not named:
                continue
            if n.func.id in rebound:
                continue
            shapes = defs.get(n.func.id)
            if not shapes or len(set(shapes)) != 1:
                continue  # undeclared here, or disagreeing branches
            acc, star, deco = shapes[0]
            if star or deco:
                continue
            rows.append({"line": n.lineno, "name": n.func.id,
                         "accepted": list(acc), "kwargs": named})
        if rows:
            out[os.path.relpath(p, sys.argv[1])] = rows
print(json.dumps(out))
`

type declOracleSite struct {
	Line     int      `json:"line"`
	Name     string   `json:"name"`
	Accepted []string `json:"accepted"`
	Kwargs   []string `json:"kwargs"`
}

func runDeclOracle(t *testing.T, root string) map[string][]declOracleSite {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not on PATH — the differential oracle is CPython's ast module")
	}
	script := filepath.Join(t.TempDir(), "decl_oracle.py")
	if err := os.WriteFile(script, []byte(declOracleScript), 0o600); err != nil {
		t.Fatalf("write oracle: %v", err)
	}
	cmd := exec.Command(py, script, root)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		// Corpus mode points at arbitrary repositories where ast.parse can raise
		// beyond what the script catches. An oracle that cannot run is no evidence
		// either way, so skip rather than fail the code under test.
		t.Skipf("oracle failed on %s: %v\n%s", root, err, stderr.String())
	}
	var got map[string][]declOracleSite
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode oracle output: %v", err)
	}
	return got
}

// pyCorpusFiles lists the .py files under root that the oracle also walks, so
// both sides see the same population.
func pyCorpusFiles(t *testing.T, root string) []string {
	t.Helper()
	skip := map[string]bool{".venv": true, "venv": true, "env": true, "node_modules": true,
		".git": true, "__pycache__": true, "build": true, "dist": true, ".tox": true,
		".mypy_cache": true, "site-packages": true, "vendor": true}
	var out []string
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // unreadable subtree — the oracle skips it too
		}
		if info.IsDir() {
			if skip[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, ".py") {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

// checkWholeFile runs the check in its Write posture: the file's whole text is the
// added text, which is exactly what the hook passes for a Write.
func checkWholeFile(lines []AddedLine) []CallShapeMismatch {
	return PyCallShapeMismatches(LangPython, nil,
		FileDiff{Path: "corpus.py", AddedLines: lines}, nil, true)
}

func TestCallShapeCheckNoFalsePositives(t *testing.T) {
	root := os.Getenv("RUNECHO_CALLSHAPE_CORPUS")
	if root == "" {
		root = filepath.Join("testdata", "callshape")
	}
	if _, err := os.Stat(root); err != nil {
		t.Skipf("corpus %s unavailable: %v", root, err)
	}
	oracle := runDeclOracle(t, root)

	var reported, confirmed, falsePositives int
	for _, path := range pyCorpusFiles(t, root) {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			continue
		}
		rel = filepath.ToSlash(rel)
		// The oracle keys by the platform separator os.path.relpath produced; on
		// this project's platforms that is "/" already, but normalize both sides.
		sites := map[string]declOracleSite{}
		for _, s := range oracle[rel] {
			sites[fmt.Sprintf("%d:%s", s.Line, s.Name)] = s
		}
		for _, m := range checkWholeFile(fileAsAddedLines(t, path)) {
			reported++
			s, known := sites[fmt.Sprintf("%d:%s", m.LineNo, m.Callee)]
			if !known {
				falsePositives++
				t.Errorf("FALSE POSITIVE %s:%d — reported %s(%s=…) at a site CPython does not consider checkable "+
					"(callee undeclared in this file, ambiguous, decorated, or takes **kwargs)",
					rel, m.LineNo, m.Callee, m.Keyword)
				continue
			}
			if containsName(s.Accepted, m.Keyword) {
				falsePositives++
				t.Errorf("FALSE POSITIVE %s:%d — reported %s(%s=…) but CPython says %s accepts %v",
					rel, m.LineNo, m.Callee, m.Keyword, m.Callee, s.Accepted)
				continue
			}
			// The oracle agrees the keyword is not accepted: a real defect in the
			// corpus, which is a true positive, not a failure.
			confirmed++
			t.Logf("CONFIRMED %s:%d — %s(%s=…); accepted: %v", rel, m.LineNo, m.Callee, m.Keyword, s.Accepted)
		}
	}
	var oracleSites int
	for _, rows := range oracle {
		oracleSites += len(rows)
	}
	t.Logf("corpus %s: oracle-checkable sites %d | reported %d | confirmed %d | false positives %d",
		root, oracleSites, reported, confirmed, falsePositives)
	if falsePositives > 0 {
		t.Errorf("%d false positive(s) — the check must abstain, never guess", falsePositives)
	}
}

// TestCallShapeCheckMutationKill is the anti-vacuity half. Zero reports on clean
// code is equally consistent with "the corpus is correct" and "the detector never
// fires"; only mutation distinguishes them. Every oracle-checkable site gets one
// keyword renamed to a name the declaration provably does not accept, and the
// check must report it — or, legitimately, abstain for a reason the Go side has
// and the oracle does not model (shadowing). Abstentions are counted separately
// and reported as recall; only a WRONG answer fails.
func TestCallShapeCheckMutationKill(t *testing.T) {
	root := os.Getenv("RUNECHO_CALLSHAPE_CORPUS")
	if root == "" {
		root = filepath.Join("testdata", "callshape")
	}
	if _, err := os.Stat(root); err != nil {
		t.Skipf("corpus %s unavailable: %v", root, err)
	}
	oracle := runDeclOracle(t, root)

	const mutant = "runecho_not_a_parameter_xyz"
	var mutated, killed, abstained int
	for _, path := range pyCorpusFiles(t, root) {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			continue
		}
		rows := oracle[filepath.ToSlash(rel)]
		if len(rows) == 0 {
			continue
		}
		base := fileAsAddedLines(t, path)
		// Sites the UNMUTATED file already reports on are excluded: a kill there
		// would not be attributable to the mutation.
		preexisting := map[string]bool{}
		for _, m := range checkWholeFile(base) {
			preexisting[fmt.Sprintf("%d:%s", m.LineNo, m.Callee)] = true
		}
		for _, s := range rows {
			if len(s.Kwargs) == 0 || preexisting[fmt.Sprintf("%d:%s", s.Line, s.Name)] {
				continue
			}
			victim := s.Kwargs[0]
			if containsName(s.Accepted, mutant) {
				continue // vanishingly unlikely, but the mutation must be a real change
			}
			lines := mutateKeywordOnLine(base, s.Line, victim, mutant)
			if lines == nil {
				continue // the keyword is not literally on that line (a continuation) — not a mutation
			}
			mutated++
			found := false
			for _, m := range checkWholeFile(lines) {
				if m.LineNo == s.Line && m.Callee == s.Name && m.Keyword == mutant {
					found = true
					break
				}
			}
			if found {
				killed++
			} else {
				abstained++
			}
		}
	}
	if mutated == 0 {
		t.Fatalf("corpus %s produced no mutations — the anti-vacuity check proved nothing", root)
	}
	t.Logf("corpus %s: mutations %d | killed %d (%.1f%%) | abstained %d",
		root, mutated, killed, 100*float64(killed)/float64(mutated), abstained)
	if killed == 0 {
		t.Errorf("0/%d mutations killed — the check is inert on this corpus", mutated)
	}
}

// mutateKeywordOnLine returns a copy of lines with the first `victim=` occurrence
// on the given 1-based line rewritten to `replacement=`, or nil when the keyword
// does not literally appear there (a call whose argument list continues onto other
// lines can carry the keyword on a different line than the callee).
func mutateKeywordOnLine(lines []AddedLine, lineNo int, victim, replacement string) []AddedLine {
	idx := -1
	for i, l := range lines {
		if l.LineNo == lineNo {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	// Match `victim` immediately followed by optional spaces and a single `=`
	// (never `==`), so a positional argument that merely mentions the name is not
	// rewritten.
	orig := lines[idx].Text
	next := replaceKeywordOnce(orig, victim, replacement)
	if next == orig {
		return nil
	}
	out := make([]AddedLine, len(lines))
	copy(out, lines)
	out[idx] = AddedLine{LineNo: lineNo, Text: next}
	return out
}

func replaceKeywordOnce(s, victim, replacement string) string {
	for i := 0; i+len(victim) <= len(s); i++ {
		if s[i:i+len(victim)] != victim {
			continue
		}
		if i > 0 && isIdentByte(s[i-1]) {
			continue // suffix of a longer identifier
		}
		j := i + len(victim)
		for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
			j++
		}
		if j >= len(s) || s[j] != '=' {
			continue
		}
		if j+1 < len(s) && s[j+1] == '=' {
			continue // a comparison, not a keyword argument
		}
		return s[:i] + replacement + s[i+len(victim):]
	}
	return s
}

func isIdentByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

func containsName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// declShapeOracleScript emits, per file, the accepted-keyword set of every
// MODULE-LEVEL `def` that sits at column zero — the exact population
// pyDeclIndex.shapesFor claims to read. Decorated definitions are reported with a
// flag rather than skipped, so the decoratedAbove walk is adjudicated too.
//
// col0 is taken from the AST's own column offset, so a def indented under a
// wrapper is excluded on the oracle side as well; comparing against ALL
// module-level defs would report the column-zero narrowing as a defect instead of
// as the documented narrowing it is.
const declShapeOracleScript = `
import ast, json, os, sys

def rows_for(tree):
    rows = []
    def walk(body):
        for n in body:
            if isinstance(n, (ast.FunctionDef, ast.AsyncFunctionDef)):
                if n.col_offset != 0:
                    continue
                a = n.args
                rows.append({
                    "line": n.lineno,
                    "name": n.name,
                    "accepted": [p.arg for p in a.args] + [p.arg for p in a.kwonlyargs],
                    "kwstar": a.kwarg is not None,
                    "decorated": bool(n.decorator_list),
                })
            elif isinstance(n, ast.ClassDef):
                pass
            else:
                for f in ("body", "orelse", "finalbody"):
                    sub = getattr(n, f, None)
                    if isinstance(sub, list):
                        walk(sub)
                for h in getattr(n, "handlers", []) or []:
                    walk(h.body)
    walk(tree.body)
    return rows

out = {}
for root, dirs, files in os.walk(sys.argv[1]):
    dirs[:] = [d for d in dirs if d not in
               (".venv", "venv", "env", "node_modules", ".git", "__pycache__",
                "build", "dist", ".tox", ".mypy_cache", "site-packages", "vendor")]
    for fn in files:
        if not fn.endswith(".py"):
            continue
        p = os.path.join(root, fn)
        try:
            with open(p, encoding="utf-8") as fh:
                tree = ast.parse(fh.read(), filename=p)
        except (SyntaxError, UnicodeDecodeError, OSError, ValueError, RecursionError):
            continue
        rows = rows_for(tree)
        if rows:
            out[os.path.relpath(p, sys.argv[1])] = rows
print(json.dumps(out))
`

type declShapeOracleRow struct {
	Line      int      `json:"line"`
	Name      string   `json:"name"`
	Accepted  []string `json:"accepted"`
	KwStar    bool     `json:"kwstar"`
	Decorated bool     `json:"decorated"`
}

// TestCallShapeDeclDifferential is the evidence for replacing a real parser with a
// hand-written one. The declaration side started as a tree-sitter parse and was
// removed for latency (about 8 ms of fixed overhead per parse against a ~12 ms hook
// budget); what stands in its place is pyKeywordParams reading masked text. That is
// only defensible if it agrees with CPython, so this compares the accepted-keyword
// set, the `**kwargs` flag and the decorated flag for every column-zero
// module-level def in the corpus.
//
// Disagreement is a FAILURE, not a statistic — unlike the call-side extractor,
// which is allowed to abstain per site, a declaration this reader claims to have
// read must be read correctly. The one permitted deviation is a shape marked
// Unknowable, which is an explicit abstention.
func TestCallShapeDeclDifferential(t *testing.T) {
	root := os.Getenv("RUNECHO_CALLSHAPE_CORPUS")
	if root == "" {
		root = filepath.Join("testdata", "callshape")
	}
	if _, err := os.Stat(root); err != nil {
		t.Skipf("corpus %s unavailable: %v", root, err)
	}
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not on PATH — the differential oracle is CPython's ast module")
	}
	script := filepath.Join(t.TempDir(), "decl_shape_oracle.py")
	if err := os.WriteFile(script, []byte(declShapeOracleScript), 0o600); err != nil {
		t.Fatalf("write oracle: %v", err)
	}
	cmd := exec.Command(py, script, root)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("oracle failed on %s: %v\n%s", root, err, stderr.String())
	}
	var oracle map[string][]declShapeOracleRow
	if err := json.Unmarshal(out, &oracle); err != nil {
		t.Fatalf("decode oracle output: %v", err)
	}

	var compared, unknowable, missing, disagreed int
	for _, path := range pyCorpusFiles(t, root) {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			continue
		}
		rows := oracle[filepath.ToSlash(rel)]
		if len(rows) == 0 {
			continue
		}
		idx := newPyDeclIndex(fileAsAddedLines(t, path))
		for _, want := range rows {
			var got pyDeclShape
			var found bool
			for _, s := range idx.shapesFor(want.Name) {
				if s.Line == want.Line {
					got, found = s, true
					break
				}
			}
			if !found {
				// A def the scan did not locate at all. Silence is safe, so this is
				// recall, not a defect — but it is counted and logged so a regression
				// that quietly stops finding declarations shows up as a number.
				missing++
				continue
			}
			if got.Unknowable {
				unknowable++
				continue
			}
			compared++
			if strings.Join(got.Keywords, ",") != strings.Join(want.Accepted, ",") {
				disagreed++
				t.Errorf("DISAGREES %s:%d %s — accepted %v, CPython says %v",
					rel, want.Line, want.Name, got.Keywords, want.Accepted)
			}
			if got.HasKwStar != want.KwStar {
				disagreed++
				t.Errorf("DISAGREES %s:%d %s — HasKwStar %v, CPython says %v",
					rel, want.Line, want.Name, got.HasKwStar, want.KwStar)
			}
			// Decorated may be reported when CPython says otherwise — the upward walk
			// errs toward abstaining — but never the reverse, which would compare a
			// call against a signature a wrapper has replaced.
			if want.Decorated && !got.Decorated {
				disagreed++
				t.Errorf("MISSED DECORATOR %s:%d %s — CPython sees %d decorator(s)",
					rel, want.Line, want.Name, 1)
			}
		}
	}
	t.Logf("corpus %s: declarations compared %d | abstained (unknowable) %d | not located %d | disagreements %d",
		root, compared, unknowable, missing, disagreed)
	if compared == 0 {
		t.Fatalf("compared no declarations — the differential proved nothing")
	}
	if disagreed > 0 {
		t.Errorf("%d disagreement(s) with CPython", disagreed)
	}
}
