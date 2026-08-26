package ir

import (
	"path/filepath"
	"sort"
	"strings"
)

// Skip reasons recorded by a Generate/Update walk. Closed set: a consumer that
// switches on these must be able to enumerate them, so new reasons are added
// here and nowhere else.
//
// The set splits in two, and the split is the contract (see IsPolicySkip):
//
//   - CAPABILITY reasons name something the indexer wanted to read and could
//     not. They are the blind spots. A consumer fails closed on these.
//   - POLICY reasons name something the operator CONFIGURED away. They are
//     informational.
//
// The discriminator is "did the operator ask for this", NOT file-versus-
// directory: an unreadable or unfollowable DIRECTORY is a capability failure,
// because its whole subtree went unvisited and nothing recorded the files inside
// -- nothing ever saw them. Either array can therefore hold a file or a
// directory, and both are matched the same way; see IsPolicySkip.
//
// Conflating the two was the defect that shipped in the first draft: `.git` is
// in DefaultIgnoredPaths, so every repo produced a non-empty list, and a gate
// prefix-matching it blocked a `testdata/README.md` edit -- the exact
// false block the language table exists to prevent, arriving through the other
// reason code.
const (
	// SkipUnsupportedLanguage — the file is source code in a language no
	// registered parser handles. THE reason this type exists: such a file is
	// absent from the IR, therefore absent from a diff, therefore a symbol
	// deleted from it is invisible at exit 0 with empty stderr.
	SkipUnsupportedLanguage = "unsupported_language"
	// SkipParseError — a supported extension whose parser failed. Already
	// counted in Stats.ParseErrors; named here so the path is reportable.
	SkipParseError = "parse_error"
	// SkipOversized — over the generator's maxParseBytes ceiling.
	SkipOversized = "oversized"
	// SkipCapReached — FileCap was hit; the walk kept counting but stopped
	// parsing. The file is a supported language that simply never got read.
	SkipCapReached = "cap_reached"
	// SkipSymlink — a symlinked source FILE. The walk does not follow symlinks,
	// so the file is not indexed under this path. Recorded only for code: a
	// symlinked README is not a blind spot in a symbol index.
	SkipSymlink = "symlink"
	// SkipUnreadable — a FILE the walk could not stat or open.
	SkipUnreadable = "unreadable"

	// SkipIgnoredDir — a directory matched IgnoredPaths and was pruned. The
	// DIRECTORY is recorded, never its contents: filepath.SkipDir means the walk
	// never descends, and enumerating it would defeat the pruning entirely.
	SkipIgnoredDir = "ignored_dir"
	// SkipSymlinkDir — a symlinked directory. Not followed (cycle and
	// escape safety), so its whole subtree is unexamined under this path.
	SkipSymlinkDir = "symlink_dir"
	// SkipUnreadableDir — a directory the walk could not read. Its entire
	// subtree went unvisited, and unlike the reasons above nothing recorded the
	// individual files, because nothing ever saw them.
	SkipUnreadableDir = "unreadable_dir"
)

// policySkips are the reasons the operator CONFIGURED. There is exactly one, and
// the narrowness is the point.
//
// The first cut split on files-versus-directories, which put `unreadable_dir`
// and `symlink_dir` here. That was wrong, and dangerously so: a directory the
// walk could not read is a blind spot -- its whole subtree went unvisited and
// nothing recorded the individual files, because nothing ever saw them -- yet it
// landed in the array USAGE.md tells consumers to treat as informational. CI
// running without read permission on `src/legacy/` therefore produced
// `skipped: []` and exit 0 for a tree it never opened.
//
// The real discriminator is not "file or directory", it is "did the operator ask
// for this". An ignore rule is a decision they made. A permission error and an
// unfollowable link are limits of the tool.
var policySkips = map[string]bool{
	SkipIgnoredDir: true,
}

// IsPolicySkip reports whether reason names something the operator configured
// away (policy), as opposed to something the indexer could not read
// (capability).
//
// This is the discriminator a consumer needs, and the reason the diff payload
// carries two arrays instead of one. Fail closed on the capability array; treat
// the policy array as information, because a repo that ignores `testdata` still
// expects its documentation edits to pass.
//
// Capability entries may name a FILE or a DIRECTORY, so a consumer matches a
// changed path against an entry with:
//
//	changed == entry || strings.HasPrefix(changed, entry+"/")
//
// which is correct for both -- a file entry can never be a directory prefix of
// another path.
func IsPolicySkip(reason string) bool {
	return policySkips[reason]
}

// SkippedFile is one path the indexer declined, with the reason it declined.
//
// Field names are exported and untagged deliberately: the sibling arrays in the
// same JSON payload (`files` → FileDiff, `hot_files` → FileChurn) marshal with
// Go field names too, and a lone snake_case array inside a payload whose other
// arrays are Go-cased is a trap for the machine consumers this contract exists
// to serve.
type SkippedFile struct {
	Path   string
	Reason string
}

