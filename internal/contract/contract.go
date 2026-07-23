// Package contract implements RunEcho's edit-scope contracts: a human-authored
// declaration of which paths a piece of work is allowed to touch.
//
// A contract is the odd one out among RunEcho's checks. Every other one answers
// a question about FACT — does this symbol exist in this repo — which is
// computable from the index and where a false positive is always a bug. A
// contract answers a question about INTENT — is this edit inside what we agreed
// to change — which the repo cannot supply and which only a human can declare.
// An edit outside the declared scope is frequently legitimate (a necessary
// refactor, a missed dependency), so this is the one surface where a "false
// positive" is not a defect. Everything here is therefore built to stay silent
// unless a human explicitly declared a scope.
package contract

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"strings"
)

// Dir is the per-repo directory holding contract files, relative to the repo
// root. A contract lives in the repo — not in the database — so it is authored,
// reviewed in a PR diff, and versioned like any other source file. The PR
// reviewer is the audience that most needs to see a scope declaration.
const Dir = ".runecho/contracts"

// Contract is a parsed scope declaration.
type Contract struct {
	Name        string // from the `name:` header, else the file's base name
	Description string // from the `description:` header; optional
	Patterns    []Pattern
	Hash        string // sha256 of the file's exact bytes, for reproducibility
	Path        string // path the contract was loaded from
}

// Pattern is one glob line. Negated patterns (prefixed `!`) carve exceptions out
// of earlier matches, exactly as in .gitignore — the last pattern that matches a
// path decides, so `internal/**` followed by `!internal/**/*_test.go` means
// "all of internal except its tests".
type Pattern struct {
	Glob    string
	Negated bool
}

// The file format is deliberately the .gitignore / .runechoguardignore shape —
// one glob per line, `#` comments, a couple of `key: value` headers — and NOT
// YAML or JSON. YAML would be the first new direct dependency in a repo that has
// two; JSON cannot carry comments, and a scope declaration is a document meant
// to be read by a human in a PR diff, where the comment explaining WHY a path is
// in scope matters as much as the path. If a contract ever genuinely outgrows
// this, revisit — but do not pay for a parser before the need exists.

// Parse reads a contract from raw bytes. It never fails on a malformed line: an
// unrecognized header or an empty glob is skipped, because a contract that
// refuses to load would take the whole check offline, and this check's job is to
// be quiet and optional rather than load-bearing.
func Parse(src []byte, fromPath, fallbackName string) Contract {
	c := Contract{
		Name: fallbackName,
		Path: fromPath,
		Hash: hashBytes(src),
	}
	sc := bufio.NewScanner(strings.NewReader(string(src)))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Headers are only recognized before any glob, so a path containing a
		// colon later in the file cannot be swallowed as a header.
		if len(c.Patterns) == 0 {
			if k, v, ok := splitHeader(line); ok {
				switch k {
				case "name":
					if v != "" {
						c.Name = v
					}
				case "description":
					c.Description = v
				}
				continue
			}
		}
		negated := strings.HasPrefix(line, "!")
		glob := strings.TrimPrefix(line, "!")
		glob = strings.TrimPrefix(glob, "./")
		if glob == "" {
			continue
		}
		c.Patterns = append(c.Patterns, Pattern{Glob: glob, Negated: negated})
	}
	return c
}

// splitHeader recognizes `key: value` where key is a bare lowercase word. It is
// deliberately narrow so that a glob like `src:gen/**` is never mistaken for a
// header.
func splitHeader(line string) (string, string, bool) {
	i := strings.Index(line, ":")
	if i <= 0 {
		return "", "", false
	}
	k := line[:i]
	for _, r := range k {
		if r < 'a' || r > 'z' {
			return "", "", false
		}
	}
	if i+1 < len(line) && line[i+1] != ' ' {
		return "", "", false
	}
	return k, strings.TrimSpace(line[i+1:]), true
}

// Load reads a contract file from disk.
func Load(p string) (Contract, error) {
	src, err := os.ReadFile(p)
	if err != nil {
		return Contract{}, fmt.Errorf("read contract %s: %w", p, err)
	}
	return Parse(src, p, path.Base(p)), nil
}

// InScope reports whether relPath (a repo-relative, slash-separated path) is
// allowed by the contract. The LAST matching pattern wins, so a later `!` line
// carves an exception out of an earlier broad match.
//
// A contract with no patterns puts nothing in scope. That is intentional: an
// empty contract is a declaration the author did not finish, and treating it as
// "everything is allowed" would silently disable the check they asked for.
func (c Contract) InScope(relPath string) bool {
	in := false
	for _, p := range c.Patterns {
		if matchGlob(p.Glob, relPath) {
			in = !p.Negated
		}
	}
	return in
}

// matchGlob matches a slash-separated path against a glob supporting `*` (within
// one segment) and `**` (any number of segments, including zero).
//
// filepath.Match is not usable here: it has no `**`, and `internal/guard/**` —
// the single most obvious thing to write in a contract — is exactly what it
// cannot express. A bare `dir` with no wildcard also matches everything beneath
// it, because "I'm working in this directory" is what a person means when they
// write a directory name.
func matchGlob(glob, p string) bool {
	if glob == p {
		return true
	}
	// A plain directory prefix means the whole subtree.
	if !strings.ContainsAny(glob, "*?[") {
		return strings.HasPrefix(p, strings.TrimSuffix(glob, "/")+"/")
	}
	return matchSegments(strings.Split(glob, "/"), strings.Split(p, "/"))
}

// matchSegments is the recursive `**`-aware matcher. `**` consumes zero or more
// path segments, so it branches: try consuming nothing, else consume one segment
// and retry.
func matchSegments(pat, seg []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			// A TRAILING `**` means "the contents of", so it requires at least one
			// remaining segment: `a/**` covers `a/b.go` but not a file named `a`.
			// A MEDIAL `**` still matches zero segments, so `a/**/c.go` covers
			// `a/c.go` — the two cases differ and conflating them makes `dir/**`
			// silently match the directory's own path.
			if len(pat) == 1 {
				return len(seg) > 0
			}
			for i := 0; i <= len(seg); i++ {
				if matchSegments(pat[1:], seg[i:]) {
					return true
				}
			}
			return false
		}
		if len(seg) == 0 {
			return false
		}
		ok, err := path.Match(pat[0], seg[0])
		if err != nil || !ok {
			return false
		}
		pat, seg = pat[1:], seg[1:]
	}
	return len(seg) == 0
}

// OutOfScope filters paths down to those the contract does not allow, preserving
// input order.
func (c Contract) OutOfScope(paths []string) []string {
	var out []string
	for _, p := range paths {
		if !c.InScope(p) {
			out = append(out, p)
		}
	}
	return out
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
