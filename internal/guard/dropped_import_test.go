package guard

import "testing"

func droppedNames(ds []DroppedImport) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.Name
	}
	return out
}

// Python: a whole-file rewrite that drops `from ulid import ULID` but still calls
// ULID() — the obs-py-002 pattern.
func TestDroppedImport_Python_StillCalled(t *testing.T) {
	oldText := "from ulid import ULID\n\ndef make():\n    return str(ULID())\n"
	newText := "\ndef make():\n    return str(ULID())\n"
	got := DroppedImportRefs(LangPython, oldText, newText)
	if !containsStr(droppedNames(got), "ULID") {
		t.Errorf("expected ULID flagged as dropped import, got %v", droppedNames(got))
	}
}

// Python: a PEP8-wrapped def signature binds its parameters across lines. The
// old single-line rePyDefParams never matched `def foo(\n config,\n):`, so the
// param `config` was not recognized as a local binding and a dropped import of
// the same name false-positived. Pins the multi-line param accumulation.
func TestDroppedImport_Python_MultiLineDefParamBound(t *testing.T) {
	oldText := "from lib import config\n"
	newText := "def foo(\n    config,\n):\n    return config\n"
	got := DroppedImportRefs(LangPython, oldText, newText)
	if containsStr(droppedNames(got), "config") {
		t.Errorf("multi-line def param `config` is locally bound; must not flag as dropped import, got %v", droppedNames(got))
	}
}

// MultiEdit: an unterminated string in one edit block must not leak into the next
// and blank a real use of a dropped import. DroppedImportRefsLines with gap-
// separated lines (AddedLinesWithGap, as the hook builds for MultiEdit) resets the
// open-string state at the boundary; the old flat "\n"-join did not, silently
// missing the drop. Pins review finding #1.
func TestDroppedImport_MultiEditGap_NoStringLeak(t *testing.T) {
	oldLines := TextToAddedLines("import { ULID } from 'ulid';")
	b1 := "const q = `SELECT * FROM" // edit #1: unterminated template literal
	b2 := "return doStuff(ULID());"  // edit #2: real use of the dropped import

	gapped := AddedLinesWithGap([]string{b1, b2})
	if got := DroppedImportRefsLines(LangJS, oldLines, gapped); !containsStr(droppedNames(got), "ULID") {
		t.Errorf("gapped MultiEdit must see ULID() past the open template; got %v", droppedNames(got))
	}
	// Contrast: a flat contiguous join leaks the open template into b2 and misses it
	// (this is exactly what the old MultiEdit flat-join did).
	flat := TextToAddedLines(b1 + "\n" + b2)
	if got := DroppedImportRefsLines(LangJS, oldLines, flat); containsStr(droppedNames(got), "ULID") {
		t.Errorf("flat join was expected to leak string state and miss ULID (documents the bug)")
	}
}

// Python: a multi-line docstring containing example code that looks like a
// binding (`ULID = ...`) must NOT suppress a genuine dropped import. The stateless
// stripLiterals scanned the interior line in isolation, read `ULID = generate()`
// as a real rebind, and silently dropped the detection (a false negative). The
// stateful stripper blanks the docstring interior, so the real drop is flagged.
func TestDroppedImport_Python_DocstringExampleNotABinding(t *testing.T) {
	oldText := "from ulid import ULID\n\ndef make():\n    return ULID()\n"
	newText := "def make():\n    \"\"\"\n    Example: ULID = generate()\n    \"\"\"\n    return ULID()\n"
	got := DroppedImportRefs(LangPython, oldText, newText)
	if !containsStr(droppedNames(got), "ULID") {
		t.Errorf("expected ULID flagged despite docstring example, got %v", droppedNames(got))
	}
}

// Python: dropped constant import still used as a subscript — obs-py-001 pattern.
func TestDroppedImport_Python_ConstStillUsed(t *testing.T) {
	oldText := "from ..models import TASTING_ROOM_KIND\nx = TASTING_ROOM_KIND[t]\n"
	newText := "x = TASTING_ROOM_KIND[t]\n"
	got := DroppedImportRefs(LangPython, oldText, newText)
	if !containsStr(droppedNames(got), "TASTING_ROOM_KIND") {
		t.Errorf("expected TASTING_ROOM_KIND flagged, got %v", droppedNames(got))
	}
}

