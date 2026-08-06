package guardstats

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// GitOracle answers the audit's dated existence questions by matching
// definition patterns against a commit, using `git grep` — no checkout, no
// working-tree mutation, and nothing written anywhere.
//
// Honest scope, because the audit's numbers are only as good as this: these are
// regular expressions over source text, not a parser. They are chosen to be
// INDEPENDENT of the guard's own extractors rather than better than them — the
// audit is a differential, and a second implementation that agrees by sharing
// code proves nothing. Two consequences follow and neither is hidden:
//
//   - A missed definition reads as VerdictStands, i.e. it flatters the guard.
//     That is the safer direction for a metric whose purpose is to find guard
//     bugs, so the patterns lean permissive.
//   - A match inside a string literal or comment reads as VerdictFP, i.e. it
//     indicts the guard falsely. The patterns are anchored to declaration
//     keywords at line start for exactly this reason; a bare "the symbol
//     appears somewhere" test would be useless here.
//
// A parser-grade oracle (running runecho's own ir generator over the blob at a
// commit) is the intended upgrade. Audit takes an Oracle interface so that swap
// costs nothing here.
type GitOracle struct {
	// Timeout bounds each git invocation. Zero means DefaultGitTimeout.
	Timeout time.Duration
}

// DefaultGitTimeout bounds one git call. `git grep` against a large history is
// the slow case; the audit runs offline, so this is generous rather than tight.
const DefaultGitTimeout = 30 * time.Second

func (g GitOracle) timeout() time.Duration {
	if g.Timeout <= 0 {
		return DefaultGitTimeout
	}
	return g.Timeout
}

// identRe bounds what may be interpolated into a regex handed to git. Symbol
// names arrive from decisions.jsonl, which records names parsed out of repo
// FILES — attacker-influenced text in any repo that is not the user's own (the
// same reasoning that made repo paths untrusted in the #212 red-team pass).
// Anything that is not a plain identifier is refused rather than escaped: the
// guard has no legitimate reason to flag one, so refusing costs nothing real and
// removes the whole class of "did I escape this correctly for git's ERE dialect"
// question.
var identRe = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)

// ErrUnsafeSymbol is returned for a flagged name that is not a plain identifier.
var ErrUnsafeSymbol = errors.New("symbol is not a plain identifier")

// langPathspec limits `git grep` to files of the decision's language. Narrower
// is better here: a Python symbol "found" in a vendored JS bundle would flip a
// correct catch to a false VerdictFP. An unknown language searches everything,
// which is the permissive direction described on GitOracle.
var langPathspec = map[string][]string{
	"go": {"*.go"},
	"py": {"*.py", "*.pyi"},
	"js": {"*.js", "*.jsx", "*.ts", "*.tsx", "*.mjs", "*.cjs", "*.gs"},
	"sh": {"*.sh", "*.bash"},
}

