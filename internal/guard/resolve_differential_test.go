// Package guard_test holds the compiler-oracle differential: the guard's
// resolution verdicts adjudicated by the Go compiler rather than by a
// hand-written corpus.
//
// WHY THIS EXISTS. Every other way this repo identifies guard defects derives
// "correct" from RunEcho itself — the testdata corpora, the hook fixtures in
// bench/hookmutate, the fuzz seeds, and fpaudit/fpreport (whose oracle is git
// history). The one exception, callshapecheck_differential_test.go, is
// adjudicated by CPython's ast and is also the method that produced hard defect
// numbers at scale. This generalises that exception to the guard's PRIMARY
// claim: "identifier X does not resolve."
//
// Two directions, both machine-proven, no human adjudication:
//
//   - FALSE POSITIVE. `go build` succeeding on a package is proof that every
//     identifier in it resolves. So on a clean-building package, every violation
//     the guard reports is a proven false positive. Nothing to argue about.
//
//   - FALSE NEGATIVE. Rewriting a use site to a name provably absent from the
//     package makes the compiler report `undefined:` there. Guard silence on
//     that line is then a proven false negative. This is the half NO existing
//     method can measure: a corpus only contains violations someone already
//     thought of, and fpaudit only sees decisions the guard already emitted.
//
// The compiler, not the site-selection heuristic, is the judge. A mutation the
// compiler does not complain about is DISCARDED and counted, never assumed to
// be a defect — the selection logic is allowed to be imperfect precisely
// because it is not trusted.
//
// SCOPE. Three checks answer "does this Go identifier resolve", and all three
// are exercised: guard.Run (bare references), GoQualifiedViolations (same-module
// `pkg.Fn`), and GoDepQualifiedViolations (external and stdlib `pkg.Fn`). The
// file-scope, dangling-import and call-shape checks answer different questions
// and are out of scope here. The two qualified checks ship behind env gates and
// are run anyway, because the question is what the code CAN see; where that
// differs from the default install, the report says so.
//
// POPULATION. A Go file can reference a name the compiler must resolve in three
// shapes, and each belongs to a different check — or to none. Run deliberately
// ignores unexported Go references (extract.go: "the IR only indexes exported
// symbols"), so silence on a lowercase bare call is documented policy, not a
// defect. Mutations are therefore stratified by shape and reported per shape.
// A single blended rate would both hide which shape is blind and manufacture a
// near-100% "false negative" number that means nothing. Only the bare-exported
// shape — the population Run declares it checks — fails the test when missed.
//
// Corpus defaults to this repository — a real, always-present Go module, so the
// test has something to say with zero setup. Point RUNECHO_ORACLE_CORPUS at
// another module to run it at scale.
package guard_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	goparser "go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/inth3shadows/runecho/internal/depindex"
	"github.com/inth3shadows/runecho/internal/guard"
	"github.com/inth3shadows/runecho/internal/ir"
)

// mutationSuffix is appended to an identifier to produce the fresh name. It is
// checked for absence from the whole package before use, so the only way a
// mutated reference resolves is if the compiler disagrees with that check — in
// which case the case is discarded, not counted.
const mutationSuffix = "Zq7Runecho"

// defaultMutations bounds phase 2, which pays one package build per mutation.
// Overridable via RUNECHO_ORACLE_MUTATIONS. The effective cap and the number of
// eligible sites are always logged: a truncated run must never read as full
// coverage.
const defaultMutations = 40

// ---------------------------------------------------------------------------
// corpus + oracle plumbing
// ---------------------------------------------------------------------------

// corpusRoot resolves the module under test: RUNECHO_ORACLE_CORPUS, else this
// repository (two levels up from internal/guard).
func corpusRoot(t *testing.T) string {
	t.Helper()
	if r := os.Getenv("RUNECHO_ORACLE_CORPUS"); r != "" {
		abs, err := filepath.Abs(r)
		if err != nil {
			t.Fatalf("resolve RUNECHO_ORACLE_CORPUS: %v", err)
		}
		return abs
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve default corpus: %v", err)
	}
	return root
}

// goPkg is the subset of `go list -json` this harness needs. GoFiles excludes
// test files and files dropped by build constraints, which keeps the population
// identical to what `go build` actually type-checks — the whole basis of the
// false-positive proof.
type goPkg struct {
	Dir        string
	ImportPath string
	GoFiles    []string
	Incomplete bool
	// Imports is every package this one imports (stdlib, same-module, and
	// external), used only by the #314 dependency-resolution accounting below —
	// not by the false-positive/false-negative adjudication, which works from
	// GoFiles' own content.
	Imports []string
}

