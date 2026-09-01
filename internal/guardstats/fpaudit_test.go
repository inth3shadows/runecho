package guardstats

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeOracle answers from a table keyed by (rev, symbol), so a test can say
// "defined at the old commit but not the new one" without a git tree.
type fakeOracle struct {
	head    string
	revAt   map[string]string // ts.Format(RFC3339) -> rev; "" entry is the default
	defined map[[2]string]bool
	wtErr   error
	revErr  error
	defErr  error
	calls   int
}

func (f *fakeOracle) Worktree(file string) (string, string, error) {
	if f.wtErr != nil {
		return "", "", f.wtErr
	}
	return "/wt", strings.TrimPrefix(file, "/wt/"), nil
}
func (f *fakeOracle) Head(string) (string, error) { return f.head, nil }
func (f *fakeOracle) RevAt(_ string, ts time.Time) (string, error) {
	if f.revErr != nil {
		return "", f.revErr
	}
	if r, ok := f.revAt[ts.UTC().Format(time.RFC3339)]; ok {
		return r, nil
	}
	return f.revAt[""], nil
}
func (f *fakeOracle) Defined(_, rev, _, sym, _ string) (bool, error) {
	f.calls++
	if f.defErr != nil {
		return false, f.defErr
	}
	return f.defined[[2]string{rev, sym}], nil
}

