package guard_test

// Block splitting and Python site enumeration for the #313 ruff-adjudicated
// Python compiler-oracle differential (see
// ~/.claude/plans/runecho-313-python-ruff-oracle-differential.md sections 7-8).
// This file owns exactly two pieces of the harness, both consumed by
// pyoracle_test.go in this same package:
//
//   - pyTopLevelBlocks: splits a Python source file into top-level def/class
//     blocks, the same job goTopLevelFuncs (resolve_differential_test.go) does
//     for Go — used to build the edit-hunk posture (see section 7's posture
//     table). A block whose start line sits inside an open triple-quoted
//     string (or a backslash-continued single/double-quoted string — #313
//     review F3) is never emitted, which is what lets the consumer pass nil
//     seeds for a hunk: a block always starts at column 0 with open-string
//     state "". (Bracket depth is NOT tracked or claimed here — an earlier
//     version of this comment claimed "and bracket depth 0" with nothing
//     computing it; deleted rather than implemented, since an unchecked
//     claimed invariant is worse than an absent one.)
//
//   - pySites: enumerates every Python reference site across a staged corpus
//     via an embedded CPython ast script, classified into the nine shapes in
//     section 8's table. It returns everything it finds — sampling and budget
//     are the consumer's job, not this file's.
//
// Neither function filters, samples, or fails on a single bad input file: a
// file that does not parse is skipped and counted (never fatal), and an
// unrecognized top-level structure is simply not treated as a block.

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inth3shadows/runecho/internal/guard"
)

// pyBlock is one top-level def/class block located by pyTopLevelBlocks.
type pyBlock struct {
	Body      string // the block's text
	Rest      string // the file with the block removed
	StartLine int    // 1-based line of the block's first line (incl. leading decorators)
	EndLine   int    // 1-based, inclusive
}

// pyTopLevelBlocks splits a Python source file into top-level def/class
// blocks.
//
// A block is a column-0 `def `/`async def `/`class ` line, plus any
// contiguous column-0 `@decorator` lines immediately above it, extending to
// the line before the next column-0 line that is non-blank, non-comment, and
// outside an open triple-quoted string. Literal/comment state is tracked
// line-by-line via guard.StripLiteralsForTest(guard.LangPython, ...) — the
// same helper and the same "state at the START of each line" discipline
// goTopLevelFuncs (resolve_differential_test.go) uses for Go brace-balance —
// so a `def`/`class` keyword that merely appears as TEXT inside a docstring
// or comment is never read as a header: a block whose start line is inside an
// open triple-quoted string is never emitted.
//
// guard.StripLiteralsForTest (backed by extract.go's stripLiteralsBraces)
// tracks triple-quoted string continuations across lines correctly, but NOT
// a backslash-continued single/double-quoted string (`s = "abc \` at EOL,
// continuing on the next physical line) — it falls through to returning the
// incoming `open` unchanged, so a header-looking line hiding inside such a
// continuation would otherwise be misread as a real block start (#313
// review F3). pyTopLevelBlocks tracks that case itself, independently of
// guard.StripLiteralsForTest, via contQuote/pyDetectQuoteContinuation/
// pyScanQuoteClose below; deliberately NOT fixed in extract.go, since that
// is product code and this is a splitter local to the test harness.
// pyLineStates computes, for every line in text, the multi-line-string
// delimiter open at the START of that line (startsOpen[i] — "" if none, or
// the sentinel "\\" for a raw backslash-string continuation line, see
// contQuote below) and that line with string/comment content blanked to
// spaces (scans[i], so a keyword found there is real code, not text inside a
// string or comment). This is the exact per-line masking pyTopLevelBlocks
// uses to find block boundaries, factored out (#313's fifth posture) so a
// caller that needs to ask "is line N (1-based, i.e. startsOpen[N-1]) inside
// an open string" for a position OTHER than a block boundary — the
// inner-hunk posture's slice-start eligibility check in
// pyresolve_differential_test.go — reuses the identical tracking, including
// the backslash-continued single/double-quoted string special case
// (pyDetectQuoteContinuation/pyScanQuoteClose) that guard.StripLiteralsForTest
// alone does not handle, instead of re-deriving it and silently drifting
// from what pyTopLevelBlocks itself considers "open".
func pyLineStates(text string) (startsOpen, scans []string) {
	lines := strings.Split(text, "\n")
	startsOpen = make([]string, len(lines))
	scans = make([]string, len(lines))
	open := ""
	// contQuote is the quote byte ('\'' or '"') of a single/double-quoted
	// string that is still open because the line that opened it ended with
	// an unescaped backslash (line continuation) — see the doc comment
	// above. 0 means "not currently inside such a continuation".
	contQuote := byte(0)
	for i, l := range lines {
		if contQuote != 0 {
			// This line is a raw continuation of an unterminated
			// single/double-quoted string. Reuse the startsOpen[]!="" sentinel
			// so both loops below treat it exactly like a genuine open-triple-
			// string continuation line: never a header, never a block
			// terminator.
			startsOpen[i] = "\\"
			scans[i] = strings.Repeat(" ", len(l))
			contQuote = pyScanQuoteClose(l, contQuote)
			continue
		}
		startsOpen[i] = open
		scans[i], open = guard.StripLiteralsForTest(guard.LangPython, l, open)
		if open == "" {
			contQuote = pyDetectQuoteContinuation(l)
		}
	}
	return startsOpen, scans
}

