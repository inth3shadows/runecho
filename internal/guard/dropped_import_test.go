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

// Python: dropped constant import still used as a subscript — obs-py-001 pattern.
func TestDroppedImport_Python_ConstStillUsed(t *testing.T) {
	oldText := "from ..models import TASTING_ROOM_KIND\nx = TASTING_ROOM_KIND[t]\n"
	newText := "x = TASTING_ROOM_KIND[t]\n"
	got := DroppedImportRefs(LangPython, oldText, newText)
	if !containsStr(droppedNames(got), "TASTING_ROOM_KIND") {
		t.Errorf("expected TASTING_ROOM_KIND flagged, got %v", droppedNames(got))
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

// Go is excluded by design (imports are package-qualified).
func TestDroppedImport_Go_Excluded(t *testing.T) {
	oldText := "import \"x\"\nx.Foo()\n"
	newText := "x.Foo()\n"
	if got := DroppedImportRefs(LangGo, oldText, newText); got != nil {
		t.Errorf("Go must be excluded, got %v", droppedNames(got))
	}
}