// listPackages enumerates the module's buildable packages. A `go list` that
// cannot run at all (no toolchain, no module) skips the test: an oracle that
// cannot speak is no evidence either way, and must not fail the code under test.
func listPackages(t *testing.T, root string) []goPkg {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH — the differential oracle is the Go compiler")
	}
	cmd := exec.Command("go", "list", "-json", "./...")
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("go list failed in %s: %v\n%s", root, err, stderr.String())
	}
	var pkgs []goPkg
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var p goPkg
		if err := dec.Decode(&p); err != nil {
			t.Fatalf("decode go list output: %v", err)
		}
		if p.Incomplete || len(p.GoFiles) == 0 {
			continue
		}
		pkgs = append(pkgs, p)
	}
	return pkgs
}

// reUndefined matches the compiler's unresolved-identifier diagnostic in both
// spellings the toolchain has used ("undefined: X" today, "undeclared name: X"
// in older releases), so the oracle does not silently stop finding anything
// after a toolchain bump.
var reUndefined = regexp.MustCompile(`^(.*?):(\d+):\d+: (?:undefined|undeclared name): ([A-Za-z_][A-Za-z0-9_.]*)`)

// reNoMember is the compiler's other spelling of an unresolved reference: a
// selector whose base resolves but whose member does not. Mutating `x.Foo(` to
// `x.FooZq7(` produces this rather than "undefined:", so an oracle that knew
// only the pattern above would silently score every selector mutation as
// "compiler had no complaint" and discard the entire shape.
var reNoMember = regexp.MustCompile(`^(.*?):(\d+):\d+: ([A-Za-z_][A-Za-z0-9_.]*) undefined \(type `)

// undefinedRef is one unresolved identifier as reported by the compiler.
type undefinedRef struct {
	File string // absolute
	Line int
	Name string
}

// buildPackage compiles pkgPath inside root and returns the unresolved-identifier
// diagnostics. ok reports whether the package built cleanly.
//
// -gcflags=-e disables the ten-errors-per-package limit: a capped error list
// would make a real false negative look like a clean build.
func buildPackage(t *testing.T, root, pkgPath string) (ok bool, refs []undefinedRef) {
	t.Helper()
	cmd := exec.Command("go", "build", "-gcflags=-e", "-o", os.DevNull, pkgPath)
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		return true, nil
	}
	sc := bufio.NewScanner(&stderr)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		text := strings.TrimSpace(sc.Text())
		m := reUndefined.FindStringSubmatch(text)
		if m == nil {
			m = reNoMember.FindStringSubmatch(text)
		}
		if m == nil {
			continue
		}
		line, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		file := m[1]
		if !filepath.IsAbs(file) {
			file = filepath.Join(root, file)
		}
		refs = append(refs, undefinedRef{File: file, Line: line, Name: m[3]})
	}
	return false, refs
}

// ---------------------------------------------------------------------------
// guard invocation — must match what the hook builds, or the numbers are fiction
// ---------------------------------------------------------------------------

// knownSet reproduces the symbol set the hook loads from the IR snapshot.
// SymbolsForLatestSnapshot (internal/snapshot/db.go) reads the `symbols` table,
// which snapshot.go fills from FileIR.Symbols[].Name — so generating the IR and
// flattening the same field yields the same set without standing up a database.
func knownSet(t *testing.T, root string) map[string]struct{} {
	t.Helper()
	// GenerateTimeout < 0 disables the default wall-clock ceiling: a large corpus
	// must not silently index a partial tree and inflate the false-positive count
	// with symbols that were merely never reached.
	gen := ir.NewGenerator(ir.GeneratorConfig{GenerateTimeout: -1})
	irData, stats, err := gen.Generate(root)
	if err != nil {
		t.Skipf("IR generation failed for %s: %v", root, err)
	}
	if stats.SupportedSeen > 0 && stats.Coverage() < 100 {
		t.Logf("NOTE: IR coverage %.1f%% (%d/%d files, %d parse errors) — "+
			"symbols missing from unindexed files can present as false positives",
			stats.Coverage(), stats.Indexed, stats.SupportedSeen, stats.ParseErrors)
	}
	known := make(map[string]struct{})
	for _, f := range irData.Files {
		for _, s := range f.Symbols {
			known[s.Name] = struct{}{}
		}
	}
	return known
}

// runGuardOnFile runs the guard over text in the posture a whole-file Write
// produces: the entire file is the added text, and the same text supplies the
// in-file definition fold. guard.FoldInFileDefs is the exact function the hook
// calls (cmd/runecho-guard's addInFileDefs delegates to it), so this known set
// is the hook's known set rather than a lookalike.
// A posture is one shape of edit the guard is actually asked about. Measuring
// only one of them is how the first cut of this harness reported "zero false
// positives" while two default-on paths were broken: it passed the whole file as
// BOTH the added text and the fold source, so the known set always already
// contained every declaration in the file — a condition neither the hunk-scoped
// hook path nor the pre-commit path ever satisfies.
//
// The fold source is the variable that matters. In hook mode it is the PRE-EDIT
// file on disk, which by definition does not contain what this edit is adding.
type posture struct {
	name string
	// fold is the text folded into the known set via FoldInFileDefs — the hook's
	// pre-edit file. Empty means no fold at all.
	fold string
	// added is the text presented as the edit.
	added string
}

