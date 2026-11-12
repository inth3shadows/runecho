package guard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPyStarImportReason pins which star imports make the main check abstain
// (#477): everything except an in-repo source with a single literal __all__.
func TestPyStarImportReason(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(rel, content string) string {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("pkg/__init__.py", "")
	write("pkg/lit.py", "__all__ = [\n    \"a\",  # first\n    'b',\n]\ndef a(): pass\n")
	write("pkg/lit_tuple.py", "__all__: list[str] = (\"a\",)\n")
	write("pkg/noall.py", "def a(): pass\n")
	write("pkg/plus.py", "__all__ = [\"a\"]\n__all__ += [\"b\"]\n")
	write("pkg/computed.py", "__all__ = base.__all__ + [\"a\"]\n")
	write("pkg/splat.py", "__all__ = [*base.__all__]\n")
	write("pkg/sub/__init__.py", "__all__ = [\"s\"]\n")
	importer := write("pkg/app.py", "")

	cases := []struct {
		name, line, want string
	}{
		{"external", "from tkinter import *", "star-import"},
		{"literal list is known", "from pkg.lit import *", ""},
		{"literal annotated tuple is known", "from pkg.lit_tuple import *", ""},
		{"package __init__ literal is known", "from pkg.sub import *", ""},
		{"relative literal is known", "from .lit import *", ""},
		{"no __all__", "from pkg.noall import *", "star-import"},
		{"__all__ extended", "from pkg.plus import *", "star-import"},
		{"computed __all__", "from pkg.computed import *", "star-import"},
		{"splat __all__", "from pkg.splat import *", "star-import"},
		{"missing in-repo module", "from pkg.nope import *", "star-import"},
		{"no star import", "from pkg.lit import a", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PyStarImport(importer, PyStarEnv{}, nil, FileDiff{AddedLines: TextToAddedLines("import os\n" + tc.line + "\n")}).Reason
			if got != tc.want {
				t.Errorf("PyStarImportReason(%q) = %q, want %q", tc.line, got, tc.want)
			}
		})
	}
	// The import may be in either slice (pre-edit file or hunk).
	if got := PyStarImport(importer, PyStarEnv{}, TextToAddedLines("from tkinter import *\n"), FileDiff{}).Reason; got != "star-import" {
		t.Errorf("star import in the added lines = %q, want star-import", got)
	}
}

// P7: an absolute import resolves from the import roots, not the importer's own
// directory first (py2 implicit relative), and a package wins over a module.
func TestResolvePyModule_RootsAndPackagePrecedence(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	mk := func(rel string) string {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	rootMod := mk("util.py")
	mk("pkg/util.py") // a sibling of the importer that py3 absolute imports do not see
	importer := mk("pkg/app.py")
	if got := resolvePyModuleDisk(importer, "util"); got != rootMod {
		t.Errorf("absolute import resolved to %q, want the root module %q", got, rootMod)
	}
	both := mk("both/__init__.py")
	mk("both.py")
	if got := resolvePyModuleDisk(importer, "both"); got != both {
		t.Errorf("package and module of one name resolved to %q, want the package %q", got, both)
	}
}

func resolvePyModuleDisk(importer, module string) string {
	p, _, _ := resolvePyModule(importer, module, "", readDiskCapped)
	return p
}

// TestPyLiteralAllNames pins the __all__ reader on comments, docstrings and
// implicit concatenation (P10, P11, P12 in the #564 review).
func TestPyLiteralAllNames(t *testing.T) {
	cases := []struct {
		name, src string
		want      []string // nil: not fully known
	}{
		{"plain", "__all__ = ['a', \"b\"]\n", []string{"a", "b"}},
		{"multi-line with comments", "__all__ = [\n    \"a\",  # first\n    'b',\n]\n", []string{"a", "b"}},
		{"P10 quoted name in a comment is not an export", "__all__ = [\n    \"real\",  # 'ghost' was removed\n]\n", []string{"real"}},
		{"P11 docstring __all__ only", "\"\"\"Example:\n__all__ = ['x']\n\"\"\"\ndef x(): pass\n", nil},
		{"P11 docstring mention plus real one", "\"\"\"Set __all__ below.\"\"\"\n__all__ = ['a']\n", []string{"a"}},
		{"comment mention plus real one", "# __all__ is literal\n__all__ = ['a']\n", []string{"a"}},
		{"P12 implicit concatenation", "__all__ = ['get_' 'config', 'b']\n", []string{"get_config", "b"}},
		{"trailing comment after close", "__all__ = ('a',)  # done\n", []string{"a"}},
		{"annotated", "__all__: list[str] = ['a']\n", []string{"a"}},
		{"empty", "__all__ = []\n", []string{}},
		{"computed", "__all__ = base.__all__ + ['a']\n", nil},
		{"splat", "__all__ = [*base]\n", nil},
		{"extended", "__all__ = ['a']\n__all__ += ['b']\n", nil},
		{"trailing expression", "__all__ = ['a'] + extra\n", nil},
		{"indented", "if X:\n    __all__ = ['a']\n", nil},
		{"non-identifier string", "__all__ = ['a-b']\n", nil},
		{"prefixed string", "__all__ = [b'a']\n", nil},
		{"unclosed", "__all__ = ['a',\n", nil},
		{"none", "def a(): pass\n", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := pyLiteralAllNames(tc.src)
			if tc.want == nil {
				if ok {
					t.Fatalf("want not known, got %q", got)
				}
				return
			}
			if !ok || strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("got %q ok=%v, want %q", got, ok, tc.want)
			}
		})
	}
}

// P13: a star import after `;` or a compound-statement colon is still found.
func TestPyStarImport_NotAtLineStart(t *testing.T) {
	for _, src := range []string{
		"import os; from tkinter import *\n",
		"if True: from tkinter import *\n",
		"import os; import sys; from tkinter import *\n",
	} {
		fd := FileDiff{Path: "a.py", AddedLines: TextToAddedLines(src)}
		if r := PyStarImport("/nowhere/a.py", PyStarEnv{Root: "/nowhere"}, nil, fd); r.Reason != "star-import" {
			t.Errorf("P13: %q: want star-import, got %+v", src, r)
		}
	}
	// Masked: inside a string it is not an import.
	fd := FileDiff{Path: "a.py", AddedLines: TextToAddedLines("x = 'a; from tkinter import *'\n")}
	if r := PyStarImport("/nowhere/a.py", PyStarEnv{Root: "/nowhere"}, nil, fd); r.Reason != "" {
		t.Errorf("P13: star import in a string: want none, got %+v", r)
	}
}
