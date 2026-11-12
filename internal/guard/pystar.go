package guard

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// A Python `from m import *` binds every public global of m (or exactly the
// names in its __all__). The additive check resolves a bare name against the
// repo-wide symbol set, which cannot tell an unknowable star-imported name
// (`from tkinter import *; Tk()`) from a hallucination, so it must abstain
// whenever the star source's export set is not fully known (#477). The
// file-scope and call-shape checks already abstain on any star import with the
// "star-import" token (abstainReasonFileScope); this is the same token for the
// main check, narrowed by one case those two don't bother with: an in-repo
// source whose __all__ is a plain literal list is fully known, so it does not
// abstain and a name outside it is still flagged.

// pyStarSourceMaxBytes caps the source read for a star-import target. A larger
// file is treated as not fully known (abstain), the fail-open direction.
const pyStarSourceMaxBytes = 1 << 20

// pyLiteralAll matches a module's one `__all__` assignment when its value is a
// list or tuple holding only string literals (comments and line breaks
// allowed): the one shape whose export set is knowable without evaluating code.
var pyLiteralAll = regexp.MustCompile(`(?m)^__all__\s*(?::[^=\n]*)?=\s*[\[(](?:\s*(?:"\w*"|'\w*')\s*,?|\s*#[^\n]*)*\s*[\])]\s*(?:#[^\n]*)?$`)

// PyStarImportReason returns "star-import" when any of the given lines is a
// `from m import *` whose exported names are not fully known, and "" otherwise.
// importerPath is the file being edited; it anchors relative imports and the
// search for an in-repo source module. Pass the pre-edit whole file AND the
// added lines: an Edit's hunk usually does not contain the import. Scanned on
// raw text, so a match inside a string or comment only causes an extra abstain.
func PyStarImportReason(importerPath string, lineSets ...[]AddedLine) string {
	seen := make(map[string]struct{})
	for _, lines := range lineSets {
		for _, l := range lines {
			m := rePyStarImport.FindStringSubmatch(l.Text)
			if m == nil {
				continue
			}
			if _, dup := seen[m[1]]; dup {
				continue
			}
			seen[m[1]] = struct{}{}
			if !pyStarSourceKnown(importerPath, m[1]) {
				return "star-import"
			}
		}
	}
	return ""
}

// pyStarSourceKnown reports whether module (as written after `from`) resolves
// to an in-repo .py file that declares a literal __all__. A module with no
// __all__ exports every public global it binds, including its own imports and
// whatever runs at import time, so it is not treated as known even though the
// parser now records its public assignments. An external or unresolvable module
// is unknown.
func pyStarSourceKnown(importerPath, module string) bool {
	path := resolvePyModule(importerPath, module)
	if path == "" {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, pyStarSourceMaxBytes+1)
	n, _ := f.Read(buf)
	if n > pyStarSourceMaxBytes {
		return false
	}
	src := strings.ReplaceAll(string(buf[:n]), "\r\n", "\n")
	// Exactly one mention, and it is the literal assignment: `__all__ +=`,
	// `.extend(`, or a computed value would all show up as a second mention or
	// fail the literal match, and each makes the set dynamic.
	return strings.Count(src, "__all__") == 1 && pyLiteralAll.MatchString(src)
}

// resolvePyModule maps a `from <module> import *` to an in-repo file: a leading
// dot count is relative to importerPath's directory; an absolute dotted name is
// tried from importerPath's directory and each ancestor up to and including the
// first one holding .git (an import root can be the repo root or a src/ dir).
// "" when nothing resolves, i.e. the module is external or not found.
func resolvePyModule(importerPath, module string) string {
	dots := len(module) - len(strings.TrimLeft(module, "."))
	rel := filepath.Join(strings.Split(strings.TrimLeft(module, "."), ".")...)
	dir := filepath.Dir(importerPath)
	try := func(base string) string {
		for _, c := range []string{filepath.Join(base, rel) + ".py", filepath.Join(base, rel, "__init__.py")} {
			if rel == "" && strings.HasSuffix(c, ".py") && !strings.HasSuffix(c, "__init__.py") {
				continue
			}
			if st, err := os.Stat(c); err == nil && st.Mode().IsRegular() {
				return c
			}
		}
		return ""
	}
	if dots > 0 {
		base := dir
		for i := 1; i < dots; i++ {
			base = filepath.Dir(base)
		}
		return try(base)
	}
	for i := 0; i < 16; i++ {
		if p := try(dir); p != "" {
			return p
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}