// maxRecordedSkips bounds the skip list. A monorepo of a language RunEcho does
// not parse would otherwise dominate a diff payload that exists to report symbol
// drift. Truncation is reported (Stats.SkippedTruncated), never silent — a
// consumer that fails closed on "did you look at my file?" must be able to tell
// "no entry, so it was indexed" from "no entry, because the list ran out".
const maxRecordedSkips = 1000

// MaxRecordedSkips exposes the cap so a consumer rendering a truncated list can
// name the number rather than hard-coding a copy that drifts.
const MaxRecordedSkips = maxRecordedSkips

// maxCapReachedSkips sub-bounds the cap_reached reason.
//
// Without it, cap_reached crowds out the reason the feature exists for. The
// walk is lexical and the recorder is first-come, so a 5000-file repo with
// FileCap=2000 emits ~3000 cap_reached adds; the list fills at 1000 and every
// .java file sorting after the fill point is dropped. The signal that survived
// was "lexically earliest", not "most important". cap_reached is also the most
// compressible reason -- it means "everything past the cap", and its count is
// already implied by SupportedSeen minus Indexed -- so a sample plus the
// truncation flag loses nothing a consumer can act on.
const maxCapReachedSkips = 100

// skipRecorder accumulates skips during one walk, bounded by maxRecordedSkips.
// The zero value is ready to use, and a nil receiver is a no-op so a caller that
// does not care about skips can pass nil.
type skipRecorder struct {
	items      []SkippedFile
	capReached int
	truncated  bool
	// cap the recorder actually hit, for a message that names the right number.
	// Two different caps can truncate; reporting MaxRecordedSkips when the
	// cap_reached sub-cap fired told an operator a 1000-entry list had been
	// exceeded next to a list of 101 entries, mis-scoping the blind spot.
	hitCap int
}

func (r *skipRecorder) add(path, reason string) {
	if r == nil {
		return
	}
	// "." is the walk root, and it is inert as a report: under the documented
	// match rule (equality, or a "<entry>/" prefix) it matches no repo-relative
	// path a consumer could hold, so recording it looks like a finding while
	// telling the consumer nothing. An unreadable or unstattable ROOT is a real
	// condition, but the honest signal for it is that the walk indexed nothing,
	// which SupportedSeen and Coverage already carry.
	if path == "." {
		return
	}
	if reason == SkipCapReached {
		if r.capReached >= maxCapReachedSkips {
			r.noteCap(maxCapReachedSkips)
			return
		}
		r.capReached++
	}
	if len(r.items) >= maxRecordedSkips {
		r.noteCap(maxRecordedSkips)
		return
	}
	r.items = append(r.items, SkippedFile{Path: path, Reason: reason})
}

// noteCap records that a cap truncated the list, keeping the LARGEST one seen.
//
// Two caps can fire in a single walk, and the message they feed names a number
// the operator uses to scope the blind spot. Last-write-wins let a cap_reached
// add arriving after the main cap filled reset the figure from 1000 back to 100
// -- printing "hit its cap (100)" beside a 1000-entry list, which is the same
// mis-scoping the sub-cap was introduced to fix, inverted. The largest cap is
// the honest headline: it is the one bounding the list the reader is looking at.
// TruncateSkips takes the MINIMUM for the same reason, since there the smaller
// per-surface budget is what bounds what the reader got.
func (r *skipRecorder) noteCap(cap int) {
	r.truncated = true
	if cap > r.hitCap {
		r.hitCap = cap
	}
}

// result returns the recorded skips in a deterministic order. filepath.Walk is
// already lexical, but the ordering is part of a machine contract whose bytes
// are compared, so it is asserted here rather than inherited from a walk
// implementation detail.
func (r *skipRecorder) result() ([]SkippedFile, bool, int) {
	if r == nil {
		return nil, false, 0
	}
	out := r.items
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Reason < out[j].Reason
	})
	return out, r.truncated, r.hitCap
}

