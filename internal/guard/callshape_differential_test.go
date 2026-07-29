package guard

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// This is the test that measures what the #243 step-2 spike deliberately could
// not: the EXTRACTOR's own false-positive rate. The spike used a perfect AST on
// both sides, so it proved the comparison logic sound and the reach real — but the
// extractor here reads masked text with a regex, and every disagreement with a
// real parser is a defect that would surface as a false positive in the hook.
//
// Ground truth is CPython's own `ast` module. Only call sites this extractor
// actually emitted are compared: a site it declined to emit is an abstention,
// which is safe by design and reported separately as recall, never as a failure.
//
// Corpus: internal/guard/testdata/callshape by default. Point
// RUNECHO_CALLSHAPE_CORPUS at a real repository to run it at scale — that is how
// the shipped numbers in the #243 thread were produced.

// oracleScript emits, for every unqualified call site CPython sees, the same shape
// fields this extractor produces. It applies no filtering of its own: the Go side
// decides what to emit, and only emitted sites are compared.
const oracleScript = `
import ast, json, os, sys

def shape(node):
    pos = sum(1 for a in node.args if not isinstance(a, ast.Starred))
    return {
        "line": node.lineno,
        "name": node.func.id,
        "pos": pos,
        "kwargs": [k.arg for k in node.keywords if k.arg is not None],
        "star": any(isinstance(a, ast.Starred) for a in node.args),
        "kwstar": any(k.arg is None for k in node.keywords),
        "lambda": any(isinstance(x, ast.Lambda)
                      for a in list(node.args) + [k.value for k in node.keywords]
                      for x in [a]),
    }

out = {}
for root, dirs, files in os.walk(sys.argv[1]):
    dirs[:] = [d for d in dirs if d not in
               (".venv", "venv", "node_modules", ".git", "__pycache__", "build", "dist", ".tox")]
    for fn in files:
        if not fn.endswith(".py"):
            continue
        p = os.path.join(root, fn)
        try:
            with open(p, encoding="utf-8") as fh:
                tree = ast.parse(fh.read(), filename=p)
        except (SyntaxError, UnicodeDecodeError, OSError):
            continue
        rows = [shape(n) for n in ast.walk(tree)
                if isinstance(n, ast.Call) and isinstance(n.func, ast.Name)]
        out[os.path.relpath(p, sys.argv[1])] = rows
print(json.dumps(out))
`

type oracleShape struct {
	Line   int      `json:"line"`
	Name   string   `json:"name"`
	Pos    int      `json:"pos"`
	Kwargs []string `json:"kwargs"`
	Star   bool     `json:"star"`
	KwStar bool     `json:"kwstar"`
	Lambda bool     `json:"lambda"`
}

// key identifies a call site as coarsely as both sides can agree on: two calls to
// the same name on the same line are compared as a multiset, since neither side
// can order them reliably.
type siteKey struct {
	file string
	line int
	name string
}

func runOracle(t *testing.T, root string) map[string][]oracleShape {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not on PATH — the differential oracle is CPython's ast module")
	}
	script := filepath.Join(t.TempDir(), "oracle.py")
	if err := os.WriteFile(script, []byte(oracleScript), 0o600); err != nil {
		t.Fatalf("write oracle: %v", err)
	}
	cmd := exec.Command(py, script, root)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		// The corpus mode is pointed at arbitrary repositories, where ast.parse can
		// raise beyond the exceptions the script catches (ValueError on a NUL byte,
		// RecursionError on deeply nested literals). Surface the traceback and SKIP:
		// an oracle that cannot run is no evidence either way, not a failure of the
		// code under test.
		t.Skipf("oracle failed on %s: %v\n%s", root, err, stderr.String())
	}
	var got map[string][]oracleShape
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode oracle output: %v", err)
	}
	return got
}

// fileAsAddedLines presents a whole file as one contiguous run of added lines,
// which is how the extractor sees a newly added file.
func fileAsAddedLines(t *testing.T, path string) []AddedLine {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out []AddedLine
	for i, s := range strings.Split(string(b), "\n") {
		out = append(out, AddedLine{LineNo: i + 1, Text: s})
	}
	return out
}