// Negative: a dropped import rebound as the SECOND declarator of a comma-separated
// const/let/var must not be flagged — the rebind is a legitimate local definition.
// Previously only the first declarator was seen, so the later rebind looked dropped.
func TestDroppedImport_JS_SecondDeclaratorRebound(t *testing.T) {
	oldText := "import { ULID } from 'ulid';\nreturn ULID();\n"
	newText := "const helper = doSomething(), ULID = () => Math.random();\nreturn ULID();\n"
	if got := DroppedImportRefs(LangJS, oldText, newText); len(got) != 0 {
		t.Errorf("ULID rebound as 2nd declarator must not warn, got %v", droppedNames(got))
	}
}

// Negative: the import is gone AND so is every use — a legitimate cleanup, stays
// silent (the false-positive killer).
func TestDroppedImport_Python_RemovedAndUnused(t *testing.T) {
	oldText := "from ulid import ULID\nx = 1\n"
	newText := "x = 1\n"
	if got := DroppedImportRefs(LangPython, oldText, newText); len(got) != 0 {
		t.Errorf("removed-and-unused import must not warn, got %v", droppedNames(got))
	}
}

// Negative: import retained in the new text — no drop.
func TestDroppedImport_Python_Retained(t *testing.T) {
	oldText := "from ulid import ULID\nx = ULID()\n"
	newText := "from ulid import ULID\nx = ULID()\ny = 2\n"
	if got := DroppedImportRefs(LangPython, oldText, newText); len(got) != 0 {
		t.Errorf("retained import must not warn, got %v", droppedNames(got))
	}
}

// Negative: the name is now provided by a local definition instead of the import.
func TestDroppedImport_Python_NowLocallyDefined(t *testing.T) {
	oldText := "from x import Helper\nv = Helper()\n"
	newText := "def Helper():\n    return 1\nv = Helper()\n"
	if got := DroppedImportRefs(LangPython, oldText, newText); len(got) != 0 {
		t.Errorf("locally-redefined name must not warn, got %v", droppedNames(got))
	}
}

// Negative: a surviving use is qualified (obj.ULID) — not the dropped binding.
func TestDroppedImport_Python_QualifiedUseIgnored(t *testing.T) {
	oldText := "from ulid import ULID\nx = ULID()\n"
	newText := "x = mod.ULID()\n"
	if got := DroppedImportRefs(LangPython, oldText, newText); len(got) != 0 {
		t.Errorf("qualified use must not count, got %v", droppedNames(got))
	}
}

// Negative: the surviving "use" is only inside a string/comment, blanked by the
// literal stripper.
func TestDroppedImport_Python_UseOnlyInStringIgnored(t *testing.T) {
	oldText := "from ulid import ULID\nx = ULID()\n"
	newText := "msg = \"ULID was here\"  # ULID\n"
	if got := DroppedImportRefs(LangPython, oldText, newText); len(got) != 0 {
		t.Errorf("string/comment-only mention must not count, got %v", droppedNames(got))
	}
}

// --- locally-rebound negatives: the import is gone but the name is re-provided
// by a local binding, so using it is valid. These are the false-positive classes
// the expert panel surfaced and an empirical probe confirmed. ---

func TestDroppedImport_Python_Rebound_Assignment(t *testing.T) {
	oldText := "from x import Foo\nFoo.bar()\n"
	newText := "Foo = make_foo()\nFoo.bar()\n"
	if got := DroppedImportRefs(LangPython, oldText, newText); len(got) != 0 {
		t.Errorf("assignment-rebound name must not warn, got %v", droppedNames(got))
	}
}

func TestDroppedImport_Python_Rebound_LoopVar(t *testing.T) {
	oldText := "from typing import TypeVar as TV\nx: TV\n"
	newText := "for TV in items:\n    print(TV)\n"
	if got := DroppedImportRefs(LangPython, oldText, newText); len(got) != 0 {
		t.Errorf("loop-variable name must not warn, got %v", droppedNames(got))
	}
}

func TestDroppedImport_Python_Rebound_WithAs(t *testing.T) {
	oldText := "from db import conn\nconn.run()\n"
	newText := "with pool() as conn:\n    conn.run()\n"
	if got := DroppedImportRefs(LangPython, oldText, newText); len(got) != 0 {
		t.Errorf("with-as-bound name must not warn, got %v", droppedNames(got))
	}
}

func TestDroppedImport_Python_Rebound_DefParam(t *testing.T) {
	oldText := "from x import handler\nhandler()\n"
	newText := "def run(handler):\n    handler()\n"
	if got := DroppedImportRefs(LangPython, oldText, newText); len(got) != 0 {
		t.Errorf("function-parameter name must not warn, got %v", droppedNames(got))
	}
}

func TestDroppedImport_JS_Rebound_Decl(t *testing.T) {
	oldText := "import { Foo } from './m';\nFoo();\n"
	newText := "const Foo = build();\nFoo();\n"
	if got := DroppedImportRefs(LangJS, oldText, newText); len(got) != 0 {
		t.Errorf("const-rebound name must not warn, got %v", droppedNames(got))
	}
}