func pyTopLevelBlocks(text string) []pyBlock {
	lines := strings.Split(text, "\n")
	startsOpen, scans := pyLineStates(text)

	isHeader := func(scan string) bool {
		return strings.HasPrefix(scan, "def ") ||
			strings.HasPrefix(scan, "async def ") ||
			strings.HasPrefix(scan, "class ")
	}

	var out []pyBlock
	for i := 0; i < len(lines); i++ {
		if startsOpen[i] != "" || !isHeader(scans[i]) {
			continue
		}
		// Walk upward over contiguous column-0 @decorator lines.
		start := i
		for start > 0 && startsOpen[start-1] == "" && strings.HasPrefix(scans[start-1], "@") {
			start--
		}
		// Walk forward to the line before the next column-0, non-blank,
		// non-comment, non-open-string line. Blank lines, comment-only lines,
		// and lines inside an open multi-line string continuation are part of
		// the block, not terminators.
		end := len(lines) - 1
		for j := i + 1; j < len(lines); j++ {
			if startsOpen[j] != "" {
				continue // inside an open triple-quoted string continuation
			}
			if strings.TrimSpace(scans[j]) == "" {
				continue // blank or comment-only
			}
			if scans[j][0] != ' ' && scans[j][0] != '\t' {
				end = j - 1
				break
			}
		}
		out = append(out, pyBlock{
			Body:      strings.Join(lines[start:end+1], "\n"),
			Rest:      strings.Join(append(append([]string{}, lines[:start]...), lines[end+1:]...), "\n"),
			StartLine: start + 1,
			EndLine:   end + 1,
		})
	}
	return out
}

// hasOddTrailingBackslash reports whether line ends with an odd run of
// backslashes — an odd run is an unescaped trailing backslash (Python line
// continuation); an even run is a run of escaped backslashes followed by no
// continuation.
func hasOddTrailingBackslash(line string) bool {
	n := 0
	for n < len(line) && line[len(line)-1-n] == '\\' {
		n++
	}
	return n%2 == 1
}

// pyDetectQuoteContinuation checks whether line ends by opening a
// single/double-quoted string that continues onto the next physical line via
// a trailing backslash, and returns the quote byte if so (0 otherwise). Only
// called when guard.StripLiteralsForTest already reported open=="" for this
// line, i.e. it believes no string is open at line end — which is exactly
// the case it gets wrong for this construct (see the pyTopLevelBlocks doc
// comment, #313 review F3).
func pyDetectQuoteContinuation(line string) byte {
	if !hasOddTrailingBackslash(line) {
		return 0
	}
	var quote byte
	i := 0
	for i < len(line) {
		c := line[i]
		if quote == 0 {
			if c == '\'' || c == '"' {
				if i+2 < len(line) && line[i+1] == c && line[i+2] == c {
					// Triple-quote open: guard.StripLiteralsForTest already
					// tracks this correctly via its own `open` return value
					// (which pyTopLevelBlocks carries through startsOpen), so
					// defer to that rather than double-tracking here.
					return 0
				}
				quote = c
			}
			i++
			continue
		}
		if c == '\\' {
			i += 2 // skip the escaped character (or the line's own trailing
			// continuation backslash, at which point the loop ends anyway)
			continue
		}
		if c == quote {
			quote = 0
		}
		i++
	}
	return quote
}

// pyScanQuoteClose continues scanning a raw backslash-string-continuation
// line (see contQuote in pyTopLevelBlocks) looking for the closing quote
// byte. It returns 0 once the string closes (whether or not the line goes on
// to open something else — out of scope here) and quote again if the string
// keeps going onto yet another line via another trailing backslash. If
// neither happens — the line neither closes the string nor continues it —
// this is a real Python SyntaxError (unterminated string literal); rather
// than loop or guess, tracking is abandoned for this line (returns 0) so a
// pathological input can never hang or crash the splitter.
func pyScanQuoteClose(line string, quote byte) byte {
	i := 0
	for i < len(line) {
		c := line[i]
		if c == '\\' {
			i += 2
			continue
		}
		if c == quote {
			return 0
		}
		i++
	}
	if hasOddTrailingBackslash(line) {
		return quote
	}
	return 0
}

