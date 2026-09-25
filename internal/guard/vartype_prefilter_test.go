package guard

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// prefilterCases pairs each of the four prefiltered regexes — the three in
// goVarTypes/goFuncSignatureIdents (vartype.go) plus reGoVarBinding's twin
// site in goReceiverTypes (recvmethod.go:114) — with the literal substring
// their call site checks via strings.Contains before ever calling the regex
// (findAllIf, util.go). What makes that pre-check safe is that the literal is
// a NECESSARY condition for a match: every match the regex can produce
// contains it, so a line that doesn't contain the literal could never have
// matched anyway. This is what both tests below verify, directly against the
// regex objects the real call sites use — not against a hand-copied twin
// implementation that could drift from them.
var prefilterCases = []struct {
	name string
	re   *regexp.Regexp
	lit  string
}{
	{"reGoVarBinding", reGoVarBinding, "var"},
	{"reGoVarDeclType", reGoVarDeclType, "var"},
	{"reGoCompositeLitBind", reGoCompositeLitBind, ":="},
	{"reGoFuncLine", reGoFuncLine, "func"},
}

// TestPrefilterRegexSourceContainsLiteral is the static half: the literal
// must appear in the regex's own pattern source. Without this, a typo'd
// literal (checking "va" instead of "var", say) could still pass the dynamic
// corpus test below by coincidence, if the corpus never happened to exercise
// the mismatch — this pins the claim itself, not just one sample of it.
func TestPrefilterRegexSourceContainsLiteral(t *testing.T) {
	for _, c := range prefilterCases {
		if !strings.Contains(c.re.String(), c.lit) {
			t.Errorf("%s's source %q does not contain the literal %q it is gated on", c.name, c.re.String(), c.lit)
		}
	}
}

// prefilterAdversarialLines are single lines chosen to probe every prefilter:
// a colon-equals with no "var" (`x:=1`), an unterminated `var(` block, a call
// whose name merely starts with "func" (`funcName()` — must not be read as a
// signature), a line where "var" appears only inside a string literal, and a
// handful of the real binding/non-binding shapes each regex's own doc comment
// discusses.
var prefilterAdversarialLines = []string{
	"x:=1",
	"var(",
	"funcName()",
	`msg := "the variance is high"`, // "var" only inside a string literal
	"var v *Reader",
	"v := &Reader{}",
	"v := Reader{}.Clone()",
	"func f(a, b string, c *T) {",
	"func (r *Reader) Fetch() {",
	"type T struct{}",          // no "var"/":="/"func" at all
	"result, err := Compute()", // ":=" present, not a composite-lit bind
	"var v pkg.T",              // package-qualified, must stay unbound
	"x = &T{}",                 // no ":=" — plain reassignment
}

// prefilterCorpusLines returns the literal-stripped (stripLiteralsStateful,
// via scanStripped — the same per-line scan every prefiltered call site
// consumes) form of every line in prefilterAdversarialLines plus every .go
// file under internal/guard and cmd/runecho-guard: a large, real, genuinely
// adversarial Go corpus (composite literals, multi-line signatures, comments
// mentioning "var"/"func"/":=", raw strings, generics) that needs no
// hand-maintained twin to stay in sync with the source it's drawn from.
func prefilterCorpusLines(t *testing.T) []string {
	t.Helper()
	var lines []AddedLine
	no := 0
	for _, s := range prefilterAdversarialLines {
		no++
		lines = append(lines, AddedLine{LineNo: no, Text: s})
	}
	for _, dir := range []string{".", "../../cmd/runecho-guard"} {
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			no++ // gap before each file so no open-string state leaks across files
			for _, l := range strings.Split(string(src), "\n") {
				no++
				lines = append(lines, AddedLine{LineNo: no, Text: capLine(l)})
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}

	var scans []string
	scanStripped(LangGo, lines, func(scan string, l AddedLine) {
		scans = append(scans, scan)
	})
	return scans
}

// TestPrefilterLiteralIsNecessary is the dynamic half: over the corpus above,
// every match FindAllStringIndex finds for each prefiltered regex must itself
// contain the gating literal. This is exactly the property
// strings.Contains(scan, lit) relies on as a pre-check (findAllIf, util.go)
// — if a regex could ever match text that does not contain its literal,
// gating the regex call on that Contains check would silently skip a real
// match, which is the correctness risk the whole prefilter optimization
// carries.
//
// Replaces an earlier hand-copied "goFuncSignatureIdentsNoPrefilter"/
// "goVarTypesNoPrefilter" oracle — a full second copy of
// goFuncSignatureIdents/goVarTypes kept in sync by hand — with a property
// that tests the regexes directly, so there is no twin implementation left to
// drift out of sync with the real one.
func TestPrefilterLiteralIsNecessary(t *testing.T) {
	corpus := prefilterCorpusLines(t)
	for _, c := range prefilterCases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			checked := 0
			for _, scan := range corpus {
				for _, idx := range c.re.FindAllStringIndex(scan, -1) {
					checked++
					m := scan[idx[0]:idx[1]]
					if !strings.Contains(m, c.lit) {
						t.Errorf("%s matched %q without containing its literal %q (line=%q)", c.name, m, c.lit, scan)
					}
				}
			}
			// Presence is not verification (project gotcha): a property test that
			// never exercises a single real match proves nothing about the claim
			// it's named for. The corpus is large and the adversarial lines were
			// each chosen to match at least one of the four regexes, so zero
			// matches means the corpus regressed, not that the property holds.
			if checked == 0 {
				t.Errorf("%s: corpus produced zero matches — this property test asserted nothing; broaden the corpus", c.name)
			}
		})
	}
}

// FuzzPrefilterLiteralNecessary is the fuzzable form of
// TestPrefilterLiteralIsNecessary's property, seeded with the same
// adversarial lines: for arbitrary single-line input, every match any of the
// four prefiltered regexes produces must contain that regex's gating
// literal. Run one of:
//
//	go test -run=x -fuzz=FuzzPrefilterLiteralNecessary -fuzztime=30s ./internal/guard
func FuzzPrefilterLiteralNecessary(f *testing.F) {
	for _, s := range prefilterAdversarialLines {
		f.Add(s)
	}
	f.Add("var v *Reader // trailing comment mentions var and func")
	f.Add("v := &Set[int]{}")
	f.Add(`x = "func fake(" + "var fake :=" ` + "`raw func var :=`")
	f.Fuzz(func(t *testing.T, line string) {
		scan, _ := stripLiteralsStateful(LangGo, line, "")
		for _, c := range prefilterCases {
			for _, idx := range c.re.FindAllStringIndex(scan, -1) {
				m := scan[idx[0]:idx[1]]
				if !strings.Contains(m, c.lit) {
					t.Fatalf("%s matched %q without containing its literal %q (scan=%q, line=%q)", c.name, m, c.lit, scan, line)
				}
			}
		}
	})
}