// posturesFor returns the edit shapes to measure for one file.
func posturesFor(text string, lang guard.Lang) []posture {
	out := []posture{
		// A Write of the whole file, where the pre-edit file is the same text.
		{name: "write-whole", fold: text, added: text},
		// A Write CREATING the file: readFileLines returns nil, so nothing is
		// folded. This is also the pre-commit path's posture, which calls
		// guard.Run with the store's symbols and no fold at all.
		{name: "write-new", fold: "", added: text},
	}
	if lang != guard.LangGo {
		return out
	}
	// An Edit that inserts a whole function: the hunk is the function, and the
	// pre-edit file is everything else. Bindings inside the hunk are therefore
	// absent from the fold — exactly the case a model produces when it writes a
	// new function, and the one that whole-file measurement cannot see.
	for _, fn := range goTopLevelFuncs(text) {
		out = append(out, posture{name: "edit-hunk", fold: fn.rest, added: fn.body})
	}
	return out
}

type goFunc struct{ body, rest string }

// goTopLevelFuncs splits text into each top-level func declaration and the file
// with that declaration removed. Brace counting on a literal-masked scan; a
// function whose braces do not balance is skipped rather than guessed at.
func goTopLevelFuncs(text string) []goFunc {
	lines := strings.Split(text, "\n")
	// Literal state at the START of each line. A `func ...` at column zero inside
	// a backtick raw string is not a declaration — x/text embeds Go templates in
	// raw strings, and treating one as a function produced a bogus hunk that this
	// harness then reported as a product false positive. The harness must not
	// invent the defects it measures.
	startsOpen := make([]string, len(lines))
	open := ""
	for i, l := range lines {
		startsOpen[i] = open
		_, open = guard.StripLiteralsForTest(guard.LangGo, l, open)
	}
	var out []goFunc
	for i, l := range lines {
		if startsOpen[i] != "" {
			continue
		}
		if !strings.HasPrefix(l, "func ") && !strings.HasPrefix(l, "func(") {
			continue
		}
		depth, end := 0, -1
		open := ""
		for j := i; j < len(lines); j++ {
			scan, newOpen := guard.StripLiteralsForTest(guard.LangGo, lines[j], open)
			open = newOpen
			depth += strings.Count(scan, "{") - strings.Count(scan, "}")
			if depth == 0 && strings.ContainsRune(scan, '}') {
				end = j
				break
			}
		}
		if end < 0 {
			continue
		}
		body := strings.Join(lines[i:end+1], "\n")
		rest := strings.Join(append(append([]string{}, lines[:i]...), lines[end+1:]...), "\n")
		out = append(out, goFunc{body: body, rest: rest})
	}
	return out
}

// modulePath and depIdx are "" / nil outside the Go false-positive arm (every
// other caller of runGuardOnFile predates #314 and passes zero values, which
// disables GoQualifiedViolations/GoDepQualifiedViolations exactly as an empty
// modulePath already does inside those functions).
func runGuardOnFile(known map[string]struct{}, relPath, text, modulePath string, depIdx depindex.Index) []attributed {
	lang := guard.LangFor(relPath)
	var out []attributed
	for _, p := range posturesFor(text, lang) {
		added := guard.TextToAddedLines(p.added)
		foldLines := guard.TextToAddedLines(p.fold)
		symbols := make(map[string]struct{}, len(known)+16)
		for s := range known {
			symbols[s] = struct{}{}
		}
		if p.fold != "" {
			guard.FoldInFileDefs(symbols, foldLines, lang)
		}
		for _, v := range guard.Run(symbols, "", []guard.FileDiff{{Path: relPath, AddedLines: added}}) {
			out = append(out, attributed{check: "Run", posture: p.name, v: v})
		}
		// Every check measured here — receiver-method, and since #314 the two
		// qualified-call checks — shares the same reasoning: a check whose
		// false-positive cost has not been measured on foreign code is not ready
		// to ship regardless of how many true positives it finds.
		if lang == guard.LangGo {
			for _, v := range guard.GoReceiverMethodViolations(foldLines, added, symbols) {
				out = append(out, attributed{check: "GoReceiverMethod", posture: p.name, v: v})
			}
			if modulePath != "" {
				for _, v := range guard.GoQualifiedViolations(foldLines, added, symbols, modulePath) {
					out = append(out, attributed{check: "GoQualified", posture: p.name, v: v})
				}
				if depIdx != nil {
					for _, v := range guard.GoDepQualifiedViolations(foldLines, added, modulePath, depIdx) {
						out = append(out, attributed{check: "GoDepQualified", posture: p.name, v: v})
					}
				}
			}
		}
	}
	return out
}

// attributed is a violation plus the check and posture that produced it, so a
// false positive names its own culprit instead of being pooled.
type attributed struct {
	check   string
	posture string
	v       guard.Violation
}

// ---------------------------------------------------------------------------
// Phase 1 — false positives, proven by a clean build
// ---------------------------------------------------------------------------