func TestCallShapeDifferential(t *testing.T) {
	root := os.Getenv("RUNECHO_CALLSHAPE_CORPUS")
	if root == "" {
		root = filepath.Join("testdata", "callshape")
	}
	if _, err := os.Stat(root); err != nil {
		t.Skipf("corpus %s unavailable: %v", root, err)
	}

	oracle := runOracle(t, root)

	// truth[key] is the multiset of oracle shapes at that site.
	truth := make(map[siteKey][]oracleShape)
	oracleTotal := 0
	for rel, rows := range oracle {
		for _, r := range rows {
			truth[siteKey{rel, r.Line, r.Name}] = append(truth[siteKey{rel, r.Line, r.Name}], r)
			oracleTotal++
		}
	}

	var emitted, agreed, abstained int
	var disagreements []string

	for rel := range oracle {
		path := filepath.Join(root, rel)
		for _, got := range ExtractCallShapes(LangPython, fileAsAddedLines(t, path), nil) {
			// A shape the extractor itself marked unreliable is an abstention: the
			// consumer must skip the call, so its counts are never acted on and
			// comparing them would measure nothing.
			if got.Unreliable {
				abstained++
				continue
			}
			emitted++
			cands := truth[siteKey{rel, got.LineNo, got.Name}]
			if len(cands) == 0 {
				// The extractor reported a call CPython does not see there. This is
				// the worst class of disagreement: it would flag a call site that
				// does not exist.
				disagreements = append(disagreements,
					rel+":"+itoa(got.LineNo)+" "+got.Name+" — extractor invented a call site")
				continue
			}
			if matchAny(got, cands) {
				agreed++
				continue
			}
			disagreements = append(disagreements,
				rel+":"+itoa(got.LineNo)+" "+got.Name+
					" — extractor "+describe(got)+", oracle "+describeAll(cands))
		}
	}

	if emitted == 0 {
		t.Fatalf("extractor emitted nothing over %d oracle call sites in %s — "+
			"a vacuous pass, not a clean one", oracleTotal, root)
	}

	t.Logf("corpus %s: oracle saw %d unqualified call sites; extractor emitted %d "+
		"usable (recall %.1f%%) and self-marked %d unreliable; agreement %d/%d = %.2f%%",
		root, oracleTotal, emitted, 100*float64(emitted)/float64(oracleTotal),
		abstained, agreed, emitted, 100*float64(agreed)/float64(emitted))

	if len(disagreements) > 0 {
		limit := len(disagreements)
		if limit > 25 {
			limit = 25
		}
		sort.Strings(disagreements)
		t.Errorf("%d of %d emitted shapes disagree with CPython — every one is a "+
			"false positive the hook would produce:\n  %s",
			len(disagreements), emitted, strings.Join(disagreements[:limit], "\n  "))
	}
}

// matchAny reports whether got equals any oracle shape at the same site. A call
// whose shape the extractor flagged unreliable (HasLambda) is not compared on
// counts: the flag IS the answer, and the consumer abstains.
func matchAny(got CallShape, cands []oracleShape) bool {
	for _, c := range cands {
		if got.HasLambda && c.Lambda {
			return true
		}
		if got.Pos == c.Pos && got.HasStar == c.Star && got.HasKwStar == c.KwStar &&
			eqStrings(got.Kwargs, c.Kwargs) {
			return true
		}
	}
	return false
}

func eqStrings(a, b []string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

func describe(c CallShape) string {
	s := "pos=" + itoa(c.Pos) + " kw=[" + strings.Join(c.Kwargs, " ") + "]"
	if c.HasStar {
		s += " *"
	}
	if c.HasKwStar {
		s += " **"
	}
	if c.HasLambda {
		s += " lambda"
	}
	return s
}

func describeAll(cs []oracleShape) string {
	var parts []string
	for _, c := range cs {
		p := "pos=" + itoa(c.Pos) + " kw=[" + strings.Join(c.Kwargs, " ") + "]"
		if c.Star {
			p += " *"
		}
		if c.KwStar {
			p += " **"
		}
		if c.Lambda {
			p += " lambda"
		}
		parts = append(parts, p)
	}
	return "{" + strings.Join(parts, " | ") + "}"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}