// Negative: a dropped import rebound as an unparenthesized single-arg arrow
// param (`x => x*2`) must not be flagged. reJSArrowParams only matches the
// parenthesized form `(x) => …`, so a bare single-identifier arrow used to slip
// past LocallyBoundNames entirely and false-positive.
func TestDroppedImport_JS_Rebound_BareArrowParam(t *testing.T) {
	oldText := "import { x } from './m';\nreturn arr.map(x => x * 2);\n"
	newText := "return arr.map(x => x * 2);\n"
	if got := DroppedImportRefs(LangJS, oldText, newText); len(got) != 0 {
		t.Errorf("bare arrow param x must not warn as dropped import, got %v", droppedNames(got))
	}
}

func TestDroppedImport_JS_Rebound_CatchAndParam(t *testing.T) {
	oldText := "import { err } from './m';\nlog(err);\n"
	newText := "try { x(); } catch (err) { log(err); }\n"
	if got := DroppedImportRefs(LangJS, oldText, newText); len(got) != 0 {
		t.Errorf("catch-bound name must not warn, got %v", droppedNames(got))
	}
}

// Guard against over-suppression: the true positives must STILL fire after the
// binding-aware suppression is added — an LHS assignment of a DIFFERENT name must
// not shield a dropped import used on the right-hand side.
func TestDroppedImport_Python_RHSUseStillFlagged(t *testing.T) {
	oldText := "from ulid import ULID\nrid = ULID()\n"
	newText := "rid = ULID()\n" // 'rid' is bound; ULID is used on the RHS and was dropped
	got := DroppedImportRefs(LangPython, oldText, newText)
	if !containsStr(droppedNames(got), "ULID") {
		t.Errorf("RHS-used dropped import must still flag despite a bound LHS, got %v", droppedNames(got))
	}
}

// JS: dropped named import still used.
func TestDroppedImport_JS_StillUsed(t *testing.T) {
	oldText := "import { fetchUser } from './api';\nconst u = fetchUser(id);\n"
	newText := "const u = fetchUser(id);\n"
	got := DroppedImportRefs(LangJS, oldText, newText)
	if !containsStr(droppedNames(got), "fetchUser") {
		t.Errorf("expected fetchUser flagged, got %v", droppedNames(got))
	}
}

// A physical line that packs an import clause and a real statement, separated
// by ';' (`import re; x = SomeDroppedImport()`), must not have the trailing
// statement's use of a dropped import swallowed as if the whole line were
// import content. rePyImport/rePyFrom are anchored to end-of-line, so
// isImportLine classifies the WHOLE line as an import — firstUnqualifiedUseLines
// used to skip it outright, silently missing the use.
func TestDroppedImport_Python_SemicolonPackedLineStillScanned(t *testing.T) {
	oldText := "from utils import SomeDroppedImport\n\ndef run():\n    import re; x = SomeDroppedImport()\n    return x\n"
	newText := "\ndef run():\n    import re; x = SomeDroppedImport()\n    return x\n"
	got := DroppedImportRefs(LangPython, oldText, newText)
	if !containsStr(droppedNames(got), "SomeDroppedImport") {
		t.Errorf("expected SomeDroppedImport flagged despite semicolon-packed import line, got %v", droppedNames(got))
	}
}

// DroppedImportRefsLinesWithBound lets a caller fold in whole-file binding
// context a hunk-only scan can't see — a name rebound on an UNTOUCHED line
// elsewhere in the file. This mirrors the guard hook's false-positive: an
// Edit/MultiEdit's newLines only cover the touched hunk, so a name rebound
// outside it looks dropped even though the file as a whole still provides it.
func TestDroppedImportRefsLinesWithBound_WholeFileRebindSuppresses(t *testing.T) {
	oldLines := TextToAddedLines("import re\nx = re.compile(y)\n")
	newLines := TextToAddedLines("x = re.compile(y)\n") // hunk only; the untouched `re = custom_regex_module()` line elsewhere is not in this scan

	// Without whole-file context, the hunk-only scan (falsely) flags `re` as
	// dropped — this pins that the test setup actually reproduces the bug.
	if got := DroppedImportRefsLines(LangPython, oldLines, newLines); !containsStr(droppedNames(got), "re") {
		t.Fatalf("expected hunk-only scan to flag re as dropped (invalid test setup), got %v", droppedNames(got))
	}

	// With whole-file context folded in as preBound (the untouched line rebinds
	// `re`), the warning must be suppressed.
	preBound := LocallyBoundNames(LangPython, TextToAddedLines("re = custom_regex_module()\n"))
	if got := DroppedImportRefsLinesWithBound(LangPython, oldLines, newLines, preBound); len(got) != 0 {
		t.Errorf("whole-file rebind of re must suppress the dropped-import warning, got %v", droppedNames(got))
	}
}