// The patterns come in two scopes, and conflating them is the single biggest
// error this oracle can make.
//
// A DECLARATION (`def f`, `class C`, `function f`, Go's `func F`) creates a name
// other files can import. Asking for it repo-wide is correct.
//
// A BINDING (`x = ...`, `from m import x`, a member inside a multi-line
// `import { … }` block) creates a name that exists only in the file that wrote
// it. Asking for those repo-wide is not a weaker test, it is a different and
// wrong one: `path = ...` or `from pathlib import Path` occurs in nearly every
// Python repo, so a repo-wide binding search reports "already defined" for
// essentially any common local name and manufactures false-positive verdicts
// out of thin air. The first run of this audit did exactly that — it scored
// `fetch`, `Path`, `escape`, `port`, `name`, `path` and `payloads` as guard
// resolution bugs, which is how the scope split got found.
//
// So: declPatterns are searched across the tree, bindPatterns only inside the
// one file the ask was about. That mirrors how the guard resolves — repo index
// plus the edited file's own definitions — without sharing any of its code.
//
// `^\s*` (not `^`) throughout: a method inside a class, a function inside a
// closure, and a conditionally-defined helper are all real definitions of the
// name for resolution purposes.
func declPatterns(lang, sym string) []string {
	q := regexp.QuoteMeta(sym)
	switch lang {
	case "go":
		return []string{
			// func Name, func (r T) Name — the receiver group is optional.
			`^func([[:space:]]*\([^)]*\))?[[:space:]]+` + q + `\b`,
			// type/var/const Name at top level.
			//
			// A name inside a grouped `var ( … )` block is deliberately NOT
			// covered. The pattern that tried to (`^\t` + name) never matched
			// anything — git's ERE does not honour `\t` as a tab — and the obvious
			// repair, `^[[:space:]]+` + name, matches every STRUCT FIELD too, which
			// is not a package-scope declaration of that name. Given the choice
			// between missing a grouped var (reads as VerdictStands, flatters the
			// guard) and crediting a struct field (reads as VerdictFP, blames the
			// guard for a bug it does not have), the file's stated bias says take
			// the miss. Stated here rather than left as a dead pattern.
			`^[[:space:]]*(type|var|const)[[:space:]]+` + q + `\b`,
		}
	case "py":
		return []string{
			`^[[:space:]]*(async[[:space:]]+)?def[[:space:]]+` + q + `\b`,
			`^[[:space:]]*class[[:space:]]+` + q + `\b`,
			// A module-level constant is importable, so it is a declaration.
			// Anchored at column 0 to exclude locals inside a function body.
			`^` + q + `[[:space:]]*(:[^=]*)?=`,
		}
	case "js":
		return []string{
			`^[[:space:]]*(export[[:space:]]+)?(default[[:space:]]+)?(async[[:space:]]+)?function[[:space:]]*\*?[[:space:]]*` + q + `\b`,
			`^[[:space:]]*(export[[:space:]]+)?(abstract[[:space:]]+)?class[[:space:]]+` + q + `\b`,
			`^[[:space:]]*(export[[:space:]]+)?(declare[[:space:]]+)?(type|interface|enum|namespace)[[:space:]]+` + q + `\b`,
			// A top-level const/let/var is module scope and importable when
			// exported; column-0 anchored for the same reason as Python.
			`^(export[[:space:]]+)?(declare[[:space:]]+)?(const|let|var)[[:space:]]+` + q + `\b`,
		}
	case "sh":
		return []string{
			`^[[:space:]]*(function[[:space:]]+)?` + q + `[[:space:]]*\(\)`,
		}
	default:
		// Unknown language: one declaration-ish pattern. Permissive by design —
		// see the GitOracle doc.
		return []string{
			`^[[:space:]]*[A-Za-z_$@#]*[[:space:]]*\b` + q + `\b[[:space:]]*[(={:]`,
		}
	}
}