func TestGoResolveNoFalsePositivesAgainstCompiler(t *testing.T) {
	root := corpusRoot(t)
	pkgs := listPackages(t, root)
	if len(pkgs) == 0 {
		t.Skipf("no buildable Go packages under %s", root)
	}
	known := knownSet(t, root)
	// #314: built once for the whole corpus, exactly as TestGoResolveFalseNegativesAgainstCompiler
	// does — this is what puts GoQualifiedViolations/GoDepQualifiedViolations under
	// the same false-positive proof every other check here already has. Before
	// #314 neither was measured on this arm at all: TECHNICAL.md's "0 proven false
	// positives" line covered only guard.Run and GoReceiverMethod.
	modulePath := guard.GoModulePath(root)
	depIdx := depindex.NewGoIndex(root)

	type fp struct {
		file, symbol, check, posture string
		line                         int
	}
	var fps []fp
	var filesChecked, linesChecked, pkgsSkipped int

	// depAccounting answers #314's other open question — GoDepQualified's
	// abstention rate — by resolving every package's actual import list against
	// the same depIdx the check itself consults. This is a PACKAGE-level
	// population (once per unique import per package), not the check's own
	// call-site population (which additionally requires an alias to appear only
	// as a `q.` selector in that file, never bare) — so it is an upper bound on
	// how often the check could fire, not an exact count of call sites. Still the
	// right number for "is there anything here for the check to abstain FROM":
	// a package-level resolve failure means every call-site the check would ever
	// look at inside that import necessarily abstains too.
	sameRepo, depResolved, depAbstained := 0, 0, 0
	abstainReasons := map[string]int{}
	seenImport := map[string]bool{}
	for _, p := range pkgs {
		for _, imp := range p.Imports {
			if seenImport[imp] {
				continue
			}
			seenImport[imp] = true
			if modulePath != "" && (imp == modulePath || strings.HasPrefix(imp, modulePath+"/")) {
				sameRepo++
				continue
			}
			if !strings.Contains(strings.SplitN(imp, "/", 2)[0], ".") {
				continue // stdlib (no dot in the first path segment) — not GoDepQualified's population
			}
			ps := depIdx.Lookup(imp)
			if ps.Res == depindex.Resolved {
				depResolved++
			} else {
				depAbstained++
				abstainReasons[ps.Res.String()]++
			}
		}
	}

	for _, p := range pkgs {
		// A package that does not compile cannot adjudicate anything: some of its
		// identifiers genuinely do not resolve, so a guard flag there is not
		// provably wrong. Skipping keeps the claim airtight rather than convenient.
		if ok, _ := buildPackage(t, root, p.ImportPath); !ok {
			pkgsSkipped++
			continue
		}
		for _, name := range p.GoFiles {
			abs := filepath.Join(p.Dir, name)
			data, err := os.ReadFile(abs)
			if err != nil {
				continue
			}
			rel, err := filepath.Rel(root, abs)
			if err != nil {
				rel = name
			}
			filesChecked++
			linesChecked += bytes.Count(data, []byte("\n"))
			for _, a := range runGuardOnFile(known, rel, string(data), modulePath, depIdx) {
				fps = append(fps, fp{file: rel, symbol: a.v.Symbol, line: a.v.Line, check: a.check, posture: a.posture})
			}
		}
	}

	t.Logf("corpus=%s packages=%d (skipped %d that do not build) files=%d lines=%d",
		root, len(pkgs), pkgsSkipped, filesChecked, linesChecked)
	t.Logf("#314 dependency resolution: module=%q same-repo-imports=%d dep-resolved=%d dep-abstained=%d (reasons=%v)",
		modulePath, sameRepo, depResolved, depAbstained, abstainReasons)
	if filesChecked == 0 {
		t.Skip("no cleanly-building Go files to adjudicate")
	}

	if len(fps) == 0 {
		// Honest framing: zero here is necessary but not sufficient. A guard that
		// flagged nothing at all would also score zero, which is why the false
		// negative phase exists.
		t.Logf("PROVEN FALSE POSITIVES: 0 over %d files / %d lines", filesChecked, linesChecked)
		return
	}

	byCheck := map[string]int{}
	byName := map[string]int{}
	for _, f := range fps {
		byName[f.symbol]++
		byCheck[f.check+" / "+f.posture]++
	}
	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		if byName[names[i]] != byName[names[j]] {
			return byName[names[i]] > byName[names[j]]
		}
		return names[i] < names[j]
	})
	var b strings.Builder
	fmt.Fprintf(&b, "PROVEN FALSE POSITIVES: %d over %d files / %d lines (%.2f per KLOC)\n",
		len(fps), filesChecked, linesChecked, float64(len(fps))*1000/float64(max(linesChecked, 1)))
	fmt.Fprintf(&b, "every one of these sits in a package that `go build` compiles, so the identifier provably resolves\n")
	checks := make([]string, 0, len(byCheck))
	for c := range byCheck {
		checks = append(checks, c)
	}
	sort.Strings(checks)
	for _, c := range checks {
		fmt.Fprintf(&b, "  by check/posture: %-32s %d\n", c, byCheck[c])
	}
	for _, n := range names {
		fmt.Fprintf(&b, "  %-40s x%d\n", n, byName[n])
	}
	shown := len(fps)
	if shown > 25 {
		shown = 25
	}
	for _, f := range fps[:shown] {
		fmt.Fprintf(&b, "  [%s/%s] %s:%d  %s\n", f.check, f.posture, f.file, f.line, f.symbol)
	}
	if len(fps) > shown {
		fmt.Fprintf(&b, "  ... %d more sites not shown\n", len(fps)-shown)
	}
	t.Error(b.String())
}