// Round-2 regression pin (F8): a physical line that packs MULTIPLE import
// clauses (`import re; from x import Dropped`) must have EVERY leading import
// segment blanked, not just the clause before the first ';'. The pre-round-2
// fix blanked only up to the first ';', then scanned the trailing
// `from x import Dropped` as code — recording `Dropped` as a bogus "use", which
// (when Dropped's import is not recognized by ExtractImports) false-positives it
// as a dropped import. firstUnqualifiedUseLines is the exact site, so pin it
// directly: the trailing import's bound name must NOT appear as a use.
func TestFirstUnqualifiedUseLines_TrailingImportSegmentBlanked(t *testing.T) {
	lines := TextToAddedLines("import re; from x import Dropped\n")
	uses := firstUnqualifiedUseLines(LangPython, lines)
	if ln := uses["Dropped"]; ln != 0 {
		t.Errorf("Dropped is bound by the trailing import segment, must not be recorded as a use (got line %d)", ln)
	}
}

// Round-2 regression pin (F6): a dropped import rebound as a BARE arrow param on
// a line that ALSO carries a parenthesized arrow (`(a, b) => …; arr.map(Dropped
// => …)`) must be suppressed. The pre-round-2 fix gated the bare-arrow check
// behind `else if` off the parenthesized form, so the parenthesized match on the
// same line shadowed the bare arrow — `Dropped` never entered the bound set and
// false-positived as dropped despite being a local param.
func TestDroppedImport_JS_BareArrowParam_SameLineAsParenArrow(t *testing.T) {
	oldText := "import { Dropped } from './m';\n(a, b) => f(a, b);\narr.map(Dropped => Dropped * 2);\n"
	newText := "(a, b) => f(a, b); arr.map(Dropped => Dropped * 2);\n"
	if got := DroppedImportRefs(LangJS, oldText, newText); len(got) != 0 {
		t.Errorf("bare arrow param Dropped alongside a paren arrow must not warn as dropped, got %v", droppedNames(got))
	}
}

// Round-2b regression pin (C#1/A#1): an import clause packed AFTER a non-import
// statement on one line (`import a; x = 1; from n import Dropped`) must still be
// blanked. The leading-run-only version broke out of the peel loop at the
// non-import `x = 1` and scanned the trailing import as code, recording Dropped
// as a use — and ExtractImports' first-clause-only parse never put Dropped in
// newImps to offset it, so Dropped false-positived as a dropped import even
// though it is literally imported on the line. Blanking EVERY import segment
// (not just the leading run) fixes it.
func TestFirstUnqualifiedUseLines_NonLeadingImportSegmentBlanked(t *testing.T) {
	lines := TextToAddedLines("import a; x = 1; from n import Dropped\n")
	uses := firstUnqualifiedUseLines(LangPython, lines)
	if ln := uses["Dropped"]; ln != 0 {
		t.Errorf("Dropped is bound by a trailing import segment, must not be recorded as a use (got line %d)", ln)
	}
	if uses["x"] == 0 {
		t.Errorf("the genuine non-import segment `x = 1` must still be scanned (x missing from uses)")
	}
}

// Round-2b regression pin (A#2): a dropped import rebound as a bare arrow param
// that is NOT the first bare arrow on the line (`arr.map(x => x).forEach(Dropped
// => log(Dropped))`) must be suppressed. FindStringSubmatch captured only the
// first bare arrow (`x`), leaving Dropped unbound and false-positived; FindAll
// binds every bare arrow.
func TestDroppedImport_JS_SecondBareArrowParam(t *testing.T) {
	oldText := "import { Dropped } from './m';\narr.map(x => x).forEach(Dropped => log(Dropped));\n"
	newText := "arr.map(x => x).forEach(Dropped => log(Dropped));\n"
	if got := DroppedImportRefs(LangJS, oldText, newText); len(got) != 0 {
		t.Errorf("a later bare arrow param Dropped must not warn as dropped, got %v", droppedNames(got))
	}
}

// Go is excluded by design (imports are package-qualified).
func TestDroppedImport_Go_Excluded(t *testing.T) {
	oldText := "import \"x\"\nx.Foo()\n"
	newText := "x.Foo()\n"
	if got := DroppedImportRefs(LangGo, oldText, newText); got != nil {
		t.Errorf("Go must be excluded, got %v", droppedNames(got))
	}
}