// bindPatterns are only ever searched inside the edited file. See declPatterns.
//
// inImportBlock gates the "bare name alone on a line" forms. Those exist to see
// a member of a multi-line `import { … }` / `from x import ( … )` block — the
// exact shape the guard's own ExtractImports is blind to, so the oracle must
// cover it or it would score that guard bug as a correct catch. But the pattern
// cannot tell a multi-line IMPORT member from a multi-line CALL ARGUMENT or list
// element, which are USAGES:
//
//	result = compute(          const arr = [
//	    payload,                 foo,
//	)                          ];
//
// Ungated, `payload` and `foo` read as "already defined at ask time" and the
// guard is blamed for a resolution bug it does not have. Verified by execution:
// 7 of 7 such usages scored defined. Measured blast radius on the reference
// window was 4 of 69 fp verdicts, so gating on "this file opens a multi-line
// import at all" removes the class for every file that has none — which is most
// of them — at the cost of one extra grep.
//
// Residual, stated rather than hidden: a file containing BOTH a multi-line
// import and a multi-line call can still over-match. Closing that needs
// multi-line context git grep cannot express; a parser-grade oracle is where it
// gets fixed properly.
func bindPatterns(lang, sym string, inImportBlock bool) []string {
	q := regexp.QuoteMeta(sym)
	switch lang {
	case "go":
		return []string{
			`^[[:space:]]*` + q + `[[:space:]]*:?=`,
			// Import alias: `alias "path/to/pkg"`.
			`^[[:space:]]*` + q + `[[:space:]]+"`,
		}
	case "py":
		pats := []string{
			`^[[:space:]]*` + q + `[[:space:]]*(:[^=]*)?=`,
			`^[[:space:]]*(from[[:space:]].*)?import[[:space:]].*\b` + q + `\b`,
			// A parameter, which binds the name for the whole function body.
			`^[[:space:]]*(async[[:space:]]+)?def[[:space:]].*[(,][[:space:]]*\*{0,2}` + q + `[[:space:]]*[:,)=]`,
		}
		if inImportBlock {
			// A name on its own line inside a parenthesised multi-line import.
			pats = append(pats, `^[[:space:]]*`+q+`([[:space:]]+as[[:space:]]+[A-Za-z_][A-Za-z0-9_]*)?[[:space:]]*,?[[:space:]]*$`)
		}
		return pats
	case "js":
		pats := []string{
			`^[[:space:]]*(const|let|var)[[:space:]].*\b` + q + `\b`,
			// Class/object member shorthand: `name(args) {`, `name = () =>`.
			`^[[:space:]]*(static[[:space:]]+)?(async[[:space:]]+)?` + q + `[[:space:]]*[(=]`,
			`\bimport[[:space:]].*\b` + q + `\b`,
			`\bfunction[[:space:]].*[(,][[:space:]]*` + q + `[[:space:]]*[:,)=]`,
		}
		if inImportBlock {
			// A member on its own line inside a multi-line `import { … }` block —
			// the exact form the guard's own ExtractImports is blind to.
			pats = append(pats, `^[[:space:]]*`+q+`([[:space:]]+as[[:space:]]+[A-Za-z_$][A-Za-z0-9_$]*)?[[:space:]]*,?[[:space:]]*$`)
		}
		return pats
	case "sh":
		return []string{
			`^[[:space:]]*(export[[:space:]]+|declare[[:space:]]+|local[[:space:]]+)?` + q + `=`,
		}
	default:
		return nil
	}
}

func (g GitOracle) git(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout())
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	// A repo-local or global hook/config must not run against an audit. This is
	// read-only tooling pointed at trees the user did not necessarily write.
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	return string(out), err
}

