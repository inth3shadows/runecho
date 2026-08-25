package ir

import (
	"sort"
	"strings"
)

// Skip reasons recorded by a Generate/Update walk. Closed set: a consumer that
// switches on these must be able to enumerate them, so new reasons are added
// here and nowhere else.
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
	// SkipIgnoredDir — a directory matched IgnoredPaths and was pruned. The
	// DIRECTORY is recorded, never its contents: filepath.SkipDir means the walk
	// never descends, and enumerating it would defeat the pruning entirely. A
	// consumer asking "was my changed path examined?" must therefore test path
	// prefixes against these entries, not just equality.
	SkipIgnoredDir = "ignored_dir"
)

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

// skipRecorder accumulates skips during one walk, bounded by maxRecordedSkips.
// The zero value is ready to use, and a nil receiver is a no-op so a caller that
// does not care about skips can pass nil.
type skipRecorder struct {
	items     []SkippedFile
	truncated bool
}

func (r *skipRecorder) add(path, reason string) {
	if r == nil {
		return
	}
	if len(r.items) >= maxRecordedSkips {
		r.truncated = true
		return
	}
	r.items = append(r.items, SkippedFile{Path: path, Reason: reason})
}

// result returns the recorded skips in a deterministic order. filepath.Walk is
// already lexical, but the ordering is part of a machine contract whose bytes
// are compared, so it is asserted here rather than inherited from a walk
// implementation detail.
func (r *skipRecorder) result() ([]SkippedFile, bool) {
	if r == nil {
		return nil, false
	}
	out := r.items
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Reason < out[j].Reason
	})
	return out, r.truncated
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
// BIAS: conservative. A missing entry degrades to the previous behaviour (a
// silent skip, no worse than before). A wrong entry makes a consumer fail on a
// file that never needed indexing. Under-inclusion is therefore the safe error,
// and ambiguous extensions are left out on purpose — `.d` (D source, but also
// the make dependency files every C build litters a tree with) is the worked
// example.
//
// INVARIANT: no entry here may be supported by a registered parser. Enforced by
// TestKnownSourceExtensionsAreNotParsed — when a parser gains an extension, its
// entry must be deleted here, or the indexer will report files it did index as
// unexamined.
var knownSourceExtensions = map[string]bool{
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
	// Scripting. .zsh and .fish are here, not in the supported set: ShellParser
	// handles .sh and .bash only, so a function deleted from a .zsh file is
	// exactly the invisible removal this table exists to surface.
	".lua": true, ".pl": true, ".pm": true, ".tcl": true, ".awk": true,
	".ps1": true, ".psm1": true, ".zsh": true, ".fish": true, ".bat": true, ".cmd": true,
	// Scientific / numeric
	".r": true, ".jl": true, ".f": true, ".for": true, ".f90": true, ".f95": true, ".f03": true,
	// Declarative code — no functions, but named blocks whose removal is
	// structural drift a reader would call a change.
	".sql": true, ".proto": true, ".tf": true, ".sol": true,
	// Assembly
	".asm": true, ".s": true,
}

// IsKnownSourceExtension reports whether ext names a source language, matched
// case-insensitively so `.C`, `.R`, and `.S` are recognised. Callers must still
// check supportsExtension first: this answers "is it code?", not "did I index
// it?", and the two are independent questions.
func IsKnownSourceExtension(ext string) bool {
	return knownSourceExtensions[strings.ToLower(ext)]
}