func auditTS(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func auditAsk(at, file, reason string, syms []string, learn []string) Decision {
	return Decision{
		TS: auditTS(at), Mode: "hook", Decision: "ask", File: file, Lang: "py",
		Reason: reason, Symbols: syms, LearnSymbols: learn,
	}
}

func verdictOf(t *testing.T, s AuditStats, sym string) AuditVerdict {
	t.Helper()
	for _, f := range s.Findings {
		if f.Symbol == sym {
			return f.Verdict
		}
	}
	t.Fatalf("no finding for %q", sym)
	return ""
}

func TestAuditThreeVerdicts(t *testing.T) {
	o := &fakeOracle{
		head:  "HEAD",
		revAt: map[string]string{"": "OLD"},
		defined: map[[2]string]bool{
			// already there when we complained -> resolver bug
			{"OLD", "alreadyThere"}:  true,
			{"HEAD", "alreadyThere"}: true,
			// arrived afterwards -> guard was right, fired early
			{"HEAD", "writtenLater"}: true,
			// "neverWritten" is absent from both -> the flag stands
		},
	}
	in := []Decision{
		auditAsk("2026-07-10T10:00:00Z", "/wt/a.py", "violations",
			[]string{"alreadyThere", "writtenLater", "neverWritten"},
			[]string{"alreadyThere", "writtenLater", "neverWritten"}),
	}
	s := Audit(in, auditTS("2026-07-01T00:00:00Z"), o)

	if got, want := verdictOf(t, s, "alreadyThere"), VerdictFP; got != want {
		t.Errorf("alreadyThere = %q, want %q", got, want)
	}
	if got, want := verdictOf(t, s, "writtenLater"), VerdictPremature; got != want {
		t.Errorf("writtenLater = %q, want %q", got, want)
	}
	if got, want := verdictOf(t, s, "neverWritten"), VerdictStands; got != want {
		t.Errorf("neverWritten = %q, want %q", got, want)
	}
	if s.Asks != 1 || s.Symbols != 3 || s.Rated() != 3 {
		t.Errorf("asks/symbols/rated = %d/%d/%d, want 1/3/3", s.Asks, s.Symbols, s.Rated())
	}
}

// A symbol defined at ask time is FP even though it is also defined now. Asking
// HEAD first and reading "defined now" as the FP signal is the mistake that made
// hashToSeed look like a false positive; this pins the order.
func TestAuditFPWinsOverPremature(t *testing.T) {
	o := &fakeOracle{
		head:  "HEAD",
		revAt: map[string]string{"": "OLD"},
		defined: map[[2]string]bool{
			{"OLD", "x"}: true, {"HEAD", "x"}: true,
		},
	}
	s := Audit([]Decision{
		auditAsk("2026-07-10T10:00:00Z", "/wt/a.py", "violations", []string{"x"}, []string{"x"}),
	}, auditTS("2026-07-01T00:00:00Z"), o)
	if got := verdictOf(t, s, "x"); got != VerdictFP {
		t.Errorf("verdict = %q, want %q", got, VerdictFP)
	}
}

// duplicate-symbol and dangling flag names that ARE defined. Judging them by
// "does it resolve" scores every correct catch as a resolver bug — the first run
// of this audit reported 27 fp / 0 stands for duplicate-symbol before VerdictNA.
func TestAuditNonResolutionChecksAreNotJudged(t *testing.T) {
	o := &fakeOracle{
		head: "HEAD", revAt: map[string]string{"": "OLD"},
		defined: map[[2]string]bool{{"OLD", "dupe"}: true, {"HEAD", "dupe"}: true},
	}
	for _, reason := range []string{"duplicate-symbol", "dangling", "file-scope", "call-shape"} {
		s := Audit([]Decision{
			// learn_symbols empty (not nil): the guard says none of these came
			// from the hallucination check.
			{TS: auditTS("2026-07-10T10:00:00Z"), Mode: "hook", Decision: "ask", File: "/wt/a.py",
				Lang: "py", Reason: reason, Symbols: []string{"dupe"}, LearnSymbols: []string{}},
		}, auditTS("2026-07-01T00:00:00Z"), o)
		if got := verdictOf(t, s, "dupe"); got != VerdictNA {
			t.Errorf("reason %q: verdict = %q, want %q", reason, got, VerdictNA)
		}
		if s.Rated() != 0 {
			t.Errorf("reason %q: rated = %d, want 0", reason, s.Rated())
		}
	}
}

// A mixed ask judges only the hallucination-origin subset the guard named.
func TestAuditMixedAskJudgesOnlyLearnSymbols(t *testing.T) {
	o := &fakeOracle{head: "HEAD", revAt: map[string]string{"": "OLD"}}
	s := Audit([]Decision{
		auditAsk("2026-07-10T10:00:00Z", "/wt/a.py", "violations+duplicate-symbol",
			[]string{"halluc", "dupe"}, []string{"halluc"}),
	}, auditTS("2026-07-01T00:00:00Z"), o)
	if got := verdictOf(t, s, "halluc"); got != VerdictStands {
		t.Errorf("halluc = %q, want %q", got, VerdictStands)
	}
	if got := verdictOf(t, s, "dupe"); got != VerdictNA {
		t.Errorf("dupe = %q, want %q", got, VerdictNA)
	}
}

// Pre-#29 records carry no learn_symbols. A reason naming only the hallucination
// check is enough; a mixed reason is not, and must not be guessed at.
func TestAuditLegacyRecordsWithoutLearnSymbols(t *testing.T) {
	o := &fakeOracle{head: "HEAD", revAt: map[string]string{"": "OLD"}}
	legacy := func(reason string) Decision {
		return Decision{TS: auditTS("2026-07-10T10:00:00Z"), Mode: "hook", Decision: "ask",
			File: "/wt/a.py", Lang: "py", Reason: reason, Symbols: []string{"s"}}
	}
	if got := verdictOf(t, Audit([]Decision{legacy("violations")}, auditTS("2026-07-01T00:00:00Z"), o), "s"); got != VerdictStands {
		t.Errorf("legacy violations = %q, want %q", got, VerdictStands)
	}
	if got := verdictOf(t, Audit([]Decision{legacy("violations+duplicate-symbol")}, auditTS("2026-07-01T00:00:00Z"), o), "s"); got != VerdictNA {
		t.Errorf("legacy mixed = %q, want %q", got, VerdictNA)
	}
}

// One edit seen by plugin + user settings + project settings writes the same ask
// two or three times within a second. Counting them weights a verdict by how
// many hook wirings the machine happened to have.
func TestAuditCollapsesDuplicateHookFires(t *testing.T) {
	o := &fakeOracle{head: "HEAD", revAt: map[string]string{"": "OLD"}}
	in := []Decision{
		auditAsk("2026-07-10T10:00:00Z", "/wt/a.py", "violations", []string{"x"}, []string{"x"}),
		auditAsk("2026-07-10T10:00:00Z", "/wt/a.py", "violations", []string{"x"}, []string{"x"}),
		auditAsk("2026-07-10T10:00:02Z", "/wt/a.py", "violations", []string{"x"}, []string{"x"}),
		// 61s later, same symbol: a genuine re-ask, not a duplicate wiring.
		auditAsk("2026-07-10T10:01:03Z", "/wt/a.py", "violations", []string{"x"}, []string{"x"}),
	}
	s := Audit(in, auditTS("2026-07-01T00:00:00Z"), o)
	if s.Asks != 2 {
		t.Errorf("asks = %d, want 2 (three fires collapsed, one genuine re-ask)", s.Asks)
	}
	if s.Symbols != 2 {
		t.Errorf("symbols = %d, want 2", s.Symbols)
	}
}

// A differing symbol list inside the window is a different ask, not a re-log.
func TestAuditDuplicateCollapseIsSymbolExact(t *testing.T) {
	o := &fakeOracle{head: "HEAD", revAt: map[string]string{"": "OLD"}}
	s := Audit([]Decision{
		auditAsk("2026-07-10T10:00:00Z", "/wt/a.py", "violations", []string{"x"}, []string{"x"}),
		auditAsk("2026-07-10T10:00:01Z", "/wt/a.py", "violations", []string{"y"}, []string{"y"}),
	}, auditTS("2026-07-01T00:00:00Z"), o)
	if s.Asks != 2 {
		t.Errorf("asks = %d, want 2", s.Asks)
	}
}

// A precommit ask carries no file at all, so it has no worktree to resolve and
// no dated question to answer. 145 of the 648 asks in the reference window are
// precommit; counting them anywhere but their own line repeats the denominator
// error this audit exists to correct.
func TestAuditSkipsPrecommitAndNonAsk(t *testing.T) {
	o := &fakeOracle{head: "HEAD", revAt: map[string]string{"": "OLD"}}
	in := []Decision{
		{TS: auditTS("2026-07-10T10:00:00Z"), Mode: "precommit", Decision: "ask", Repo: "r",
			Reason: "violations", Symbols: []string{"x"}},
		{TS: auditTS("2026-07-10T10:00:01Z"), Mode: "hook", Decision: "defer", File: "/wt/a.py",
			Reason: "clean"},
		{TS: auditTS("2026-07-10T10:00:02Z"), Mode: "hook", Decision: "outcome", File: "/wt/a.py",
			Reason: "approved", Symbols: []string{"x"}},
	}
	s := Audit(in, auditTS("2026-07-01T00:00:00Z"), o)
	if s.Asks != 0 || s.Symbols != 0 {
		t.Errorf("asks/symbols = %d/%d, want 0/0", s.Asks, s.Symbols)
	}
}

func TestAuditRespectsWindow(t *testing.T) {
	o := &fakeOracle{head: "HEAD", revAt: map[string]string{"": "OLD"}}
	s := Audit([]Decision{
		auditAsk("2026-06-01T10:00:00Z", "/wt/a.py", "violations", []string{"old"}, []string{"old"}),
		auditAsk("2026-07-10T10:00:00Z", "/wt/a.py", "violations", []string{"new"}, []string{"new"}),
	}, auditTS("2026-07-01T00:00:00Z"), o)
	if s.Asks != 1 {
		t.Errorf("asks = %d, want 1", s.Asks)
	}
	if got := verdictOf(t, s, "new"); got != VerdictStands {
		t.Errorf("new = %q", got)
	}
}

// A vanished worktree or an unanswerable oracle is reported, never folded into
// a verdict and never dropped.
func TestAuditUnknownIsReportedNotAbsorbed(t *testing.T) {
	for name, o := range map[string]*fakeOracle{
		"worktree gone": {wtErr: errors.New("worktree gone")},
		"no commit":     {head: "HEAD", revErr: errors.New("no commit at or before")},
		"grep failed":   {head: "HEAD", revAt: map[string]string{"": "OLD"}, defErr: errors.New("git grep failed")},
	} {
		s := Audit([]Decision{
			auditAsk("2026-07-10T10:00:00Z", "/wt/a.py", "violations", []string{"x"}, []string{"x"}),
		}, auditTS("2026-07-01T00:00:00Z"), o)
		if got := verdictOf(t, s, "x"); got != VerdictUnknown {
			t.Errorf("%s: verdict = %q, want %q", name, got, VerdictUnknown)
		}
		if s.Rated() != 0 {
			t.Errorf("%s: rated = %d, want 0 — unknown must not enter the denominator", name, s.Rated())
		}
		if s.Symbols != 1 {
			t.Errorf("%s: symbols = %d, want 1 — unknown must still be counted", name, s.Symbols)
		}
		if f := s.Findings[0]; f.Note == "" {
			t.Errorf("%s: unknown finding carries no note", name)
		}
	}
}

// Share must divide by the rated pairs, not by every flagged symbol: an audit
// that let unknown and n/a into the denominator would report a falling FP rate
// simply by losing coverage.
func TestAuditShareExcludesUnratedFromDenominator(t *testing.T) {
	o := &fakeOracle{
		head: "HEAD", revAt: map[string]string{"": "OLD"},
		defined: map[[2]string]bool{{"OLD", "fp1"}: true},
	}
	s := Audit([]Decision{
		auditAsk("2026-07-10T10:00:00Z", "/wt/a.py", "violations", []string{"fp1", "stands1"}, []string{"fp1", "stands1"}),
		{TS: auditTS("2026-07-10T10:05:00Z"), Mode: "hook", Decision: "ask", File: "/wt/a.py",
			Lang: "py", Reason: "duplicate-symbol", Symbols: []string{"na1"}, LearnSymbols: []string{}},
	}, auditTS("2026-07-01T00:00:00Z"), o)
	if s.Symbols != 3 || s.Rated() != 2 {
		t.Fatalf("symbols/rated = %d/%d, want 3/2", s.Symbols, s.Rated())
	}
	if got := s.Share(VerdictFP); got != 0.5 {
		t.Errorf("Share(fp) = %v, want 0.5", got)
	}
}

// The same symbol is re-flagged many times in a session (hashToSeed alone
// accounts for eight records in one morning); each distinct question should
// reach the oracle once.
func TestAuditMemoisesOracleCalls(t *testing.T) {
	o := &fakeOracle{head: "HEAD", revAt: map[string]string{"": "OLD"}}
	var in []Decision
	for i := 0; i < 5; i++ {
		// 61s apart so the duplicate-fire collapse does not do this for us.
		in = append(in, auditAsk(auditTS("2026-07-10T10:00:00Z").Add(time.Duration(i)*61*time.Second).Format(time.RFC3339),
			"/wt/a.py", "violations", []string{"x"}, []string{"x"}))
	}
	s := Audit(in, auditTS("2026-07-01T00:00:00Z"), o)
	if s.Asks != 5 {
		t.Fatalf("asks = %d, want 5", s.Asks)
	}
	// Two questions (OLD, HEAD) for one (root, lang, symbol, file) pair.
	if o.calls != 2 {
		t.Errorf("oracle calls = %d, want 2", o.calls)
	}
}

func TestFormatAuditRendersAndDoesNotPanicOnEmpty(t *testing.T) {
	empty := Audit(nil, auditTS("2026-07-01T00:00:00Z"), &fakeOracle{})
	if out := FormatAudit(empty); out == "" {
		t.Error("FormatAudit returned empty string for an empty audit")
	}
	o := &fakeOracle{head: "HEAD", revAt: map[string]string{"": "OLD"},
		defined: map[[2]string]bool{{"OLD", "x"}: true}}
	s := Audit([]Decision{
		auditAsk("2026-07-10T10:00:00Z", "/wt/a.py", "violations", []string{"x"}, []string{"x"}),
	}, auditTS("2026-07-01T00:00:00Z"), o)
	out := FormatAudit(s)
	for _, want := range []string{"fp", "premature", "stands", "By check:", "By language:"} {
		if !contains(out, want) {
			t.Errorf("FormatAudit output missing %q:\n%s", want, out)
		}
	}
	p := PayloadAudit(s)
	if p["rated"].(int) != 1 {
		t.Errorf("payload rated = %v, want 1", p["rated"])
	}
	if len(p["findings"].([]map[string]any)) != 1 {
		t.Errorf("payload findings = %v", p["findings"])
	}
}

func contains(hay, needle string) bool {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// The guard emits one entry per violating LINE, so one ask can list the same
// name several times (7 of the 1,028 real ask records do; one lists 19 entries
// for 15 names). Every copy resolves identically, so counting them weights the
// verdict by how often the agent happened to call the symbol.
func TestAuditDedupesRepeatedSymbolsWithinOneAsk(t *testing.T) {
	o := &fakeOracle{head: "HEAD", revAt: map[string]string{"": "OLD"}}
	s := Audit([]Decision{
		auditAsk("2026-07-10T10:00:00Z", "/wt/a.py", "violations",
			[]string{"dup", "dup", "dup", "other"}, []string{"dup", "other"}),
	}, auditTS("2026-07-01T00:00:00Z"), o)
	if s.Symbols != 2 || s.Rated() != 2 {
		t.Errorf("symbols/rated = %d/%d, want 2/2 — repeats must collapse", s.Symbols, s.Rated())
	}
	if n := len(s.Findings); n != 2 {
		t.Errorf("findings = %d, want 2", n)
	}
}

// An empty window must not render a zero-time header ("0001-01-01").
func TestAuditUntilNeverZero(t *testing.T) {
	s := Audit(nil, auditTS("2026-08-01T00:00:00Z"), &fakeOracle{})
	if s.Until.IsZero() {
		t.Fatal("Until is the zero time; the report header would read 0001-01-01")
	}
	if contains(FormatAudit(s), "0001-01-01") {
		t.Errorf("header renders the zero time:\n%s", FormatAudit(s))
	}
}

// failIfDefinedOracle wraps a fakeOracle but fails the test the moment
// Defined is called. Used to pin that the not-an-identifier gate SHORT-
// CIRCUITS classify — not merely that it produces the right label after
// consulting git, but that it never asks git at all (issue #360).
type failIfDefinedOracle struct {
	*fakeOracle
	t *testing.T
}

func (f *failIfDefinedOracle) Defined(root, rev, lang, sym, rel string) (bool, error) {
	f.t.Fatalf("Defined(%q) called — the not-an-identifier gate should have short-circuited before any oracle lookup", sym)
	return false, nil
}

func auditAskLang(at, file, lang, reason string, syms []string, learn []string) Decision {
	return Decision{
		TS: auditTS(at), Mode: "hook", Decision: "ask", File: file, Lang: lang,
		Reason: reason, Symbols: syms, LearnSymbols: learn,
	}
}

// A JS literal that can never be a declaration (NaN) must classify as
// not-an-identifier WITHOUT ever calling the oracle. This is the short-circuit
// itself, not just the resulting label — a test that only checked the verdict
// would still pass if the gate ran the git lookups and then relabeled the
// result, which defeats the "needs no git lookup" point of the gate.
func TestClassifyNotIdentShortCircuitsOracle(t *testing.T) {
	base := &fakeOracle{head: "HEAD", revAt: map[string]string{"": "OLD"}}
	o := &failIfDefinedOracle{fakeOracle: base, t: t}
	in := []Decision{
		auditAskLang("2026-07-10T10:00:00Z", "/wt/a.ts", "js", "violations",
			[]string{"NaN"}, []string{"NaN"}),
	}
	s := Audit(in, auditTS("2026-07-01T00:00:00Z"), o)
	if got, want := verdictOf(t, s, "NaN"), VerdictNotIdent; got != want {
		t.Errorf("NaN = %q, want %q", got, want)
	}
}

// Table names and SQL-keyword leaks (SUM, COALESCE, EXISTS) are NOT JS
// keywords — calling them not-an-identifier IN JS would misclassify a real
// JS function legitimately named `sum` or `exists`. Only exact matches
// against the flagged language's own reserved-word/literal set qualify; a
// same-spelled SQL keyword must fall through to the ordinary oracle path.
func TestNotIdentIsNotASQLKeywordList(t *testing.T) {
	for _, sym := range []string{"sum", "SUM", "exists", "EXISTS", "coalesce", "COALESCE"} {
		if isNotIdentifier("js", sym) {
			t.Errorf("isNotIdentifier(js, %q) = true, want false — not a JS reserved word", sym)
		}
	}
	// Sanity: a real JS function named `exists` still resolves normally
	// through the oracle (i.e. reaches VerdictStands/VerdictFP, not
	// VerdictNotIdent) rather than being silently misjudged.
	o := &fakeOracle{head: "HEAD", revAt: map[string]string{"": "OLD"}}
	s := Audit([]Decision{
		auditAskLang("2026-07-10T10:00:00Z", "/wt/a.ts", "js", "violations",
			[]string{"exists"}, []string{"exists"}),
	}, auditTS("2026-07-01T00:00:00Z"), o)
	if got := verdictOf(t, s, "exists"); got != VerdictStands {
		t.Errorf("exists = %q, want %q (must go through the oracle, not the not-ident gate)", got, VerdictStands)
	}
}

// nil is a Go builtin but a legal Python identifier, and None is the reverse.
// The membership test must be keyed on the flagged language's own tag, not a
// language-agnostic union of every language's reserved words.
func TestNotIdentIsPerLanguage(t *testing.T) {
	cases := []struct {
		lang, sym string
		want      bool
	}{
		{"go", "nil", true},
		{"py", "nil", false},
		{"py", "None", true},
		{"go", "None", false},
	}
	for _, c := range cases {
		if got := isNotIdentifier(c.lang, c.sym); got != c.want {
			t.Errorf("isNotIdentifier(%q, %q) = %v, want %v", c.lang, c.sym, got, c.want)
		}
	}
}

// VerdictNotIdent is a judged, wrong flag — it must stay inside Rated(), the
// same way fp/premature/stands do. Excluding it (like VerdictUnknown/
// VerdictNA) would move the symbol out of `stands` and leave the reported FP
// rate exactly as understated as before this verdict existed.
func TestRatedIncludesNotIdent(t *testing.T) {
	o := &fakeOracle{head: "HEAD", revAt: map[string]string{"": "OLD"}}
	s := Audit([]Decision{
		auditAskLang("2026-07-10T10:00:00Z", "/wt/a.ts", "js", "violations",
			[]string{"NaN", "realSym"}, []string{"NaN", "realSym"}),
	}, auditTS("2026-07-01T00:00:00Z"), o)
	if s.Symbols != 2 {
		t.Fatalf("Symbols = %d, want 2", s.Symbols)
	}
	if s.Rated() != 2 {
		t.Errorf("Rated() = %d, want 2 (not-an-identifier must not be excluded like unknown/n-a)", s.Rated())
	}
	if s.Counts[VerdictNotIdent] != 1 {
		t.Errorf("Counts[VerdictNotIdent] = %d, want 1", s.Counts[VerdictNotIdent])
	}
}