// Worktree resolves a recorded absolute file path to a usable git worktree.
//
// The recorded path is frequently gone. The claudew/codexw flow creates one
// worktree per session and deletes it on exit, so a decision written three weeks
// ago routinely names a directory that no longer exists — the founding example,
// frostline's claude-20260718-183238, is already deleted. Refusing those would
// throw away most of the window, so this walks up to the nearest surviving
// ancestor and, if that ancestor is the bare-repo parent that holds the
// worktrees, adopts a sibling worktree of the same repository instead.
//
// A sibling is a sound substitute BECAUSE the questions asked of it are dated
// and repo-wide: "was this symbol defined anywhere at this commit" is answered
// from shared history, not from the vanished worktree's uncommitted state. What
// is lost is any change that was never committed, which biases toward calling a
// symbol undefined — the flattering direction, consistent with the rest.
// It returns the worktree root AND the file's repo-relative path, because the
// two are only separable here. A recorded path carries no marker for where the
// worktree root ended, and guessing from its trailing segments is wrong for any
// file at the repo root: there the parent segment IS the worktree directory name
// (`claude-20260718-183238/main.go`), which by construction does not exist in
// the sibling being substituted. Splitting at the surviving ancestor gives the
// exact boundary instead of approximating it.
func (g GitOracle) Worktree(file string) (root, rel string, err error) {
	if file == "" {
		return "", "", errors.New("decision record has no file path")
	}
	dir := filepath.Dir(file)
	for {
		if fi, statErr := os.Stat(dir); statErr == nil && fi.IsDir() {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", "", fmt.Errorf("no surviving ancestor of %s", file)
		}
		dir = parent
	}
	if out, gitErr := g.git(dir, "rev-parse", "--show-toplevel"); gitErr == nil {
		if r := strings.TrimSpace(out); r != "" {
			sub, relErr := filepath.Rel(r, file)
			if relErr != nil || strings.HasPrefix(sub, "..") {
				return "", "", fmt.Errorf("%s is not under its own worktree %s", file, r)
			}
			return r, filepath.ToSlash(sub), nil
		}
	}
	// Not inside a repo any more — look for a sibling worktree beneath the
	// surviving ancestor. Sorted so the choice is deterministic across runs
	// rather than dependent on directory order.
	//
	// EVERY candidate must be proved to be the right repository before it is
	// returned. Without that proof the walk above is a foot-gun: when a whole
	// repo directory is deleted (not just one worktree) the surviving ancestor
	// becomes ~/personal_projects, whose children are ~40 unrelated repos, and
	// the first one alphabetically would be adopted and then answer every dated
	// question about a project it has never seen. The failure is silent — a
	// plausible verdict computed against the wrong history — which is strictly
	// worse than reporting the ask unauditable. (CodeGraph 1.2.0 shipped exactly
	// this bug in its index-root resolution and it took two weeks to notice.)
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		return "", "", fmt.Errorf("worktree gone and %s unreadable: %w", dir, readErr)
	}
	// Where the repo-relative path starts inside the remainder is NOT knowable
	// from the path, so it is not guessed — both readings are offered and
	// knowsPath picks the one a candidate repo actually recognises.
	//
	// Two real layouts produce different answers. A vanished claudew worktree
	// leaves `<parent>/<worktree>/<rel>`, so the first component must be dropped.
	// But a repo MIGRATED from a plain clone to the bare-worktree layout leaves
	// its old path `<repo>/<rel>` intact — the directory still exists, it merely
	// stopped being a worktree — and there the whole remainder IS the relative
	// path. Assuming the first case silently refused 28 quarry asks that
	// `quarry/main` could answer perfectly well.
	remainder, relErr := filepath.Rel(dir, file)
	if relErr != nil {
		return "", "", fmt.Errorf("cannot relativise %s against %s: %w", file, dir, relErr)
	}
	parts := strings.Split(filepath.ToSlash(remainder), "/")
	candidates := []string{strings.Join(parts, "/")}
	if len(parts) > 1 {
		candidates = append(candidates, strings.Join(parts[1:], "/"))
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, n := range names {
		cand := filepath.Join(dir, n)
		out, gitErr := g.git(cand, "rev-parse", "--show-toplevel")
		if gitErr != nil {
			continue
		}
		r := strings.TrimSpace(out)
		if r == "" {
			continue
		}
		for _, c := range candidates {
			if g.knowsPath(r, c) {
				return r, c, nil
			}
		}
	}
	return "", "", fmt.Errorf("worktree gone, no repo under %s holds any of %v", dir, candidates)
}

// knowsPath reports whether rel has ever existed anywhere in worktree's history.
// This is the proof that a candidate sibling is the SAME repository the decision
// was recorded against, not merely a git repo that happens to sit next to it.
//
// History-wide (`rev-list --all`) rather than a working-tree check, because a
// file legitimately moves or is deleted between the ask and the audit; requiring
// it to exist right now would reject repos that are in fact correct. A file that
// never existed in this history at all is the case worth refusing.
func (g GitOracle) knowsPath(worktree, rel string) bool {
	if rel == "" {
		return false
	}
	out, err := g.git(worktree, "rev-list", "--all", "-1", "--", literalPathspec(rel))
	return err == nil && strings.TrimSpace(out) != ""
}

// literalPathspec disables git's wildcard interpretation for a path taken from a
// decision record. Without it a repo path containing `*`, `?` or `[` would be
// matched as a glob — the same "repo file paths are attacker text" reasoning
// that governs identRe above.
func literalPathspec(rel string) string { return ":(literal)" + rel }

// Head returns the current HEAD commit.
func (g GitOracle) Head(worktree string) (string, error) {
	out, err := g.git(worktree, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("rev-parse HEAD in %s: %w", worktree, err)
	}
	return strings.TrimSpace(out), nil
}

