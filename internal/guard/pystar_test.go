package guard

import (
	"os"
	"path/filepath"
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
			got := PyStarImportReason(importer, TextToAddedLines("import os\n"+tc.line+"\n"))
			if got != tc.want {
				t.Errorf("PyStarImportReason(%q) = %q, want %q", tc.line, got, tc.want)
			}
		})
	}
	// The import may be in either slice (pre-edit file or hunk).
	if got := PyStarImportReason(importer, nil, TextToAddedLines("from tkinter import *\n")); got != "star-import" {
		t.Errorf("star import in the added lines = %q, want star-import", got)
	}
}