// ---------------------------------------------------------------------------
// Phase 2 — false negatives, proven by an oracle-adjudicated mutation
// ---------------------------------------------------------------------------

// A Go source file can reference a name the compiler must resolve in three
// shapes, and guard.Run only looks at one of them. Measuring them separately is
// the whole point: a single blended "false negative rate" would hide which shape
// the guard cannot see, and the shape is the fix.
type refShape string

const (
	shapeBareExported   refShape = "bare-exported"   // Foo(...)     — guard.Run's Go population
	shapeBareUnexported refShape = "bare-unexported" // foo(...)     — Run skips by documented policy
	shapeSelectorPkg    refShape = "selector-pkg"    // pkg.Foo(...) — the qualified checks' population
	shapeSelectorValue  refShape = "selector-value"  // v.Foo(...)   — a method on a value
)

// reBareCall matches a bare call site: an identifier followed by `(` that is not
// preceded by `.` or another identifier byte. This mirrors the shape guard.Run's
// own Go extractor looks for, so the mutation population and the check's
// population are the same one.
var reBareCall = regexp.MustCompile(`(^|[^\w.])([A-Za-z_][A-Za-z0-9_]*)\s*\(`)

// reSelectorCall matches `base.Member(`. The member is the mutation target: an
// invented method or package function on a real receiver is what a hallucinating
// model actually writes, and it is the shape guard.Run cannot see at all.
var reSelectorCall = regexp.MustCompile(`(^|[^\w.])([A-Za-z_][A-Za-z0-9_]*)\.([A-Za-z_][A-Za-z0-9_]*)\s*\(`)

// mutationSite is one candidate rewrite, before the compiler has ruled on it.
type mutationSite struct {
	relPath string
	absPath string
	line    int // 1-based
	col     int // byte offset of the identifier within the line
	name    string
	fresh   string
	shape   refShape
}

// copyModule duplicates the corpus into dir so mutations never touch the real
// tree. .git is skipped (large and irrelevant to a build); everything else is
// copied verbatim, including vendor/ and go.sum, so the copy builds exactly as
// the original does.
func copyModule(t *testing.T, src, dst string, maxBytes int64) bool {
	t.Helper()
	var total int64
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable subtree: skip rather than abort the copy
		}
		rel, rerr := filepath.Rel(src, p)
		if rerr != nil {
			return nil
		}
		if d.IsDir() {
			// None of these contribute to a Go build, and each can be large enough
			// to blow the copy budget on its own — a .codegraph index in particular
			// has been observed at 880 MB.
			switch d.Name() {
			case ".git", ".codegraph", "node_modules", ".venv":
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		if !d.Type().IsRegular() {
			return nil // symlinks and devices are not needed to build
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		total += info.Size()
		if total > maxBytes {
			return fmt.Errorf("corpus exceeds %d bytes", maxBytes)
		}
		data, rerr2 := os.ReadFile(p)
		if rerr2 != nil {
			return nil
		}
		return os.WriteFile(filepath.Join(dst, rel), data, 0o644)
	})
	if err != nil {
		t.Logf("corpus copy aborted: %v", err)
		return false
	}
	return true
}