func TestPyTopLevelBlocks(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []pyBlock
	}{
		{
			name: "decorated function — decorators included in the block",
			text: "@decorator\n@another.deco\ndef foo():\n    pass",
			want: []pyBlock{{
				Body:      "@decorator\n@another.deco\ndef foo():\n    pass",
				Rest:      "",
				StartLine: 1,
				EndLine:   4,
			}},
		},
		{
			name: "nested def is not its own block",
			text: "def outer():\n    def inner():\n        pass\n    return inner",
			want: []pyBlock{{
				Body:      "def outer():\n    def inner():\n        pass\n    return inner",
				Rest:      "",
				StartLine: 1,
				EndLine:   4,
			}},
		},
		{
			name: "module docstring containing a column-0 def must not split there",
			text: "\"\"\"\nModule doc.\ndef not_real():\n    pass\n\"\"\"\n\ndef real():\n    return 1",
			want: []pyBlock{{
				Body:      "def real():\n    return 1",
				Rest:      "\"\"\"\nModule doc.\ndef not_real():\n    pass\n\"\"\"\n",
				StartLine: 7,
				EndLine:   8,
			}},
		},
		{
			name: "class followed by module-level code",
			text: "class Foo:\n    x = 1\n\nCONST = 2",
			want: []pyBlock{{
				Body:      "class Foo:\n    x = 1\n",
				Rest:      "CONST = 2",
				StartLine: 1,
				EndLine:   3,
			}},
		},
		{
			name: "unterminated triple-quoted string at EOF does not crash and runs to EOF",
			text: "def foo():\n    x = \"\"\"unterminated\n    still going",
			want: []pyBlock{{
				Body:      "def foo():\n    x = \"\"\"unterminated\n    still going",
				Rest:      "",
				StartLine: 1,
				EndLine:   3,
			}},
		},
		{
			// #313 review F3: a header-looking line hiding inside a
			// backslash-continued single-quoted string must not be read as a
			// real block start. Reproduces the reviewer's exact finding:
			// `s = "abc \` + newline + `class Bar: x"` must not split a
			// `class Bar` block out of the middle of that string.
			name: "async def function is recognized as a block header",
			text: "async def foo():\n    pass\n\ndef after():\n    pass",
			want: []pyBlock{
				{
					Body:      "async def foo():\n    pass\n",
					Rest:      "def after():\n    pass",
					StartLine: 1,
					EndLine:   3,
				},
				{
					Body:      "def after():\n    pass",
					Rest:      "async def foo():\n    pass\n",
					StartLine: 4,
					EndLine:   5,
				},
			},
		},
		{
			name: "tab-indented body lines are part of the block, not terminators",
			text: "def foo():\n\tpass\n\ndef after():\n    pass",
			want: []pyBlock{
				{
					Body:      "def foo():\n\tpass\n",
					Rest:      "def after():\n    pass",
					StartLine: 1,
					EndLine:   3,
				},
				{
					Body:      "def after():\n    pass",
					Rest:      "def foo():\n\tpass\n",
					StartLine: 4,
					EndLine:   5,
				},
			},
		},
		{
			// Documents (does not change) measured behavior: a MULTI-LINE
			// decorator is not absorbed into the block, because the
			// upward-walk only accepts contiguous column-0 "@..." lines
			// immediately above the header, and the decorator's own
			// continuation lines (and closing paren) are not "@"-prefixed.
			// Matches the plan's letter ("contiguous column-0 decorator
			// lines"); occurs 0 times in the stdlib per the #313 review, but
			// pinned here rather than left accidental.
			name: "multi-line decorator is NOT absorbed into the block",
			text: "@decorator(\n    arg1,\n    arg2,\n)\ndef foo():\n    pass",
			want: []pyBlock{{
				Body:      "def foo():\n    pass",
				Rest:      "@decorator(\n    arg1,\n    arg2,\n)",
				StartLine: 5,
				EndLine:   6,
			}},
		},
		{
			// Documents (does not change) measured behavior: a decorator
			// separated from its header by a blank line is NOT absorbed,
			// for the same contiguity reason.
			name: "decorator separated by a blank line is NOT absorbed into the block",
			text: "@decorator\n\ndef foo():\n    pass",
			want: []pyBlock{{
				Body:      "def foo():\n    pass",
				Rest:      "@decorator\n",
				StartLine: 3,
				EndLine:   4,
			}},
		},
		{
			name: "header-looking line inside a backslash-continued single-quoted string is not a block",
			text: "def keep():\n    pass\n\ns = \"abc \\\nclass Bar: x\"\n\ndef after():\n    pass",
			want: []pyBlock{
				{
					Body:      "def keep():\n    pass\n",
					Rest:      "s = \"abc \\\nclass Bar: x\"\n\ndef after():\n    pass",
					StartLine: 1,
					EndLine:   3,
				},
				{
					Body:      "def after():\n    pass",
					Rest:      "def keep():\n    pass\n\ns = \"abc \\\nclass Bar: x\"\n",
					StartLine: 7,
					EndLine:   8,
				},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pyTopLevelBlocks(tc.text)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d block(s), want %d\ngot=%+v", len(got), len(tc.want), got)
			}
			for i, w := range tc.want {
				g := got[i]
				if g.StartLine != w.StartLine || g.EndLine != w.EndLine {
					t.Errorf("block %d: got StartLine=%d EndLine=%d, want StartLine=%d EndLine=%d",
						i, g.StartLine, g.EndLine, w.StartLine, w.EndLine)
				}
				if g.Body != w.Body {
					t.Errorf("block %d Body mismatch:\ngot:  %q\nwant: %q", i, g.Body, w.Body)
				}
				if g.Rest != w.Rest {
					t.Errorf("block %d Rest mismatch:\ngot:  %q\nwant: %q", i, g.Rest, w.Rest)
				}
			}
		})
	}
}

