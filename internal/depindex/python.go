package depindex

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Python dependency resolution.
//
// Locating the RIGHT interpreter is the first false-positive source: validating
// `pl.corr()` against a system-wide polars when the project runs a venv with a
// different version flags valid code. So this resolver ONLY trusts an explicit
// virtualenv (VIRTUAL_ENV, or a pyvenv.cfg-bearing .venv/venv walking up from the
// edited file). No system-interpreter fallback, no PYTHONPATH guessing: no
// confident environment means Unknown for every module, and the check silently
// does nothing. That is the intended posture — a repo without a venv gets no
// external-dependency checking rather than a risky approximation of one.

// maxInitSize caps the __init__.py we will read. Real package inits are a few KB;
// a multi-megabyte generated one is not worth the guard's latency budget, and
// refusing to read it yields Unknown (abstain), never a partial export set
// masquerading as complete.
const maxInitSize = 4 << 20 // 4 MiB

// maxVenvWalk bounds the parent walk looking for a venv, matching the bounded
// walk in guard.GoModulePath.
const maxVenvWalk = 64

// PythonIndex resolves installed distributions out of one virtualenv's
// site-packages. The zero value is unusable; construct with NewPythonIndex.
//
// Results are memoized per module for the process lifetime. A guard invocation is
// short-lived (one commit or one edit), so there is no cache-invalidation problem
// to solve here: the environment cannot change underneath a single run.
type PythonIndex struct {
	sitePackages string // "" => every Lookup returns Unknown
	reason       string // why sitePackages is empty (for the Unknown reason)

	mu       sync.Mutex
	cache    map[string]PackageSymbols
	resolves int // distinct modules actually read from disk this run
}

// maxResolvesPerRun bounds how many distinct modules one index will read from
// disk. A cold resolve measures ~2ms (file read + directory listing + a linear
// scan), so an unbounded file — a module importing thirty third-party packages —
// could spend well past the guard's ~12ms edit-time budget before answering.
//
// The cap is on a COUNT, not a wall clock, so the result stays a pure function of
// the input: the same file yields the same verdicts on a fast and a slow machine.
// That is the same reasoning as the parser's maxParseNestDepth, and the reason a
// timeout was not used. Modules past the cap resolve Unknown, i.e. the check
// quietly narrows rather than degrading into guesses.
const maxResolvesPerRun = 16

// NewPythonIndex builds an index rooted at the virtualenv governing startDir.
// It never returns nil: when no venv is found, the returned index resolves
// everything to Unknown with an explanatory reason, so callers need no nil check
// and the abstain path is uniform.
func NewPythonIndex(startDir string) *PythonIndex {
	sp, reason := FindSitePackages(startDir)
	return &PythonIndex{sitePackages: sp, reason: reason, cache: map[string]PackageSymbols{}}
}

// SitePackages returns the resolved site-packages directory ("" if none). Exposed
// for diagnostics and tests.
func (idx *PythonIndex) SitePackages() string { return idx.sitePackages }

