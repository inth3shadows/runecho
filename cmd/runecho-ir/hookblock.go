package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// runecho owns a marked block inside each git hook it installs, never the whole
// file (#443). The hooks live in the git common dir, shared with other tools'
// installers — kb-mcp's kb-drift fragment was silently deleted when a runecho
// install rewrote post-merge/post-checkout wholesale. The marker convention and
// its edge cases follow kb-mcp's deploy/git-hooks/install.sh.
const (
	hookOpenMarker  = "# >>> runecho >>>"
	hookCloseMarker = "# <<< runecho <<<"
	hookBakSuffix   = ".runecho.bak"
	newHookShebang  = "#!/usr/bin/env bash"
)

type hookAction string

const (
	hookCreated   hookAction = "created"   // block added to a new or foreign hook
	hookUpdated   hookAction = "updated"   // existing marked block replaced in place
	hookMigrated  hookAction = "migrated"  // legacy unmarked runecho lines converted
	hookUnchanged hookAction = "unchanged" // block already current; file not touched
	hookRefused   hookAction = "refused"   // could not merge safely; file not touched
)

// hookBlocks returns the marked block for each hook runecho installs. Every body
// is POSIX sh that never execs and never exits except to propagate the guard's
// failure: the block shares its file with other tools' content, so an `exec`
// (the old pre-commit) or an unconditional early `exit` (the old post-checkout
// gate) would kill whatever follows it.
//
// pre-commit: `||` exempts the guard from a foreign `set -e`, and `exit $?`
// hands git the guard's exact status; on success control falls through.
//
// Freshness is ADVISORY on post-merge/post-checkout: one offline line that
// executes nothing (#375). Rebuilding belongs to the periodic job and an
// explicit `version-check --reinstall` (freshen.go) — a checkout is not an act
// of trust. It stays folded into these hooks (#228) rather than a third
// installer that would collide with the reindex hooks.
func hookBlocks(irBin, guardBin string) map[string]string {
	ir := shellQuote(irBin)
	advise := ir + " version-check --quiet || true"
	reindex := ir + " repo reindex . >/dev/null 2>&1 &"
	return map[string]string{
		"pre-commit":  wrapHookBlock(shellQuote(guardBin) + ` "$@" || exit $?`),
		"post-commit": wrapHookBlock(reindex),
		"post-merge":  wrapHookBlock(advise + "\n" + reindex),
		// Only a branch switch ($3 == 1) acts, not a file checkout.
		"post-checkout": wrapHookBlock("if [ \"$3\" = \"1\" ]; then\n  " + advise + "\n  " + reindex + "\nfi"),
	}
}

func wrapHookBlock(body string) string {
	return hookOpenMarker + "\n" +
		"# Managed by `runecho-ir install`; edits between these markers are replaced.\n" +
		body + "\n" + hookCloseMarker + "\n"
}

// legacyHookLine matches every line a pre-#443 runecho ever wrote into a hook,
// with the binary quoted by %q (early releases) or shellQuote, at any path
// (`.exe` on Windows). The quoted path admits no quote other than shellQuote's
// own '\” escape, so a line a person wrapped (`flock '/l' '/x/runecho-ir' …`)
// does not match and is never silently rewritten. Frozen: every hook written since #443 carries markers.
var legacyHookLine = func() *regexp.Regexp {
	bin := `(?:'(?:[^']|'\\'')*runecho-(?:guard|ir)(?:\.exe)?'|"[^"]*runecho-(?:guard|ir)(?:\.exe)?")`
	return regexp.MustCompile(`^(?:` +
		`exec ` + bin + ` "\$@"` +
		`|` + bin + ` repo reindex \. >/dev/null 2>&1 &` +
		`|\[ "\$3" = "1" \] && ` + bin + ` repo reindex \. >/dev/null 2>&1 &` +
		`|\[ "\$3" = "1" \] \|\| exit 0` +
		`|` + bin + ` version-check (?:--reinstall )?--quiet \|\| true` +
		`)$`)
}()

var (
	looseHookMarker = regexp.MustCompile(`(>>>|<<<)\s*runecho`)
	shellNames      = map[string]bool{"sh": true, "bash": true, "dash": true, "ksh": true, "zsh": true}
)

