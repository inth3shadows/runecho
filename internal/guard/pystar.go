package guard

import (
	"io"
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
// abstain, its names are folded in for this file, and a name outside it is still
// flagged.

// pyStarSourceMaxBytes caps the source read for a star-import target. A larger
// file is treated as not fully known (abstain), the fail-open direction.
const pyStarSourceMaxBytes = 1 << 20

// pyAllHead matches, on a line with strings and comments masked, a column-0
// `__all__ =` (optionally annotated) up to and including the opening bracket of
// a list or tuple.
var pyAllHead = regexp.MustCompile(`^__all__\s*(?::[^=]*)?=\s*[\[(]`)

// rePyStarImportStmt matches a star import at the start of a statement on a
// masked line: the line start, after a `;`, or after a compound statement's
// colon (`if X: from m import *`) (P13). The masking is what makes the `;` and
// `:` safe to trust; filescope's rePyStarImport reads unmasked lines and stays
// line-start only.
var rePyStarImportStmt = regexp.MustCompile(`(?:^|[;:])\s*from\s+([.\w]+)\s+import\s+\*`)

// pyIdent is a Python identifier, the only thing a star import can bind.
var pyIdent = regexp.MustCompile(`^[\p{L}_][\p{L}\p{N}_]*$`)

// PyStarEnv says where star-import sources are read from.
type PyStarEnv struct {
	// Root, when set, is the repo root: the upward module search stops there. When
	// empty the search stops at the first directory holding .git.
	Root string
	// Read returns a source file's bytes, or ok=false when it does not exist or
	// cannot be read. nil reads the working tree. The pre-commit path supplies a
	// staged-blob reader so the star source is judged as committed (#524).
	Read func(abs string) (data []byte, ok bool)
}

// PyStarResult is the outcome for one file. Reason is "star-import" when the
// main check must abstain for the file. Names are the __all__ names of the
// fully-known sources, to be treated as resolved for the file.
type PyStarResult struct {
	Reason string
	Names  []string
}

// PyStarImport finds the file's `from m import *` statements and decides whether
// the additive check can still run. wholeFile is the file the decision is made
// on: the post-edit file in the hook, or the staged file at commit. fd carries
// added lines not already in it (with their string-state seeds), for a hook
// edit that could not be replayed; both are
// scanned with string and docstring content masked, so an example in a
// docstring does not count as an import.
func PyStarImport(importerPath string, env PyStarEnv, wholeFile []AddedLine, fd FileDiff) PyStarResult {
	var modules []string
	seen := make(map[string]struct{})
	collect := func(lines []AddedLine, seed func(int) string) {
		scanStrippedSeeded(LangPython, lines, seed, func(scan string, _ AddedLine) {
			for _, m := range rePyStarImportStmt.FindAllStringSubmatch(scan, -1) {
				if _, dup := seen[m[1]]; !dup {
					seen[m[1]] = struct{}{}
					modules = append(modules, m[1])
				}
			}
		})
	}
	collect(wholeFile, nil)
	collect(fd.AddedLines, seedFunc(LangPython, fd.withSeeds()))

	var res PyStarResult
	for _, mod := range modules {
		names, ok := pyStarSourceNames(importerPath, mod, env)
		if !ok {
			return PyStarResult{Reason: "star-import"}
		}
		res.Names = append(res.Names, names...)
	}
	return res
}

// pyStarSourceNames returns the __all__ names of module (as written after
// `from`) when it resolves to an in-repo .py file declaring a single literal
// __all__. A module with no __all__ exports every public global it binds,
// including its own imports and whatever runs at import time, so it is not
// known; neither is an external or unresolvable module.
func pyStarSourceNames(importerPath, module string, env PyStarEnv) ([]string, bool) {
	read := env.Read
	if read == nil {
		read = readDiskCapped
	}
	path, src, ok := resolvePyModule(importerPath, module, env.Root, read)
	if !ok || path == "" || len(src) > pyStarSourceMaxBytes {
		return nil, false
	}
	text := strings.ReplaceAll(string(src), "\r\n", "\n")
	return pyLiteralAllNames(text)
}

// pyLiteralAllNames reads a module's __all__ when its export set is fully
// known: __all__ is mentioned exactly once in code (strings, docstrings and
// comments masked first, so an example in a docstring neither counts as the
// assignment nor blocks it), and that mention is a column-0 assignment of a list
// or tuple holding only plain string literals, with nothing but a comment after
// the closing bracket. Inside the brackets, comments are skipped (a quoted name
// in a comment is not an export) and adjacent literals are joined, as Python
// joins them. `__all__ +=`, `.extend(`, an indented or computed value all fail
// one of these, and each makes the set dynamic.
func pyLiteralAllNames(text string) ([]string, bool) {
	lines := strings.Split(text, "\n")
	at, col, open := -1, 0, ""
	for i, l := range lines {
		scan, o := stripLiteralsStateful(LangPython, l, open)
		open = o
		switch n := strings.Count(scan, "__all__"); {
		case n == 0:
			continue
		case n > 1 || at >= 0:
			return nil, false
		}
		m := pyAllHead.FindStringIndex(scan)
		if m == nil {
			return nil, false
		}
		at, col = i, m[1]-1
	}
	if at < 0 {
		return nil, false
	}
	rest := strings.Join(lines[at:], "\n")[col:]
	closer := byte(']')
	if rest[0] == '(' {
		closer = ')'
	}
	names := []string{}
	cur, pending := "", false
	for i := 1; i < len(rest); {
		switch c := rest[i]; {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '#':
			k := strings.IndexByte(rest[i:], '\n')
			if k < 0 {
				return nil, false
			}
			i += k
		case c == ',':
			if !pending {
				return nil, false
			}
			names, cur, pending = append(names, cur), "", false
			i++
		case c == '"' || c == '\'':
			k := strings.IndexByte(rest[i+1:], c)
			// No close, an escape or line break inside, or a triple quote: not a
			// plain literal.
			if k < 0 || strings.ContainsAny(rest[i+1:i+1+k], "\\\n") ||
				(k == 0 && i+2 < len(rest) && rest[i+2] == c) {
				return nil, false
			}
			cur, pending = cur+rest[i+1:i+1+k], true
			i += k + 2
		case c == closer:
			if pending {
				names = append(names, cur)
			}
			tail := rest[i+1:]
			if k := strings.IndexByte(tail, '\n'); k >= 0 {
				tail = tail[:k]
			}
			if tail = strings.TrimSpace(tail); tail != "" && tail[0] != '#' {
				return nil, false
			}
			for _, n := range names {
				if !pyIdent.MatchString(n) {
					return nil, false
				}
			}
			return names, true
		default:
			return nil, false
		}
	}
	return nil, false
}

// readDiskCapped reads a working-tree file, refusing one over the size cap.
func readDiskCapped(abs string) ([]byte, bool) {
	f, err := os.Open(abs)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(f, pyStarSourceMaxBytes+1))
	if err != nil {
		return nil, false
	}
	return data, true
}