// FindSitePackages locates the site-packages directory of the virtualenv that
// governs startDir, returning ("", reason) when it cannot do so confidently.
//
// Order: an active VIRTUAL_ENV wins (it is an explicit statement of intent),
// then the nearest .venv/venv directory walking up from startDir. A candidate
// must carry pyvenv.cfg to count as a venv — a stray directory named "venv" is
// not an environment. If the venv contains more than one lib/python3.X tree, the
// interpreter is ambiguous and we abstain rather than pick one.
func FindSitePackages(startDir string) (string, string) {
	if ve := strings.TrimSpace(os.Getenv("VIRTUAL_ENV")); ve != "" {
		if sp, ok := sitePackagesIn(ve); ok {
			return sp, ""
		}
		return "", "VIRTUAL_ENV set but no unambiguous site-packages under " + ve
	}
	dir := startDir
	for i := 0; i < maxVenvWalk; i++ {
		for _, name := range []string{".venv", "venv"} {
			cand := filepath.Join(dir, name)
			if !isFile(filepath.Join(cand, "pyvenv.cfg")) {
				continue
			}
			if sp, ok := sitePackagesIn(cand); ok {
				return sp, ""
			}
			return "", "venv at " + cand + " has no unambiguous site-packages"
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", "no virtualenv found (external-dependency checking needs one)"
}

// sitePackagesIn returns the single site-packages dir inside a venv. Windows venvs
// use Lib/site-packages; POSIX uses lib/pythonX.Y/site-packages. More than one
// match means we cannot tell which interpreter runs the code — abstain.
func sitePackagesIn(venv string) (string, bool) {
	if sp := filepath.Join(venv, "Lib", "site-packages"); isDir(sp) {
		return sp, true
	}
	matches, err := filepath.Glob(filepath.Join(venv, "lib", "python*", "site-packages"))
	if err != nil || len(matches) != 1 {
		return "", false
	}
	if !isDir(matches[0]) {
		return "", false
	}
	return matches[0], true
}

// Lookup resolves a dotted module path ("polars", "numpy.linalg") to its export
// surface. See the package doc for what each Resolution permits.
func (idx *PythonIndex) Lookup(module string) PackageSymbols {
	if idx.sitePackages == "" {
		return unknown("%s", idx.reason)
	}
	idx.mu.Lock()
	if ps, ok := idx.cache[module]; ok {
		idx.mu.Unlock()
		return ps
	}
	if idx.resolves >= maxResolvesPerRun {
		idx.mu.Unlock()
		return unknown("resolve budget exhausted (%d modules)", maxResolvesPerRun)
	}
	idx.resolves++
	idx.mu.Unlock()

	ps := idx.resolve(module)

	idx.mu.Lock()
	idx.cache[module] = ps
	idx.mu.Unlock()
	return ps
}

// resolve does the uncached work: locate the module's source, read it, derive its
// export surface, and fold in the package's submodule names.
//
// Submodules matter because `import pkg.sub` anywhere in the program makes `sub`
// an attribute of `pkg` at runtime, even though nothing in pkg/__init__.py binds
// it. Folding every child .py/package name into the export set removes that whole
// class of false positive without needing to know what else the program imports.
func (idx *PythonIndex) resolve(module string) PackageSymbols {
	src, pkgDir, ps, ok := idx.readModuleSource(module)
	if !ok {
		return ps
	}
	syms := exportsFromPythonModule(src)
	if syms.Res == Resolved && pkgDir != "" {
		for _, name := range submoduleNames(pkgDir) {
			syms.Exports[name] = struct{}{}
		}
	}
	return syms
}

// submoduleNames lists the importable children of a package directory: each
// `name.py` and each subdirectory. Compiled extensions (.so/.pyd) count too — a
// package's C submodule is just as attribute-reachable as a Python one. Errors
// yield no names; the caller's set is only ever widened here, so a read failure
// costs recall, never precision.
func submoduleNames(pkgDir string) []string {
	ents, err := os.ReadDir(pkgDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() {
			out = append(out, name)
			continue
		}
		for _, ext := range []string{".py", ".pyi", ".so", ".pyd"} {
			if strings.HasSuffix(name, ext) {
				base := strings.TrimSuffix(name, ext)
				// Compiled extensions carry an ABI tag: `_lib.cpython-312-x86_64.so`
				// is imported as `_lib`, so keep only the part before the first dot.
				out = append(out, strings.Split(base, ".")[0])
				break
			}
		}
	}
	return out
}

// readModuleSource finds and reads the .py source defining module. pkgDir is the
// package's own directory when module is a package (so the caller can enumerate
// submodules), and "" for a single-file module. It returns ok=false with the
// abstain result to propagate when the module is absent, oversized, unreadable,
// or has no Python source at all (a compiled extension or a namespace package —
// both of which we cannot enumerate statically).
func (idx *PythonIndex) readModuleSource(module string) (string, string, PackageSymbols, bool) {
	parts := strings.Split(module, ".")
	for _, p := range parts {
		// Reject anything that could escape site-packages or is not an identifier.
		// A malformed qualifier is a parse artifact, not a real module.
		if p == "" || p == "." || p == ".." || strings.ContainsAny(p, `/\`) {
			return "", "", unknown("malformed module path %q", module), false
		}
	}
	base := filepath.Join(append([]string{idx.sitePackages}, parts...)...)

	pkgInit := filepath.Join(base, "__init__.py")
	modFile := base + ".py"

	var path, pkgDir string
	switch {
	case isFile(pkgInit):
		path, pkgDir = pkgInit, base
	case isFile(modFile):
		path = modFile
	case isDir(base):
		// A directory with no __init__.py is a PEP 420 namespace package, or a
		// compiled-extension-only distribution. Either way its attributes are not
		// statically enumerable from source.
		return "", "", unknown("%s: no __init__.py (namespace or compiled-only package)", module), false
	default:
		// Not installed in this environment. Critically this is Unknown, NOT
		// "package has no such symbol" — a module we cannot see must never be
		// the basis for flagging a call into it.
		return "", "", unknown("%s: not installed in %s", module, idx.sitePackages), false
	}

	fi, err := os.Stat(path)
	if err != nil {
		return "", "", unknown("%s: stat: %v", module, err), false
	}
	if fi.Size() > maxInitSize {
		return "", "", unknown("%s: source exceeds %d bytes", module, maxInitSize), false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", unknown("%s: read: %v", module, err), false
	}
	return string(data), pkgDir, PackageSymbols{}, true
}

// --- export-surface extraction -------------------------------------------------

// reDynamicSurface matches constructs that make a module's attribute set
// non-enumerable from its source. Any hit anywhere in the module downgrades it to
// Partial, because the names such a construct injects are invisible to us and are
// exactly the ones that would false-positive.
//
//   - `def __getattr__` — PEP 562 module-level lazy attributes (how polars,
//     scipy, and friends defer submodule imports)
//   - `import *`        — pulls in an unknowable set of names
//   - globals()/setattr/sys.modules/importlib — explicit dynamic namespace writes
var reDynamicSurface = regexp.MustCompile(
	`(?m)^\s*def\s+__getattr__\s*\(|^\s*from\s+\S+\s+import\s+\*|\bglobals\s*\(\s*\)|\bsetattr\s*\(|\bsys\.modules\b|\bimportlib\b`)

// reAllAssign finds an `__all__` assignment and captures the RHS start, so we can
// verify it is a plain literal sequence rather than something computed.
var reAllAssign = regexp.MustCompile(`(?m)^\s*__all__\s*(?::[^=]*)?(\+?=)\s*(.*)$`)

// reAllMutate matches in-place __all__ mutation, which we cannot evaluate.
var reAllMutate = regexp.MustCompile(`(?m)^\s*__all__\s*\.\s*(extend|append|insert)\s*\(`)

// reStringItem extracts quoted items from a literal sequence.
var reStringItem = regexp.MustCompile(`['"]([A-Za-z_][A-Za-z0-9_]*)['"]`)

// reTopLevelDef captures the name bound by a module-level def/class, including
// the async and decorated forms (a decorator sits on its own line, so the def
// itself still matches).
var reTopLevelDef = regexp.MustCompile(`^(?:async\s+)?(?:def|class)\s+([A-Za-z_]\w*)`)

// reTopLevelAssign captures a module-level binding target: an unindented
// `NAME =` / `NAME: T =` (but not `==`, and not a dunder we handle separately).
var reTopLevelAssign = regexp.MustCompile(`^([A-Za-z_]\w*)\s*(?::[^=]+)?=[^=]`)

// exportsFromPythonModule derives the export surface of one module's source.
//
// The set unions __all__ with every top-level binding — defs, classes,
// assignments, and imported names. That is intentionally wider than the module's
// public API: a name bound anywhere at module level IS reachable as an attribute,
// so including it prevents flagging a real (if private or re-exported) access.
func exportsFromPythonModule(src string) PackageSymbols {
	src = strings.ReplaceAll(src, "\r\n", "\n")

	if m := reDynamicSurface.FindString(src); m != "" {
		return partial("module has a dynamic attribute surface (%s)", strings.TrimSpace(m))
	}
	if reAllMutate.MatchString(src) {
		return partial("__all__ is mutated in place")
	}

	exports := map[string]struct{}{}

	// __all__ must be a plain literal sequence if present at all. A computed
	// __all__ (comprehension, concatenation of imported lists, list(...)) means
	// the declared surface is unknowable, so the whole module is Partial rather
	// than silently under-enumerated.
	for _, loc := range reAllAssign.FindAllStringSubmatchIndex(src, -1) {
		items, ok := literalSequenceItems(src, loc[4])
		if !ok {
			return partial("__all__ is not a static literal sequence")
		}
		for _, it := range items {
			exports[it] = struct{}{}
		}
	}

	// Module-level bindings, over a paren/bracket-joined view so a multi-line
	// `from x import (\n a,\n b,\n)` is seen as one logical line. Without the
	// join its continuation lines read as indented and the names would be
	// dropped — an under-count, which is the false-positive direction.
	//
	// "Module level" is NOT the same as "column zero". A great deal of real
	// module state is bound inside an indented try/except or `if` at module
	// scope — `try: import ssl except ImportError: ssl = None` in requests and
	// urllib3, the `_pandas_parser_CAPI` guards in pandas. Those names ARE module
	// attributes, so skipping indented lines wholesale flags valid code. What
	// must be excluded is only what is inside a def/class BODY, whose names are
	// local. So: skip lines nested under a def/class, keep everything else at any
	// indentation.
	skipIndent := -1
	for _, line := range logicalLines(src) {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if skipIndent >= 0 {
			if indent > skipIndent {
				continue // inside the def/class body opened at skipIndent
			}
			skipIndent = -1
		}
		trimmed := strings.TrimSpace(line)
		// A def/class at module level binds its own name and opens a body whose
		// names are local. Take the name here and skip the body: this is the same
		// information the AST parser would give, for none of its cost. The AST
		// path was measured at ~3.6ms of FIXED grammar-init per Parse call — on a
		// 24-byte module — which alone would blow the guard's ~12ms edit-time
		// budget after three lookups. Nested defs never reach this branch, since
		// they sit inside a skipped body.
		if m := reTopLevelDef.FindStringSubmatch(trimmed); m != nil {
			exports[m[1]] = struct{}{}
			skipIndent = indent
			continue
		}
		for _, n := range pyImportBindings(trimmed) {
			exports[n] = struct{}{}
		}
		if m := reTopLevelAssign.FindStringSubmatch(trimmed); m != nil {
			exports[m[1]] = struct{}{}
		}
	}

	return PackageSymbols{Res: Resolved, Exports: exports}
}

// literalSequenceItems parses the RHS of an `__all__` assignment that begins on
// the given line. It returns the quoted identifiers and ok=true only when the
// whole bracketed region contains nothing but string literals, commas, and
// whitespace/comments — anything else (a name, a call, a star-unpack) means the
// sequence is computed and the caller must degrade to Partial.
//
// rhsStart is the BYTE OFFSET in src where the right-hand side begins. It must be
// an offset, not the RHS text: an `__all__ = (` assignment has an RHS of just "(",
// and locating that by content would find the first parenthesis anywhere in the
// module and parse an unrelated region.
func literalSequenceItems(src string, rhsStart int) ([]string, bool) {
	if rhsStart < 0 || rhsStart > len(src) {
		return nil, false
	}
	// Skip whitespace between `=` and the opening bracket.
	start := rhsStart
	for start < len(src) && (src[start] == ' ' || src[start] == '\t') {
		start++
	}
	if start >= len(src) {
		return nil, false
	}
	var closer byte
	switch src[start] {
	case '[':
		closer = ']'
	case '(':
		closer = ')'
	default:
		return nil, false // not a literal sequence (a name, a call, a concat)
	}

	region, ok := bracketRegion(src[start:], src[start], closer)
	if !ok {
		return nil, false
	}

	// The literal must be the WHOLE right-hand side. `__all__ = ["a"] + other`
	// closes its bracket cleanly, so checking only the bracketed region would
	// accept it and silently drop everything `other` contributes — a truncated
	// export set presented as complete, which is the exact failure this package
	// exists to prevent. Require the rest of the statement to be empty.
	tail := src[start+len(region):]
	if nl := strings.IndexByte(tail, '\n'); nl >= 0 {
		tail = tail[:nl]
	}
	if i := strings.IndexByte(tail, '#'); i >= 0 {
		tail = tail[:i]
	}
	if strings.TrimSpace(tail) != "" {
		return nil, false
	}

	// Strip the quoted items, then require that nothing meaningful remains.
	items := []string{}
	for _, m := range reStringItem.FindAllStringSubmatch(region, -1) {
		items = append(items, m[1])
	}
	residue := reStringItem.ReplaceAllString(region, "")
	for _, line := range strings.Split(residue, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		if strings.Trim(line, " \t\r\n,[]()") != "" {
			return nil, false // a name, call, or unpack — not a static literal
		}
	}
	sort.Strings(items)
	return items, true
}

// bracketRegion returns the text from s[0] (which must be open) through its
// matching close, quote-aware so a bracket inside a string does not unbalance the
// count. ok=false if unterminated.
func bracketRegion(s string, open, close byte) (string, bool) {
	depth := 0
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if c == '\\' {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case '#':
			// Skip to end of line: a bracket in a comment is not structural.
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return s[:i+1], true
			}
		}
	}
	return "", false
}

// logicalLines joins physical lines into logical ones across open brackets and
// backslash continuations, so a multi-line import or assignment is analyzed as
// the single statement it is. Leading whitespace of the FIRST physical line is
// preserved (callers use it to test top-level-ness).
func logicalLines(src string) []string {
	var out []string
	var cur strings.Builder
	depth := 0
	cont := false
	for _, line := range strings.Split(src, "\n") {
		if depth == 0 && !cont {
			cur.Reset()
			cur.WriteString(line)
		} else {
			cur.WriteByte(' ')
			cur.WriteString(strings.TrimSpace(line))
		}
		depth += bracketDelta(line)
		if depth < 0 {
			depth = 0
		}
		cont = strings.HasSuffix(strings.TrimRight(line, " \t"), "\\")
		if depth == 0 && !cont {
			out = append(out, cur.String())
		}
	}
	if cur.Len() > 0 && (depth != 0 || cont) {
		out = append(out, cur.String())
	}
	return out
}

// bracketDelta is the net open-bracket count of a line, ignoring brackets inside
// string literals and comments.
func bracketDelta(line string) int {
	delta := 0
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		if quote != 0 {
			if c == '\\' {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case '#':
			return delta
		case '(', '[', '{':
			delta++
		case ')', ']', '}':
			delta--
		}
	}
	return delta
}

// pyImportBindings returns the module-level names a single logical import
// statement binds:
//
//	import a.b            → "a"      (only the root name is bound)
//	import a.b as c       → "c"
//	from m import x, y as z → "x", "z"
//
// A star-import binds an unknowable set, but callers never reach here with one:
// reDynamicSurface has already downgraded such a module to Partial.
func pyImportBindings(line string) []string {
	line = strings.TrimSpace(line)
	if i := strings.IndexByte(line, '#'); i >= 0 {
		line = line[:i]
	}
	var clause string
	if rest, ok := strings.CutPrefix(line, "from "); ok {
		i := strings.Index(rest, " import ")
		if i < 0 {
			return nil
		}
		clause = rest[i+len(" import "):]
	} else if rest, ok := strings.CutPrefix(line, "import "); ok {
		clause = rest
		var names []string
		for _, part := range strings.Split(clause, ",") {
			part = strings.TrimSpace(strings.Trim(part, "()"))
			if part == "" {
				continue
			}
			if fields := strings.Fields(part); len(fields) == 3 && fields[1] == "as" {
				names = append(names, fields[2])
				continue
			}
			// `import a.b` binds the ROOT package name, not the dotted path.
			names = append(names, strings.Split(part, ".")[0])
		}
		return names
	} else {
		return nil
	}

	var names []string
	for _, part := range strings.Split(clause, ",") {
		part = strings.TrimSpace(strings.Trim(part, "()"))
		if part == "" || part == "*" {
			continue
		}
		fields := strings.Fields(part)
		switch {
		case len(fields) == 3 && fields[1] == "as":
			names = append(names, fields[2])
		case len(fields) == 1:
			names = append(names, fields[0])
		}
	}
	return names
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