// RevAt returns the newest commit at or before ts.
//
// --before matches on COMMITTER date, which lags the working tree: code exists
// in a file for some minutes-to-hours before it is committed. So a symbol
// written just before the ask can still look absent here, which classifies a
// genuine false positive as premature. That bias is one-directional and is
// reported rather than corrected — correcting it would require reconstructing
// working-tree state that was never recorded.
func (g GitOracle) RevAt(worktree string, ts time.Time) (string, error) {
	out, err := g.git(worktree, "rev-list", "-1",
		"--before="+ts.UTC().Format(time.RFC3339), "HEAD")
	if err != nil {
		return "", fmt.Errorf("rev-list in %s: %w", worktree, err)
	}
	rev := strings.TrimSpace(out)
	if rev == "" {
		return "", fmt.Errorf("no commit at or before %s", ts.UTC().Format(time.RFC3339))
	}
	return rev, nil
}

// Defined reports whether sym was resolvable at rev: either declared anywhere in
// the tree, or bound inside the edited file itself. rel is that file's
// repo-relative path as returned by Worktree, which is what lets a sibling
// worktree answer for a vanished one.
func (g GitOracle) Defined(worktree, rev, lang, sym, rel string) (bool, error) {
	// A qualified name (`http.Gett`, `pkg.Helper`) reaches here from the
	// dep-qualified check. Resolving one properly means resolving the qualifier
	// to a package first, which this oracle does not do; searching the final
	// segment tree-wide is the permissive approximation, and permissive here
	// means "credits the guard with fewer FPs than it may deserve".
	needle := sym
	if i := strings.LastIndexByte(needle, '.'); i >= 0 {
		needle = needle[i+1:]
	}
	if !identRe.MatchString(needle) {
		return false, fmt.Errorf("%w: %q", ErrUnsafeSymbol, sym)
	}

	found, err := g.grep(worktree, rev, declPatterns(lang, needle), langPathspec[lang])
	if err != nil || found {
		return found, err
	}
	// Bindings only count inside the edited file.
	if rel != "" {
		inImport, err := g.opensMultilineImport(worktree, rev, lang, rel)
		if err != nil {
			return false, err
		}
		if pats := bindPatterns(lang, needle, inImport); len(pats) > 0 {
			return g.grep(worktree, rev, pats, []string{literalPathspec(rel)})
		}
	}
	return false, nil
}

// multilineImportOpener matches a line that opens a multi-line import block:
// `import {` / `from x import (` with nothing but whitespace after the bracket.
// A file with none of these cannot contain a multi-line import member, so the
// bare-name binding patterns are withheld from it entirely — see bindPatterns.
var multilineImportOpener = map[string]string{
	"py": `^[[:space:]]*(from[[:space:]].*)?import[[:space:]]*\([[:space:]]*$`,
	"js": `^[[:space:]]*import[[:space:]]*(type[[:space:]]+)?\{[[:space:]]*$`,
}

// opensMultilineImport reports whether rel contains a multi-line import opener
// at rev. Costs one grep, and only for languages that have the construct.
func (g GitOracle) opensMultilineImport(worktree, rev, lang, rel string) (bool, error) {
	pat, ok := multilineImportOpener[lang]
	if !ok {
		return false, nil
	}
	return g.grep(worktree, rev, []string{pat}, []string{literalPathspec(rel)})
}

// grep runs one `git grep` over rev, returning whether any pattern matched.
func (g GitOracle) grep(worktree, rev string, patterns, pathspecs []string) (bool, error) {
	if len(patterns) == 0 {
		return false, nil
	}
	args := []string{"grep", "--no-color", "-I", "-l", "-E"}
	for _, p := range patterns {
		args = append(args, "-e", p)
	}
	args = append(args, rev, "--")
	// Every pathspec is a literal glob built here, never taken from the record
	// as free text.
	args = append(args, pathspecs...)

	_, err := g.git(worktree, args...)
	if err == nil {
		return true, nil
	}
	// git grep exits 1 for "no match" — the expected negative, not a failure.
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("git grep in %s at %s: %w", worktree, rev, err)
}