// mergeHookBlock returns existing with runecho's block installed, touching no
// line outside the markers (or, on migration, outside the legacy runecho lines).
// An error means the file cannot be merged safely and must be left as it is.
// notes are advisories for the operator; they never change the result.
func mergeHookBlock(existing, block string, force bool) (out string, action hookAction, notes []string, err error) {
	if strings.TrimSpace(existing) == "" {
		return newHookShebang + "\n" + block, hookCreated, nil, nil
	}
	lines := strings.SplitAfter(existing, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	bare := make([]string, len(lines))
	for i, l := range lines {
		bare[i] = strings.TrimSuffix(l, "\n")
	}

	opens, closes, loose := []int{}, []int{}, []int{}
	for i, l := range bare {
		switch l {
		case hookOpenMarker:
			opens = append(opens, i)
		case hookCloseMarker:
			closes = append(closes, i)
		}
		if looseHookMarker.MatchString(l) {
			loose = append(loose, i+1)
		}
	}

	switch {
	case len(opens) == 1 && len(closes) == 1 && len(loose) == 2 && opens[0] < closes[0]:
		// Replace in place, so a block the operator moved stays where they put it.
		out = strings.Join(lines[:opens[0]], "") + block + strings.Join(lines[closes[0]+1:], "")
		if out == existing {
			return existing, hookUnchanged, nil, nil
		}
		return out, hookUpdated, nil, verifyHookMarkers(out)
	case len(loose) > 0:
		return "", hookRefused, nil, fmt.Errorf(
			"runecho markers it cannot safely replace (marker-shaped lines at %v; want exactly one %q above one %q, each on its own line with no indentation or CRLF)",
			loose, hookOpenMarker, hookCloseMarker)
	}

	// No markers. A shebang naming a non-shell interpreter cannot take a shell block.
	hasShebang := strings.HasPrefix(bare[0], "#!")
	if hasShebang {
		if interp := shebangInterpreter(bare[0]); !shellNames[interp] {
			return "", hookRefused, nil, fmt.Errorf("existing hook is a %q script, not a shell script", interp)
		}
	}

	// Legacy: a hook written by runecho before markers. The longest run of known
	// runecho lines right after the shebang becomes the block; everything after
	// it — e.g. a kb-drift fragment appended later — is kept byte for byte.
	if bare[0] == newHookShebang {
		end, invokes, widened := 1, false, ""
		for end < len(bare) && legacyHookLine.MatchString(bare[end]) {
			l := bare[end]
			if strings.Contains(l, "runecho-") {
				invokes = true
			}
			if strings.HasPrefix(l, "exec ") {
				widened = "never ran before (the old hook ended in `exec`); it now runs on every commit"
			} else if strings.HasSuffix(l, "|| exit 0") {
				widened = "ran only on branch switches before (the old `|| exit 0` gate); it now runs on every checkout"
			}
			end++
		}
		if invokes {
			// A runecho line left below foreign content would survive outside
			// the block, ungated and pinned to a stale path: refuse, don't guess.
			if n := runechoInvocationLine(bare[end:]); n > 0 && !force {
				return "", hookRefused, nil, fmt.Errorf(
					"line %d still mentions runecho below other content; remove any old runecho lines there and re-run (or use --force to migrate anyway)", end+n)
			}
			rest := strings.Join(lines[end:], "")
			if widened != "" && strings.TrimSpace(rest) != "" {
				notes = append(notes, "content below runecho's old lines "+widened)
			}
			out = lines[0] + block + rest
			return out, hookMigrated, notes, verifyHookMarkers(out)
		}
	}

	// Someone wired runecho in by hand: adding the block would run it twice.
	if n := runechoInvocationLine(bare); n > 0 && !force {
		return "", hookRefused, nil, fmt.Errorf(
			"line %d already invokes runecho outside runecho's markers; adding the block would run it twice (use --force to add it anyway)", n)
	}

	// Insert right after the shebang (or at the top), not at the end: a foreign
	// hook ending in `exit 0` or `exec` would otherwise skip the guard.
	if hasShebang {
		head := lines[0]
		if !strings.HasSuffix(head, "\n") {
			head += "\n"
		}
		out = head + block + strings.Join(lines[1:], "")
	} else {
		out = block + existing
	}
	return out, hookCreated, nil, verifyHookMarkers(out)
}

// runechoInvocationLine returns the 1-based index of the first non-comment
// line naming a runecho binary, or 0.
func runechoInvocationLine(lines []string) int {
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if !strings.HasPrefix(t, "#") && (strings.Contains(t, "runecho-guard") || strings.Contains(t, "runecho-ir")) {
			return i + 1
		}
	}
	return 0
}

// verifyHookMarkers re-checks a merge result: exactly one marker pair. The scan
// cannot parse shell (a marker inside a heredoc still counts), so this plus the
// .bak and the `bash -n` in installHookFile are the backstop.
func verifyHookMarkers(s string) error {
	opens, closes := 0, 0
	for _, l := range strings.Split(s, "\n") {
		switch l {
		case hookOpenMarker:
			opens++
		case hookCloseMarker:
			closes++
		}
	}
	if opens != 1 || closes != 1 {
		return fmt.Errorf("merge produced %d opening and %d closing runecho markers, want 1 and 1", opens, closes)
	}
	return nil
}

