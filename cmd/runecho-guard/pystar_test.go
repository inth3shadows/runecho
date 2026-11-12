package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inth3shadows/runecho/internal/gitutil"
	"github.com/inth3shadows/runecho/internal/ir"
)

// pyStarRepo builds a real git repo holding the given files, indexes it with the
// real IR generator and enrolls it, so the hook resolves names exactly as in
// production (#477: the repro depends on what the parser records for an
// in-repo module). It returns the repo root.
func pyStarRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	repo := t.TempDir()
	gitInit(t, repo)
	for rel, content := range files {
		p := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	top, err := gitutil.TopLevel(repo)
	if err != nil {
		t.Fatalf("toplevel: %v", err)
	}
	irData, _, err := ir.NewGenerator(ir.GeneratorConfig{}).Generate(top)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	enrolledStoreWithFiles(t, top, irData.Files)
	return top
}

// pyStarFlagged runs a Write of content to rel through the hook and returns the
// ask's reason text ("" when the edit was not asked about).
func pyStarFlagged(t *testing.T, repo, rel, content string) string {
	t.Helper()
	code, _, d := runHook(t, payload(t, "Write", filepath.Join(repo, filepath.FromSlash(rel)), "", content, nil))
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if d.Hook.PermissionDec != "ask" {
		return ""
	}
	return d.Hook.PermissionReason
}

// issue477Models is pkg/models.py from the issue's repro: three public globals
// bound by non-def statements, and no __all__.
const issue477Models = `import functools
from collections import namedtuple
def _impl(x): return x
get_config = functools.lru_cache()(_impl)
square = lambda x: x*x
Point = namedtuple("Point", "x y")
def real_func(): pass
`

// TestPyStarImport_Issue477 is the issue's end-to-end repro.
func TestPyStarImport_Issue477(t *testing.T) {
	repo := pyStarRepo(t, map[string]string{
		"pkg/__init__.py": "",
		"pkg/models.py":   issue477Models,
	})

	app := "from pkg.models import *\n\nreal_func()\nget_config(1)\nsquare(2)\nPoint(1, 2)\n"
	if got := pyStarFlagged(t, repo, "pkg/app.py", app); got != "" {
		t.Errorf("names reached through `from pkg.models import *` were flagged:\n%s", got)
	}
	// An external module's exports cannot be enumerated: abstain.
	ui := "from tkinter import *\n\nroot = Tk()\n"
	if got := pyStarFlagged(t, repo, "pkg/ui.py", ui); got != "" {
		t.Errorf("Tk() through `from tkinter import *` was flagged:\n%s", got)
	}
}

// TestPyStarImport_StillFlagsWithoutStarImport: the abstain is per file. The
// same unresolved name, with no star import, is still a hallucination.
func TestPyStarImport_StillFlagsWithoutStarImport(t *testing.T) {
	repo := pyStarRepo(t, map[string]string{
		"pkg/__init__.py": "",
		"pkg/models.py":   issue477Models,
	})
	app := "from pkg.models import real_func\n\nreal_func()\nnot_a_real_function(1)\n"
	got := pyStarFlagged(t, repo, "pkg/app.py", app)
	if !strings.Contains(got, "not_a_real_function") {
		t.Errorf("a file with no star import must still flag a nonexistent function, got: %q", got)
	}
}

// TestPyStarImport_CompleteAllStillFlags: an in-repo star-import source with a
// literal __all__ has a fully known export set, so a name outside it is flagged.
// The abstain applies only when the exports are not fully known.
func TestPyStarImport_CompleteAllStillFlags(t *testing.T) {
	repo := pyStarRepo(t, map[string]string{
		"pkg/__init__.py": "",
		"pkg/models.py":   "__all__ = [\"real_func\", \"get_config\"]\nimport functools\ndef _impl(x): return x\nget_config = functools.lru_cache()(_impl)\ndef real_func(): pass\n",
	})
	app := "from pkg.models import *\n\nreal_func()\nget_config(1)\nnot_in_all(2)\n"
	got := pyStarFlagged(t, repo, "pkg/app.py", app)
	if !strings.Contains(got, "not_in_all") {
		t.Errorf("a name outside a complete __all__ must still be flagged, got: %q", got)
	}
	if strings.Contains(got, "real_func") || strings.Contains(got, "get_config") {
		t.Errorf("names listed in __all__ must resolve, got: %q", got)
	}
}