// pyShape classifies one Python reference site by its nearest syntactic role.
// See the shape table in the plan's section 8.
type pyShape string

const (
	pyShapeBareCall    pyShape = "bare-call"
	pyShapeBareConst   pyShape = "bare-const"
	pyShapeBareName    pyShape = "bare-name"
	pyShapeAttrBase    pyShape = "attr-base"
	pyShapeDecorator   pyShape = "decorator"
	pyShapeClassBase   pyShape = "class-base"
	pyShapeAnnotation  pyShape = "annotation"
	pyShapeExceptClass pyShape = "except-class"
	pyShapeAttrMember  pyShape = "attr-member"
)

// pySite is one reference site found by pySites.
type pySite struct {
	RelPath string
	Line    int // 1-based
	Col     int // BYTE offset of the identifier within the line
	Name    string
	Shape   pyShape
}

// pySiteScript is an embedded CPython script that enumerates reference sites
// across a set of corpus-relative .py files, classifying every
// ast.Name(ctx=Load) — plus every ast.Attribute's `attr` field — by its
// nearest parent/field, per the shape table in the plan's section 8.
//
// Column offsets come straight from CPython's own col_offset/end_col_offset,
// which are documented as UTF-8 BYTE offsets — pySites passes them through
// unmodified (for ast.Name sites) so a mutation applied against a Go string
// (also byte-indexed) lands on the right byte even when the line contains
// non-ASCII text before the identifier. attr-member sites are the exception:
// ast.Attribute has no col_offset of its own for the `.attr` token, so its
// column is DERIVED as `end_col_offset - len(attr.encode("utf-8"))` — the
// attribute name's own UTF-8 BYTE length, not len(attr) (a CHARACTER count).
// Using the character count would under-subtract by however many multi-byte
// characters the attribute name contains, landing on the wrong byte for any
// non-ASCII attribute name (measured: `résumé` reported col 11 instead of
// the true byte col 9, silently discarding the mutation site downstream —
// #313 review F1).
//
// hint threads a syntactic role (decorator / annotation / except / class-base)
// down through the recursion for shapes the immediate parent/field alone
// cannot answer — an annotation like `Dict[str, Foo]` nests Foo inside a
// Subscript, and `except (A, B):` nests A/B inside a Tuple. hint is cleared
// when recursing into a Call's own arguments/keywords, so `@build(x)` reports
// `build` as bare-call and `x` as an ordinary bare-name — a call argument is
// never itself "the decorator" (or "the annotation", for the rarer
// `Annotated[int, validate()]` case) just because it sits inside one.
// Whichever of these an ast.Name's IMMEDIATE parent/field already answers —
// Call.func, Attribute.value — always takes precedence over hint, so a
// SCREAMING_SNAKE name that is itself a call classifies as bare-call, never
// bare-const.
//
// A file that fails to read, parse, or walk (including exceeding Python's
// recursion limit on a pathologically nested file) is skipped and counted,
// never fatal — one bad file must not take the whole sweep down.
const pySiteScript = `
import ast, builtins, json, os, re, sys

BUILTINS = set(dir(builtins))
SCREAMING = re.compile(r'^[A-Z_][A-Z0-9_]*$')


def is_screaming(name):
    # A leading-underscore name (_MODE_READ, _KEEP, ...) is excluded here even
    # though it matches SCREAMING, because it is structurally invisible to the
    # only check that owns the bare-const shape: extract.go's reUpperSnakeRef
    # is \b([A-Z][A-Z0-9]*(?:_[A-Z0-9]+)+)\b, and \b never fires between the
    # leading "_" and the letter after it (both are \w) — so the regex can
    # never even START a match on such a name, mutated suffix or not. Census
    # (#313 review F4): 441 of 1879 bare-const sites in the default corpus
    # (23.5%) are leading-underscore, all unreachable for this reason; the
    # fair-sample ceiling is ~76%, not 100%. Enumerating them here would let
    # the harness score a miss the guard never had a chance to catch — this
    # exclusion is what pysites_test.go controls; extract.go is NOT changed.
    #
    # Whether _PRIVATE_CONST references SHOULD be checked is a separate
    # product question. If the answer is yes, the \b gap in reUpperSnakeRef
    # is a product defect to file, and this shape stays report-only (no
    # owning check enforces it) until that is decided.
    if name.startswith("_"):
        return False
    return bool(SCREAMING.match(name)) and any(c.isalpha() for c in name)


def visit(node, parent, field, hint, out):
    if isinstance(node, ast.Attribute):
        # ctx must be Load — a Store/Del target (obj.attr = 1, del obj.gone,
        # for obj.tgt in xs:) is a DEFINITION site, not a reference, and must
        # never join the pool (#313 review F2). BUILTINS is intentionally
        # NOT applied to node.attr (#313 review F4): an attribute name is
        # not resolved against builtins scope, so x.format has nothing to do
        # with the builtin format() — filtering it here silently dropped
        # 1,132/30,040 (3.8%) of measured attr-member sites, non-uniformly.
        if isinstance(node.ctx, ast.Load):
            # end_col_offset is a BYTE offset; len(node.attr) would be a
            # CHARACTER count. Subtract the attr's own UTF-8 byte length
            # instead (#313 review F1).
            out.append((node.end_lineno,
                         node.end_col_offset - len(node.attr.encode("utf-8")),
                         node.attr, "attr-member"))
    # ctx must be Load here too, same reason as the Attribute branch above
    # (#313 review F2) — a Store target like "MAX_SIZE = 10" or "TOTAL = ..."
    # must never join the pool.
    if isinstance(node, ast.Name) and isinstance(node.ctx, ast.Load) and node.id not in BUILTINS:
        if isinstance(parent, ast.Call) and field == "func":
            shape = "bare-call"
        elif isinstance(parent, ast.Attribute) and field == "value":
            shape = "attr-base"
        elif hint == "decorator":
            shape = "decorator"
        elif hint == "annotation":
            shape = "annotation"
        elif hint == "except":
            shape = "except-class"
        elif hint == "class-base":
            shape = "class-base"
        elif is_screaming(node.id):
            shape = "bare-const"
        else:
            shape = "bare-name"
        out.append((node.lineno, node.col_offset, node.id, shape))

    for f, value in ast.iter_fields(node):
        child_hint = hint
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
            if f == "decorator_list":
                child_hint = "decorator"
            elif f == "returns":
                child_hint = "annotation"
        if isinstance(node, ast.ClassDef):
            if f == "decorator_list":
                child_hint = "decorator"
            elif f == "bases":
                child_hint = "class-base"
        if isinstance(node, ast.arg) and f == "annotation":
            child_hint = "annotation"
        if isinstance(node, ast.AnnAssign) and f == "annotation":
            child_hint = "annotation"
        if isinstance(node, ast.ExceptHandler) and f == "type":
            child_hint = "except"
        if isinstance(node, ast.Call) and f in ("args", "keywords"):
            child_hint = None

        if isinstance(value, list):
            for item in value:
                if isinstance(item, ast.AST):
                    visit(item, node, f, child_hint, out)
        elif isinstance(value, ast.AST):
            visit(value, node, f, child_hint, out)


def main():
    root = sys.argv[1]
    with open(sys.argv[2], encoding="utf-8") as fh:
        rels = json.load(fh)
    sites = []
    skipped = 0
    parsed = 0
    for rel in rels:
        p = os.path.join(root, rel)
        try:
            # Read raw BYTES, not text (#313 review F6). open(..., encoding=
            # "utf-8") does not strip a UTF-8 BOM, so a valid BOM-prefixed
            # file used to raise SyntaxError here and get counted as a parse
            # failure — while ruffF821 (the other arm of the oracle
            # differential) classified the same file clean, silently
            # measuring two different populations.
            #
            # raw.decode("utf-8-sig") is a VALIDATION-ONLY decode (its
            # result is discarded): it strips a BOM if present (a no-op
            # otherwise) purely to confirm the file is UTF-8-encoded, then
            # ast.parse() below gets the ORIGINAL raw bytes (BOM included)
            # so its own BOM/PEP-263-aware offsets stay aligned with the
            # raw file bytes everywhere else in this harness (measured:
            # col_offset=4 matches raw byte index 4 for the BOM case).
            #
            # Do NOT simplify this to "just ast.parse(raw)" — a file with a
            # PEP-263 coding cookie declaring a non-UTF-8 encoding (e.g.
            # "# -*- coding: latin-1 -*-") would then be silently decoded
            # and parsed per THAT cookie by ast.parse, but its byte offsets
            # do not align 1:1 with the UTF-8 assumption the rest of this
            # harness (and ruff) makes (measured: reported col 14 vs true
            # raw byte col 13). Validating utf-8-sig first keeps such files
            # skipped, exactly as before this fix — only the BOM case
            # changes.
            with open(p, "rb") as fh:
                raw = fh.read()
            raw.decode("utf-8-sig")
            tree = ast.parse(raw, filename=p)
            found = []
            visit(tree, None, None, None, found)
        except (SyntaxError, UnicodeDecodeError, OSError, ValueError, RecursionError):
            skipped += 1
            continue
        parsed += 1
        for line, col, name, shape in found:
            sites.append({"rel": rel, "line": line, "col": col, "name": name, "shape": shape})
    print(json.dumps({"sites": sites, "skipped": skipped, "parsed": parsed}))


main()
`