// collectSites finds every mutable call site in the package whose fresh name is
// absent from the entire package. Absence is checked across all the package's
// files, not just the mutated one, because a same-package sibling file could
// otherwise declare the "fresh" name and quietly make the mutation a no-op.
func collectSites(t *testing.T, root string, p goPkg) []mutationSite {
	t.Helper()
	contents := make(map[string]string, len(p.GoFiles))
	var all strings.Builder
	for _, name := range p.GoFiles {
		data, err := os.ReadFile(filepath.Join(p.Dir, name))
		if err != nil {
			continue
		}
		contents[name] = string(data)
		all.WriteString(contents[name])
	}
	pkgText := all.String()

	var sites []mutationSite
	for name, text := range contents {
		// A selector's base is either an imported package or a value. The two are
		// different resolution problems handled by different code (the qualified
		// checks resolve package members against an export index; a method on a
		// value needs the value's type, which no check has), so scoring them as one
		// shape would average a covered case together with an uncovered one and
		// hide which is which.
		imports := goImportNames(text)
		abs := filepath.Join(p.Dir, name)
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			rel = name
		}
		for i, line := range strings.Split(text, "\n") {
			// Comment lines are not code; a rewrite there compiles fine and would be
			// discarded by the oracle anyway, so skipping is a cost saving rather
			// than a correctness claim.
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			add := func(col int, ident string, shape refShape) {
				if isGoKeywordOrBuiltin(ident) {
					return
				}
				fresh := ident + mutationSuffix
				if strings.Contains(pkgText, fresh) {
					return
				}
				sites = append(sites, mutationSite{
					relPath: rel, absPath: abs, line: i + 1, col: col,
					name: ident, fresh: fresh, shape: shape,
				})
			}
			for _, m := range reBareCall.FindAllStringSubmatchIndex(line, -1) {
				ident := line[m[4]:m[5]]
				shape := shapeBareUnexported
				if c := ident[0]; c >= 'A' && c <= 'Z' {
					shape = shapeBareExported
				}
				add(m[4], ident, shape)
			}
			for _, m := range reSelectorCall.FindAllStringSubmatchIndex(line, -1) {
				// m[6]:m[7] is the member, which is what gets renamed. The base is
				// left alone: breaking it would test a different question (does the
				// guard see an undefined *receiver*) and muddy the shape's number.
				shape := shapeSelectorValue
				if _, ok := imports[line[m[4]:m[5]]]; ok {
					shape = shapeSelectorPkg
				}
				add(m[6], line[m[6]:m[7]], shape)
			}
		}
	}
	return sites
}

// goImportNames returns the identifiers a Go file's import block binds: the
// alias when one is given, otherwise the last path segment.
//
// guard.ExtractImports is not usable here — it returns nothing for Go (verified:
// it covers the Python and JS import forms only), so classifying selector bases
// with it silently put every package-qualified call in the value bucket and
// reported selector-pkg=0 on a corpus full of `os.Getenv`.
//
// The last-segment fallback is wrong for the minority of packages whose name
// differs from their final path element (`gopkg.in/yaml.v2`). That misclassifies
// a shape label, never a verdict: the compiler still adjudicates the mutation and
// the catching check is still recorded, so the error is confined to which row a
// site is counted in.
func goImportNames(text string) map[string]struct{} {
	out := map[string]struct{}{}
	f, err := goparser.ParseFile(token.NewFileSet(), "", text, goparser.ImportsOnly)
	if err != nil {
		return out
	}
	for _, spec := range f.Imports {
		if spec.Name != nil {
			if n := spec.Name.Name; n != "_" && n != "." {
				out[n] = struct{}{}
			}
			continue
		}
		path := strings.Trim(spec.Path.Value, `"`)
		if i := strings.LastIndex(path, "/"); i >= 0 {
			path = path[i+1:]
		}
		if path != "" {
			out[path] = struct{}{}
		}
	}
	return out
}

// isGoKeywordOrBuiltin filters the identifiers that are followed by `(` but are
// not calls to a resolvable object — control-flow keywords, conversions to
// builtin types, and builtin functions. Renaming `len(` would produce an
// undefined name the guard is right to ignore, so these would only add noise.
func isGoKeywordOrBuiltin(s string) bool {
	switch s {
	case "if", "for", "switch", "return", "go", "defer", "func", "select", "case", "chan", "range":
		return true
	case "append", "cap", "clear", "close", "complex", "copy", "delete", "imag", "len",
		"make", "max", "min", "new", "panic", "print", "println", "real", "recover":
		return true
	case "bool", "byte", "complex64", "complex128", "error", "float32", "float64",
		"int", "int8", "int16", "int32", "int64", "rune", "string",
		"uint", "uint8", "uint16", "uint32", "uint64", "uintptr", "any":
		return true
	}
	return false
}

// applyMutation rewrites the one identifier occurrence at s and returns the new
// file text alongside the original, so the caller can restore it.
func applyMutation(s mutationSite) (mutated, original string, ok bool) {
	data, err := os.ReadFile(s.absPath)
	if err != nil {
		return "", "", false
	}
	original = string(data)
	lines := strings.Split(original, "\n")
	if s.line-1 >= len(lines) {
		return "", "", false
	}
	line := lines[s.line-1]
	if s.col+len(s.name) > len(line) || line[s.col:s.col+len(s.name)] != s.name {
		return "", "", false
	}
	lines[s.line-1] = line[:s.col] + s.fresh + line[s.col+len(s.name):]
	return strings.Join(lines, "\n"), original, true
}