// resolvePyModule maps a `from <module> import *` to an in-repo file and its
// bytes. A leading dot count is relative to importerPath's directory. An
// absolute dotted name is searched from the import roots: the repo root, its
// src/ directory, then the directories between the root and the importer,
// outermost first. The importer's own directory is therefore a root of last
// resort (a script run in place), never the first place looked, which would be
// Python 2 implicit relative import. In each root a package (name/__init__.py)
// wins over a module (name.py), as in Python's own finder. ok=false when
// nothing resolves: the module is external or absent.
func resolvePyModule(importerPath, module, root string, read func(string) ([]byte, bool)) (string, []byte, bool) {
	dots := len(module) - len(strings.TrimLeft(module, "."))
	rel := filepath.Join(strings.Split(strings.TrimLeft(module, "."), ".")...)
	try := func(base string) (string, []byte, bool) {
		cands := []string{filepath.Join(base, rel, "__init__.py")}
		if rel != "" {
			cands = append(cands, filepath.Join(base, rel)+".py")
		}
		for _, c := range cands {
			if data, ok := read(c); ok {
				return c, data, true
			}
		}
		return "", nil, false
	}
	dir := filepath.Dir(importerPath)
	if dots > 0 {
		base := dir
		for i := 1; i < dots; i++ {
			base = filepath.Dir(base)
		}
		return try(base)
	}
	// Ancestors of the importer, innermost first, up to the repo root.
	var chain []string
	cur := dir
	for i := 0; i < 16; i++ {
		chain = append(chain, cur)
		if root != "" {
			if cur == root {
				break
			}
		} else if _, err := os.Stat(filepath.Join(cur, ".git")); err == nil {
			break
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	top := chain[len(chain)-1]
	roots := []string{top, filepath.Join(top, "src")}
	for i := len(chain) - 2; i >= 0; i-- { // outermost first, root already tried
		roots = append(roots, chain[i])
	}
	for _, r := range roots {
		if p, d, ok := try(r); ok {
			return p, d, true
		}
	}
	return "", nil, false
}