// pySitesStats carries the parse-failure count back to the caller (#313
// review F5) alongside how many files parsed successfully. Previously
// payload.Skipped was only t.Logf'd and discarded — unreachable by the
// caller, so a corpus that fails to parse ENTIRELY (e.g. a Python-2 tree
// pointed at by RUNECHO_ORACLE_PY_CORPUS) produced an empty sites slice
// with no signal distinguishing "parsed clean, zero sites" from "nothing
// parsed at all."
type pySitesStats struct {
	Skipped int // files that failed to read/parse/walk
	Parsed  int // files that parsed successfully (may still yield zero sites)
}

// pySites enumerates reference sites across the staged corpus by running an
// embedded CPython script over it. rels are corpus-relative .py paths. It
// returns everything it finds, unfiltered and unsampled — budget and sampling
// belong to the caller — plus a pySitesStats the caller can use to detect a
// wholesale parse failure across the corpus. t.Skip when python3 is
// unavailable or the script cannot run at all (an oracle that cannot run is
// no evidence either way); a single file that fails to parse is skipped and
// counted (both internally and in the returned stats), logged via t.Logf,
// and never fails the run.
func pySites(t *testing.T, staged string, rels []string) ([]pySite, pySitesStats) {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not on PATH — pySites requires CPython's ast module")
		return nil, pySitesStats{}
	}
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "pysites.py")
	if err := os.WriteFile(scriptPath, []byte(pySiteScript), 0o600); err != nil {
		t.Fatalf("write pySites script: %v", err)
	}
	relsJSON, err := json.Marshal(rels)
	if err != nil {
		t.Fatalf("marshal rels: %v", err)
	}
	relsPath := filepath.Join(dir, "rels.json")
	if err := os.WriteFile(relsPath, relsJSON, 0o600); err != nil {
		t.Fatalf("write rels: %v", err)
	}

	cmd := exec.Command(py, scriptPath, staged, relsPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("pySites failed on %s: %v\n%s", staged, err, stderr.String())
		return nil, pySitesStats{}
	}

	var payload struct {
		Sites []struct {
			Rel   string `json:"rel"`
			Line  int    `json:"line"`
			Col   int    `json:"col"`
			Name  string `json:"name"`
			Shape string `json:"shape"`
		} `json:"sites"`
		Skipped int `json:"skipped"`
		Parsed  int `json:"parsed"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("decode pySites output: %v\n%s", err, out)
	}
	if payload.Skipped > 0 {
		t.Logf("pySites: %d corpus file(s) skipped (parse/read failure)", payload.Skipped)
	}

	sites := make([]pySite, 0, len(payload.Sites))
	for _, s := range payload.Sites {
		sites = append(sites, pySite{
			RelPath: s.Rel,
			Line:    s.Line,
			Col:     s.Col,
			Name:    s.Name,
			Shape:   pyShape(s.Shape),
		})
	}
	return sites, pySitesStats{Skipped: payload.Skipped, Parsed: payload.Parsed}
}

// pyShapesFixture exercises every shape in the plan's section 8 table:
//
//	bare-call    -> `build`  (@build(x)) and `helper` (helper(MAX_SIZE))
//	bare-const   -> `MAX_SIZE` (TOTAL = MAX_SIZE)
//	bare-name    -> `helper` (return helper, no call) and `x`  (@build(x) arg)
//	attr-base    -> `os`     (os.path.join(...))
//	decorator    -> `register` (@register, no parens)
//	class-base   -> `Base`   (class Widget(Base):)
//	annotation   -> `Widget` (param annotation and -> return annotation)
//	except-class -> `Widget` (except Widget:)
//	attr-member  -> `path`, `join` (os.path.join(...))
//
// The unicode_case function adds one non-ASCII line so the Col assertion can
// prove pySites reports a BYTE offset, not a rune offset: "café" is a
// 5-byte, 4-rune UTF-8 string, so a rune-offset bug and a byte-offset-correct
// answer disagree on where `helper` starts.
//
// unicode_attr adds a non-ASCII ATTRIBUTE name spread across multiple
// physical lines, so the Line/Col assertions on it can kill both the
// end_lineno->lineno and the end_col_offset-len(attr)->col_offset mutations
// for attr-member specifically — a plain single-line ASCII case can't tell
// those apart, since lineno==end_lineno and col_offset happens to be close
// to the derived column for a short single-line attribute chain (#313
// review F1).
//
// store_targets adds Store/Del attribute targets (obj.attr = 1, del
// obj.gone, for obj.tgt in xs:) alongside the pre-existing Store NAME
// targets above (MAX_SIZE, TOTAL, note) so TestPySitesShapes can assert
// ABSENCE: dropping either ctx==Load filter must fail a test, not just
// leave a site un-asserted (#313 review F2).
//
// uses_builtin_named_attr adds `x.format()` so TestPySitesShapes can assert
// PRESENCE of an attr-member whose name collides with a builtin function
// name — BUILTINS must never be applied to Attribute.attr (#313 review F4).
const pyShapesFixture = `import os

MAX_SIZE = 10
TOTAL = MAX_SIZE
_PRIVATE_MAX = 10
_PRIVATE_TOTAL = _PRIVATE_MAX


@register
def bare_decorated():
    pass


@build(x)
def called_decorator():
    pass


class Widget(Base):
    pass


def helper(x):
    return x


def use_helper():
    return helper(MAX_SIZE)


def read_only():
    return helper


def check_type(x: Widget) -> Widget:
    pass


try:
    pass
except Widget:
    pass


os.path.join("a")


def unicode_case():
    note = "café" + helper
    return note


def unicode_attr():
    result = (
        thing
        .résumé
    )
    return result


def store_targets():
    obj.attr = 1
    del obj.gone
    for obj.tgt in xs:
        pass


def uses_builtin_named_attr():
    return x.format()
`

func TestPySitesShapes(t *testing.T) {
	staged := t.TempDir()
	const rel = "shapes.py"
	if err := os.WriteFile(filepath.Join(staged, rel), []byte(pyShapesFixture), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	sites, stats := pySites(t, staged, []string{rel})
	if stats.Skipped != 0 || stats.Parsed != 1 {
		t.Fatalf("pySites stats: got Skipped=%d Parsed=%d, want Skipped=0 Parsed=1", stats.Skipped, stats.Parsed)
	}

	fixtureLines := strings.Split(pyShapesFixture, "\n")
	lineOf := func(needle string) int {
		t.Helper()
		for i, l := range fixtureLines {
			if strings.Contains(l, needle) {
				return i + 1
			}
		}
		t.Fatalf("fixture missing a line containing %q", needle)
		return 0
	}

	has := func(line int, name string, shape pyShape) bool {
		for _, s := range sites {
			if s.RelPath == rel && s.Line == line && s.Name == name && s.Shape == shape {
				return true
			}
		}
		return false
	}

	want := []struct {
		line  int
		name  string
		shape pyShape
	}{
		{lineOf("@build(x)"), "build", pyShapeBareCall},
		{lineOf("return helper(MAX_SIZE)"), "helper", pyShapeBareCall},
		{lineOf("TOTAL = MAX_SIZE"), "MAX_SIZE", pyShapeBareConst},
		// Leading-underscore SCREAMING_SNAKE names classify as bare-name, not
		// bare-const (#313 review F4 — is_screaming excludes them because
		// extract.go's reUpperSnakeRef can never match a name starting with
		// "_": no \b fires between two adjacent \w characters). Pins the
		// exclusion so a future edit to is_screaming can't silently widen the
		// bare-const population back past what the guard can actually see.
		{lineOf("_PRIVATE_TOTAL = _PRIVATE_MAX"), "_PRIVATE_MAX", pyShapeBareName},
		// "def read_only():" is unique; its next line is the bare (no-call)
		// `return helper` — "return helper" alone would also match the
		// "return helper(MAX_SIZE)" line above, since that contains it as a
		// substring.
		{lineOf("def read_only():") + 1, "helper", pyShapeBareName},
		{lineOf("@build(x)"), "x", pyShapeBareName},
		{lineOf(`os.path.join("a")`), "os", pyShapeAttrBase},
		{lineOf("@register"), "register", pyShapeDecorator},
		{lineOf("class Widget(Base):"), "Base", pyShapeClassBase},
		{lineOf("def check_type"), "Widget", pyShapeAnnotation},
		{lineOf("except Widget:"), "Widget", pyShapeExceptClass},
		{lineOf(`os.path.join("a")`), "path", pyShapeAttrMember},
		{lineOf(`os.path.join("a")`), "join", pyShapeAttrMember},
		// #313 review F4: BUILTINS must not filter Attribute.attr — `format`
		// is also a builtin function name, but x.format() has nothing to do
		// with it.
		{lineOf("return x.format()"), "format", pyShapeAttrMember},
	}
	for _, w := range want {
		if !has(w.line, w.name, w.shape) {
			t.Errorf("missing site line=%d name=%q shape=%q in %+v", w.line, w.name, w.shape, sites)
		}
	}

	// #313 review F2: Store/Del targets must never join the pool, on either
	// the ast.Name branch (MAX_SIZE, TOTAL, note — pre-existing Store NAME
	// targets in this fixture) or the ast.Attribute branch (obj.attr, del
	// obj.gone, for obj.tgt in xs:). Dropping either ctx==Load filter
	// previously left TestPySitesShapes green, because it only ever
	// asserted presence, never absence.
	hasName := func(line int, name string) bool {
		for _, s := range sites {
			if s.RelPath == rel && s.Line == line && s.Name == name {
				return true
			}
		}
		return false
	}
	for _, absent := range []struct {
		needle, name string
	}{
		{"MAX_SIZE = 10", "MAX_SIZE"},
		{"TOTAL = MAX_SIZE", "TOTAL"},
		{`note = "café" + helper`, "note"},
		{"obj.attr = 1", "attr"},
		{"del obj.gone", "gone"},
		{"for obj.tgt in xs:", "tgt"},
	} {
		if hasName(lineOf(absent.needle), absent.name) {
			t.Errorf("Store/Del target %q on line %q must not appear as any site — a definition site joining the pool corrupts the mutation-proof line downstream (#313 review F2)", absent.name, absent.needle)
		}
	}

	// #313 review F1: attr-member Line/Col must be independently pinned,
	// not just presence-checked. Uses a non-ASCII attribute name
	// (`résumé`, 6 chars / 8 UTF-8 bytes) split across physical lines so
	// both mutations are individually lethal:
	//   - end_col_offset - len(attr) mutated to col_offset: wrong column
	//     even on this line, since col_offset points at `thing`, not
	//     `.résumé`, AND a byte/rune mismatch would also show up here.
	//   - end_lineno mutated to lineno: wrong LINE — lineno is the line of
	//     `thing` (the attribute's value), not the line of `.résumé`.
	attrLine := lineOf(".résumé")
	attrRawLine := fixtureLines[attrLine-1]
	wantAttrCol := strings.Index(attrRawLine, "résumé")
	if wantAttrCol < 0 {
		t.Fatalf("fixture line %q missing résumé", attrRawLine)
	}
	foundAttr := false
	for _, s := range sites {
		if s.RelPath == rel && s.Name == "résumé" && s.Shape == pyShapeAttrMember {
			foundAttr = true
			if s.Line != attrLine {
				t.Errorf("attr-member Line: got %d, want %d (end_lineno — the line of `.résumé`, not lineno, the line of the attribute's base value)", s.Line, attrLine)
			}
			if s.Col != wantAttrCol {
				t.Errorf("attr-member Col byte-offset mismatch: got %d, want %d (line %q) — must subtract the attr's own UTF-8 byte length, not len(attr)", s.Col, wantAttrCol, attrRawLine)
			}
		}
	}
	if !foundAttr {
		t.Fatalf("no attr-member site found for non-ASCII attribute `résumé`")
	}

	// Byte-offset proof: "café" is 5 UTF-8 bytes / 4 runes before the space
	// that follows it, so if pySites (or the CPython col_offset it forwards)
	// were rune-indexed, this Col would be 1 byte short of where `helper`
	// actually starts.
	ucLine := lineOf(`"café" + helper`)
	rawLine := fixtureLines[ucLine-1]
	wantCol := strings.Index(rawLine, "helper")
	if wantCol < 0 {
		t.Fatalf("fixture line %q missing helper", rawLine)
	}
	found := false
	for _, s := range sites {
		if s.RelPath == rel && s.Line == ucLine && s.Name == "helper" && s.Shape == pyShapeBareName {
			found = true
			if s.Col != wantCol {
				t.Errorf("Col byte-offset mismatch on unicode line: got %d, want %d (line %q)", s.Col, wantCol, rawLine)
			}
		}
	}
	if !found {
		t.Fatalf("no bare-name `helper` site found on unicode line %d", ucLine)
	}

	// Robustness: a sibling file that fails to parse must not take the whole
	// sweep down, and must not poison the valid file's sites.
	const brokenRel = "broken.py"
	if err := os.WriteFile(filepath.Join(staged, brokenRel), []byte("def broken(:\n    pass\n"), 0o600); err != nil {
		t.Fatalf("write broken fixture: %v", err)
	}
	sitesWithBroken, brokenStats := pySites(t, staged, []string{rel, brokenRel})
	if brokenStats.Skipped != 1 || brokenStats.Parsed != 1 {
		t.Errorf("pySites stats with a broken sibling: got Skipped=%d Parsed=%d, want Skipped=1 Parsed=1", brokenStats.Skipped, brokenStats.Parsed)
	}
	stillHas := func(line int, name string, shape pyShape) bool {
		for _, s := range sitesWithBroken {
			if s.RelPath == rel && s.Line == line && s.Name == name && s.Shape == shape {
				return true
			}
		}
		return false
	}
	if !stillHas(lineOf("@register"), "register", pyShapeDecorator) {
		t.Errorf("an unparseable sibling file corrupted results for the valid file")
	}
	for _, s := range sitesWithBroken {
		if s.RelPath == brokenRel {
			t.Errorf("unparseable file %s must be skipped, not produce sites: %+v", brokenRel, s)
		}
	}
}