// whichCheckCatches runs the guard's Go resolution checks over the mutated file
// and reports which one, if any, flagged the fresh name.
//
// Both checks are run regardless of their shipped default, because the question
// here is what the code CAN see. Where that differs from what a user gets, the
// report says so — a check that catches a defect only when an env var is set is
// not catching it for anybody by default.
func whichCheckCatches(known map[string]struct{}, modulePath, relPath, text, fresh string, depIdx depindex.Index) string {
	lines := guard.TextToAddedLines(text)
	symbols := make(map[string]struct{}, len(known)+16)
	for s := range known {
		symbols[s] = struct{}{}
	}
	guard.FoldInFileDefs(symbols, lines, guard.LangFor(relPath))
	hit := func(v guard.Violation) bool {
		return v.Symbol == fresh || strings.HasSuffix(v.Symbol, "."+fresh)
	}

	for _, v := range guard.Run(symbols, "", []guard.FileDiff{{Path: relPath, AddedLines: lines}}) {
		if hit(v) {
			return "Run"
		}
	}
	if modulePath != "" {
		// Same-module qualified calls (`internal/foo.Bar`), validated against the
		// repo IR.
		for _, v := range guard.GoQualifiedViolations(lines, lines, symbols, modulePath) {
			if hit(v) {
				return "GoQualified"
			}
		}
	}
	if len(guardReceiverHits(lines, symbols, fresh)) > 0 {
		return "GoReceiverMethod"
	}
	if depIdx != nil {
		// External and stdlib qualified calls (`os.Getenv`), validated against the
		// dependency export index. Without this the selector shape would be scored
		// against two checks that both exclude stdlib by design, and the guard
		// would be blamed for a gap it has a third check for.
		for _, v := range guard.GoDepQualifiedViolations(lines, lines, modulePath, depIdx) {
			if hit(v) {
				return "GoDepQualified"
			}
		}
	}
	return ""
}

// guardReceiverHits returns the receiver-method violations matching fresh.
func guardReceiverHits(lines []guard.AddedLine, symbols map[string]struct{}, fresh string) []guard.Violation {
	var out []guard.Violation
	for _, v := range guard.GoReceiverMethodViolations(lines, lines, symbols) {
		if v.Symbol == fresh || strings.HasSuffix(v.Symbol, "."+fresh) {
			out = append(out, v)
		}
	}
	return out
}

// shapeResult accumulates one shape's outcome across the run.
type shapeResult struct {
	proven   int            // mutations the compiler confirmed are unresolved
	caught   map[string]int // check name -> catches
	missed   int
	examples []string
}