// TestPyStarImport_EditOutsideStarImportHunk: the star import sits in the
// pre-edit file, outside the edited hunk (the common Edit shape).
func TestPyStarImport_EditOutsideStarImportHunk(t *testing.T) {
	repo := pyStarRepo(t, map[string]string{
		"pkg/__init__.py": "",
		"pkg/ui.py":       "from tkinter import *\n\nroot = Tk()\n",
	})
	f := filepath.Join(repo, "pkg", "ui.py")
	code, _, d := runHook(t, payloadOld(t, "Edit", f, "root = Tk()", "root = Tk()\nlabel = Label(root)", "", nil))
	if code != 0 || d.Hook.PermissionDec == "ask" {
		t.Errorf("Label() through a star import outside the hunk was flagged: code=%d %q", code, d.Hook.PermissionReason)
	}
}

// writeFile writes content at repo-relative rel, creating directories.
func pyStarWrite(t *testing.T, repo, rel, content string) {
	t.Helper()
	p := filepath.Join(repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// P2: a Write's added text is the whole proposed file, so a star import in the
// stale pre-edit copy that the Write drops must not abstain.
func TestPyStarImport_WriteDroppingStarImportStillFlags(t *testing.T) {
	repo := pyStarRepo(t, map[string]string{
		"pkg/__init__.py": "",
		"pkg/ui.py":       "from tkinter import *\n\nroot = Tk()\n",
	})
	got := pyStarFlagged(t, repo, "pkg/ui.py", "root = Tk()\n")
	if !strings.Contains(got, "Tk") {
		t.Errorf("a Write that removes the star import must flag Tk, got %q", got)
	}
}

// P3: a docstring that merely shows a star import must not blind the file.
func TestPyStarImport_DocstringExampleDoesNotAbstain(t *testing.T) {
	repo := pyStarRepo(t, map[string]string{"pkg/__init__.py": ""})
	src := "\"\"\"Usage:\n\n    from foo import *\n\"\"\"\n\nnot_a_real_function(1)\n"
	if got := pyStarFlagged(t, repo, "pkg/doc.py", src); !strings.Contains(got, "not_a_real_function") {
		t.Errorf("a docstring example must not abstain, got %q", got)
	}
}

// P4: the literal __all__ names resolve even when the index has never seen them
// (stale or unindexed source), so the #477 false positive cannot come back.
func TestPyStarImport_LiteralAllNamesFoldedWhenIndexStale(t *testing.T) {
	repo := pyStarRepo(t, map[string]string{
		"pkg/__init__.py": "",
		"pkg/models.py":   "def old(): pass\n",
	})
	pyStarWrite(t, repo, "pkg/models.py", "__all__ = [\"fresh_name\"]\nfresh_name = lambda: 1\n")
	app := "from pkg.models import *\n\nfresh_name()\nnot_in_all()\n"
	got := pyStarFlagged(t, repo, "pkg/app.py", app)
	if strings.Contains(got, "fresh_name") {
		t.Errorf("a name in the source's literal __all__ was flagged: %q", got)
	}
	if !strings.Contains(got, "not_in_all") {
		t.Errorf("a name outside __all__ must still be flagged, got %q", got)
	}
}

// P9: an Edit's star-import decision is made on the post-edit file, so an Edit
// that replaces the star import no longer abstains on the pre-edit copy.
func TestPyStarImport_EditDroppingStarImportFlags(t *testing.T) {
	repo := pyStarRepo(t, map[string]string{
		"pkg/__init__.py": "",
		"pkg/ui.py":       "from tkinter import *\n\nroot = Tk()\n",
	})
	f := filepath.Join(repo, "pkg", "ui.py")
	_, _, d := runHook(t, payloadOld(t, "Edit", f,
		"from tkinter import *\n\nroot = Tk()",
		"import tkinter\n\nroot = tkinter.Tk()\nzz_not_real()", "", nil))
	if d.Hook.PermissionDec != "ask" || !strings.Contains(d.Hook.PermissionReason, "zz_not_real") {
		t.Errorf("an Edit that drops the star import must flag zz_not_real, got %+v", d.Hook)
	}
}

// P9: a MultiEdit that adds a star import in one edit and a name it binds in
// another does not flag the name.
func TestPyStarImport_MultiEditAddingStarImport(t *testing.T) {
	repo := pyStarRepo(t, map[string]string{
		"pkg/__init__.py": "",
		"pkg/ui.py":       "import os\n\nroot = os.getcwd()\n",
	})
	f := filepath.Join(repo, "pkg", "ui.py")
	_, _, d := runHook(t, payloadOld(t, "MultiEdit", f, "", "", "", []editOp{
		{OldString: "import os\n", NewString: "import os\nfrom tkinter import *\n"},
		{OldString: "root = os.getcwd()", NewString: "root = Tk()"},
	}))
	if d.Hook.PermissionDec == "ask" {
		t.Errorf("Tk() bound by a star import added in the same MultiEdit was flagged: %q", d.Hook.PermissionReason)
	}
}