// shebangInterpreter returns the interpreter's base name, looking through env
// (and its flags) — "#!/usr/bin/env -S bash -e" → "bash".
func shebangInterpreter(line string) string {
	f := strings.Fields(strings.TrimPrefix(line, "#!"))
	if len(f) == 0 {
		return ""
	}
	name := filepath.Base(f[0])
	if name != "env" {
		return name
	}
	for _, a := range f[1:] {
		if !strings.HasPrefix(a, "-") && !strings.Contains(a, "=") {
			return filepath.Base(a)
		}
	}
	return ""
}

// installHookFile installs runecho's block into one hook. It refuses (leaving
// the file untouched) rather than guess when the merge is unsafe, and replaces
// the file by rename: hooks are shared by every worktree, bash reads a script
// incrementally, and rewriting a hook in place while another worktree runs it
// would hand that bash shifted bytes.
//
// A symlinked hook is refused: its target lives outside the hooks dir — often
// a tracked file in the repo or a dotfiles hook shared by many repos — and
// editing it would commit runecho's machine-local paths or wire the guard into
// every repo that links it.
//
// <hook>.runecho.bak is written only when content outside the markers is first
// touched (created in a foreign hook, or migrated), so a later in-block update
// never overwrites the original it exists to protect.
func installHookFile(hooksDir, name, block string, force bool) (hookAction, error) {
	path := filepath.Join(hooksDir, name)
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			target = "an unreadable target"
		}
		fmt.Fprintf(os.Stderr, "  Refusing %s: it is a symlink (to %s); runecho does not edit files outside the hooks dir. Add the block there yourself, or replace the link with a file. File left untouched.\n", name, printableSnippet(target))
		return hookRefused, nil
	}

	var existing []byte
	mode := os.FileMode(0755)
	info, err := os.Stat(path)
	switch {
	case err == nil:
		mode = info.Mode().Perm()
		if existing, err = os.ReadFile(path); err != nil {
			return "", fmt.Errorf("read %s hook: %w", name, err)
		}
	case !errors.Is(err, os.ErrNotExist):
		return "", fmt.Errorf("stat %s hook: %w", name, err)
	}

	out, action, notes, err := mergeHookBlock(string(existing), block, force)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  Refusing %s: %v. File left untouched.\n", name, err)
		return hookRefused, nil
	}
	for _, n := range notes {
		fmt.Fprintf(os.Stderr, "  Note (%s): %s\n", name, n)
	}
	if info != nil && mode&0111 == 0 {
		fmt.Fprintf(os.Stderr, "  Note (%s): the hook is not executable, so git will not run it; left as is.\n", name)
	}
	if action == hookUnchanged {
		fmt.Printf("  Unchanged %s\n", name)
		return action, nil
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".runecho-*")
	if err != nil {
		return "", fmt.Errorf("write %s hook: %w", name, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once renamed
	_, werr := tmp.WriteString(out)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmpPath, mode)
	}
	if werr != nil {
		return "", fmt.Errorf("write %s hook: %w", name, werr)
	}
	if err := bashSyntaxCheck(tmpPath, out); err != nil {
		fmt.Fprintf(os.Stderr, "  Refusing %s: the merged hook fails `bash -n` (%s). File left untouched.\n", name, printableSnippet(err.Error()))
		return hookRefused, nil
	}
	if existing != nil && action != hookUpdated {
		if err := os.WriteFile(path+hookBakSuffix, existing, mode&^0111); err != nil {
			return "", fmt.Errorf("back up %s hook: %w", name, err)
		}
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return "", fmt.Errorf("replace %s hook: %w", name, err)
	}
	fmt.Printf("  %s %s\n", strings.ToUpper(string(action[:1]))+string(action[1:]), name)
	return action, nil
}

// printableSnippet keeps an error about arbitrary file content safe to print
// on a shared terminal: one line, printable runes only (C0/C1 controls and
// invalid UTF-8 become '?'), capped at 200 runes.
func printableSnippet(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n == 200 {
			b.WriteString("…")
			break
		}
		if r == utf8.RuneError || !unicode.IsPrint(r) {
			r = '?'
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// bashSyntaxCheck runs `bash -n` on a merged hook whose interpreter bash can
// judge (bash, sh, dash, or none — git falls back to sh). Skipped when bash is
// absent: the check is a backstop, not a prerequisite.
func bashSyntaxCheck(path, content string) error {
	if first, _, _ := strings.Cut(content, "\n"); strings.HasPrefix(first, "#!") {
		switch shebangInterpreter(first) {
		case "bash", "sh", "dash":
		default:
			return nil
		}
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		return nil
	}
	if out, err := exec.Command(bash, "-n", path).CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