func TestGoResolveFalseNegativesAgainstCompiler(t *testing.T) {
	if testing.Short() {
		t.Skip("phase 2 pays one package build per mutation; skipped in -short")
	}
	root := corpusRoot(t)
	if len(listPackages(t, root)) == 0 {
		t.Skipf("no buildable Go packages under %s", root)
	}

	work := t.TempDir()
	const maxCorpusBytes = 512 << 20
	if !copyModule(t, root, work, maxCorpusBytes) {
		t.Skipf("could not stage a writable copy of %s", root)
	}
	// Everything below runs against the copy, so the real tree is never mutated
	// even if the test panics mid-run.
	workPkgs := listPackages(t, work)
	if len(workPkgs) == 0 {
		t.Skip("staged copy does not list any buildable packages")
	}
	known := knownSet(t, work)
	modulePath := guard.GoModulePath(work)
	// The dependency export index is built once for the whole corpus. It is what
	// cmd/runecho-guard's newGoDepIndex constructs, minus the env gate — see
	// whichCheckCatches for why the gate is deliberately bypassed here.
	depIdx := depindex.NewGoIndex(work)

	budget := defaultMutations
	if v := os.Getenv("RUNECHO_ORACLE_MUTATIONS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			t.Fatalf("RUNECHO_ORACLE_MUTATIONS must be a positive integer, got %q", v)
		}
		budget = n
	}

	// Gather every eligible site first so the cap is reported against the real
	// population rather than against however far the run happened to get.
	pools := map[refShape][]mutationSite{}
	pkgOf := map[string]string{} // dir -> import path, for building just the mutated package
	for _, p := range workPkgs {
		pkgOf[p.Dir] = p.ImportPath
		for _, s := range collectSites(t, work, p) {
			pools[s.shape] = append(pools[s.shape], s)
		}
	}
	shapes := []refShape{shapeBareExported, shapeBareUnexported, shapeSelectorPkg, shapeSelectorValue}

	// Stratified, evenly-spaced sampling. Taking a prefix of one pooled list would
	// have concentrated the sample in whichever files sort first AND let the
	// commonest shape crowd the others out — an earlier run of this harness spent
	// 23 of 24 adjudicated mutations on one shape and produced a number for the
	// other that rested on a single site.
	perShape := budget / len(shapes)
	if perShape < 1 {
		perShape = 1
	}
	var chosen []mutationSite
	for _, sh := range shapes {
		pool := pools[sh]
		sort.Slice(pool, func(i, j int) bool {
			if pool[i].relPath != pool[j].relPath {
				return pool[i].relPath < pool[j].relPath
			}
			if pool[i].line != pool[j].line {
				return pool[i].line < pool[j].line
			}
			return pool[i].col < pool[j].col
		})
		stride := 1
		if len(pool) > perShape {
			stride = len(pool) / perShape
		}
		for i, n := 0, 0; i < len(pool) && n < perShape; i, n = i+stride, n+1 {
			chosen = append(chosen, pool[i])
		}
	}
	if len(chosen) == 0 {
		t.Skip("no eligible call sites in the corpus")
	}

	results := map[refShape]*shapeResult{}
	for _, sh := range shapes {
		results[sh] = &shapeResult{caught: map[string]int{}}
	}
	discarded := map[refShape]int{}

	for _, s := range chosen {
		importPath, ok := pkgOf[filepath.Dir(s.absPath)]
		if !ok {
			discarded[s.shape]++
			continue
		}
		mutated, original, ok := applyMutation(s)
		if !ok {
			discarded[s.shape]++
			continue
		}
		if err := os.WriteFile(s.absPath, []byte(mutated), 0o644); err != nil {
			t.Fatalf("write mutation: %v", err)
		}
		built, refs := buildPackage(t, work, importPath)
		// ADJUDICATION. The mutation counts only if the compiler says this exact
		// name is unresolved at this exact line. Anything else — the package still
		// builds, or it broke for an unrelated reason — means the site selection
		// was wrong, and the case is dropped rather than scored. The compiler, not
		// the site-selection regex, decides what is a real defect.
		proven := false
		if !built {
			for _, r := range refs {
				if r.Line != s.line {
					continue
				}
				if r.Name == s.fresh || strings.HasSuffix(r.Name, "."+s.fresh) {
					proven = true
					break
				}
			}
		}
		if !proven {
			discarded[s.shape]++
			if err := os.WriteFile(s.absPath, []byte(original), 0o644); err != nil {
				t.Fatalf("restore file: %v", err)
			}
			continue
		}

		by := whichCheckCatches(known, modulePath, s.relPath, mutated, s.fresh, depIdx)
		if err := os.WriteFile(s.absPath, []byte(original), 0o644); err != nil {
			t.Fatalf("restore file: %v", err)
		}

		res := results[s.shape]
		res.proven++
		if by != "" {
			res.caught[by]++
			continue
		}
		res.missed++
		if len(res.examples) < 8 {
			res.examples = append(res.examples,
				fmt.Sprintf("%s:%d  %s -> %s", s.relPath, s.line, s.name, s.fresh))
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "corpus=%s module=%s\n", root, modulePath)
	fmt.Fprintf(&b, "eligible sites by shape:")
	for _, sh := range shapes {
		fmt.Fprintf(&b, " %s=%d", sh, len(pools[sh]))
	}
	fmt.Fprintf(&b, "\nmutations attempted=%d (budget %d, %d per shape)\n", len(chosen), budget, perShape)
	fmt.Fprintf(&b, "\n%-18s %8s %9s %s\n", "SHAPE", "PROVEN", "MISSED", "CAUGHT BY / DISCARDED")
	totalProven, totalMissed := 0, 0
	for _, sh := range shapes {
		r := results[sh]
		totalProven += r.proven
		totalMissed += r.missed
		var by []string
		for k, v := range r.caught {
			by = append(by, fmt.Sprintf("%s=%d", k, v))
		}
		sort.Strings(by)
		if len(by) == 0 {
			by = append(by, "-")
		}
		fmt.Fprintf(&b, "%-18s %8d %9d %s (discarded %d)\n",
			sh, r.proven, r.missed, strings.Join(by, " "), discarded[sh])
	}
	fmt.Fprintf(&b, "\nTOTAL proven-unresolved=%d, unseen by every check=%d (%.0f%%)\n",
		totalProven, totalMissed, pct(totalMissed, totalProven))
	fmt.Fprintf(&b, "discarded = the compiler did not call the rewrite undefined (the site was inside a "+
		"string literal, a build-excluded file, or otherwise not a real reference), so it was dropped rather than scored\n")
	for _, sh := range shapes {
		for _, e := range results[sh].examples {
			fmt.Fprintf(&b, "  [%s] %s\n", sh, e)
		}
	}
	t.Log("\n" + b.String())

	// Only the bare-exported shape is a defect when missed: it is the population
	// guard.Run declares it checks. The other two shapes are reported as reach,
	// not scored as failures, because silence there is documented policy (Go
	// unexported names are not indexed) or belongs to a check that is off by
	// default. Failing on those would be scoring the guard against a contract it
	// never made.
	if r := results[shapeBareExported]; r.proven == 0 {
		t.Log("NOTE: no bare-exported mutation survived adjudication — the in-population " +
			"claim rests on nothing in this run; raise RUNECHO_ORACLE_MUTATIONS")
	} else if r.missed > 0 {
		t.Errorf("PROVEN FALSE NEGATIVES: %d/%d bare exported calls (%.0f%%) are reported "+
			"`undefined:` by the compiler and flagged by nothing in the guard:\n  %s",
			r.missed, r.proven, pct(r.missed, r.proven), strings.Join(r.examples, "\n  "))
	}
}

func pct(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) * 100 / float64(d)
}