// knownSourceExtensions lists extensions that ARE source code but that no
// registered parser handles.
//
// This table is the whole point of the feature, and its absence is what made the
// blind spot unreportable. `supportsExtension` answers one question — "do I have
// a parser?" — so `.java` and `.md` are the same answer: no. A consumer cannot
// fail closed on that, because failing on `.md` blocks every documentation
// change. Distinguishing "code I do not index" from "not code" is knowledge only
// the indexer can hold, and duplicating it in each consumer guarantees drift; it
// lives here, beside parser registration, for that reason.
//
// BIAS: conservative, WITH ONE EXCEPTION. A missing entry degrades to the
// previous behaviour (a silent skip); a wrong entry makes a consumer fail on a
// file that never needed indexing. Under-inclusion is therefore the safe error,
// and ambiguous extensions are left out on purpose — `.d` (D source, but also
// the make dependency files every C build litters a tree with) is the worked
// example. Two entries were removed after review found them matching non-code by
// the same accident `.d` would: `.hcl` (`.terraform.lock.hcl`) and `.ru`
// (`README.ru`). A table entry is only safe when its extension implies source
// for essentially every file that bears it.
//
// The exception, and it is not optional: an extension in a language family
// RunEcho ADVERTISES support for must never be missing. `.java` going unindexed
// is a capability limit a reader can reason about; `.mts` going unindexed in a
// tool that says it parses TypeScript reproduces the original bug in the one
// place nobody would look for it. TestAdvertisedLanguageFamiliesAreAccountedFor
// enforces this.
//
// INVARIANT: no entry here may be supported by a registered parser. Enforced by
// TestKnownSourceExtensionsAreNotParsed — when a parser gains an extension, its
// entry must be deleted here, or the indexer will report files it did index as
// unexamined.
var knownSourceExtensions = map[string]bool{
	// --- Family members of languages RunEcho advertises. Not optional; see the
	// "exception" paragraph above and TestAdvertisedLanguageFamiliesAreAccountedFor.
	// TypeScript: JSParser handles .ts/.tsx but not the ESM/CJS module variants.
	".mts": true, ".cts": true,
	// Python: stubs and Cython.
	".pyi": true, ".pyx": true, ".pxd": true,
	// Ruby: dialects the RubyParser does not claim. `.ru` is deliberately absent
	// -- its only common use is the single file `config.ru`, while `README.ru`,
	// `LICENSE.ru` are the <name>.<lang> translated-doc convention, and a
	// translated README in the fail-closed array blocks a docs-only change.
	".rake": true, ".gemspec": true,
	// Shell: ShellParser handles .sh/.bash only.
	".zsh": true, ".fish": true, ".ksh": true,

	// --- Languages with no parser at all.
	// C / C++
	".c": true, ".h": true, ".cc": true, ".cpp": true, ".cxx": true, ".c++": true,
	".hpp": true, ".hh": true, ".hxx": true, ".h++": true,
	// JVM
	".java": true, ".kt": true, ".kts": true, ".scala": true, ".groovy": true,
	".clj": true, ".cljs": true, ".cljc": true,
	// .NET
	".cs": true, ".vb": true, ".fs": true, ".fsi": true, ".fsx": true,
	// Apple
	".swift": true, ".m": true, ".mm": true,
	// Web / app
	".php": true, ".vue": true, ".svelte": true, ".elm": true, ".dart": true,
	// Systems
	".zig": true, ".nim": true, ".cr": true, ".v": true,
	// Functional
	".hs": true, ".ml": true, ".mli": true, ".ex": true, ".exs": true,
	".erl": true, ".hrl": true, ".rkt": true, ".scm": true, ".lisp": true, ".el": true,
	// Scripting
	".lua": true, ".pl": true, ".pm": true, ".tcl": true, ".awk": true,
	".ps1": true, ".psm1": true, ".bat": true, ".cmd": true,
	// Scientific / numeric
	".r": true, ".jl": true, ".f": true, ".for": true, ".f90": true, ".f95": true, ".f03": true,
	// Declarative code — no functions, but named blocks whose removal is
	// structural drift a reader would call a change.
	// `.hcl` is deliberately absent: filepath.Ext(".terraform.lock.hcl") is
	// ".hcl", so a generated lockfile -- rewritten on every provider bump --
	// would be reported as unexamined source and block any consumer that fails
	// closed. `.tf` already covers the hand-written case.
	".sql": true, ".proto": true, ".tf": true, ".sol": true,
	// Assembly
	".asm": true, ".s": true,
}

// looksLikeNonCodeFile reports whether path is recognisably a file that is not
// source: it carries an extension, and that extension is not code.
//
// It exists for the branches where the walk could not stat an entry and so does
// not know whether it was a file or a directory. Both mistakes there are real:
// dropping an unreadable DIRECTORY hides its whole subtree (nothing recorded the
// files inside, because nothing ever saw them), while recording an unreadable
// README puts non-code in the fail-closed array and blocks a docs-only change.
//
// The extension is the only evidence available. A path with a non-code extension
// is a file we do not care about; anything else -- no extension, or a code
// extension -- is recorded. Known failure mode: a DIRECTORY whose name happens
// to carry a non-code extension (`my.config/`) is dropped. That is rare, and it
// errs the same way the rest of the table does.
func (g *Generator) looksLikeNonCodeFile(path string) bool {
	return filepath.Ext(path) != "" && !g.isCode(path)
}

// IsKnownSourceExtension reports whether ext names a source language, matched
// case-insensitively so `.C`, `.R`, and `.S` are recognised. Callers must still
// check supportsExtension first: this answers "is it code?", not "did I index
// it?", and the two are independent questions.
func IsKnownSourceExtension(ext string) bool {
	return knownSourceExtensions[strings.ToLower(ext)]
}
