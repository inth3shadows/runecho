package ir

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/inth3shadows/runecho/internal/guard"
	"github.com/inth3shadows/runecho/internal/parser"
	"golang.org/x/text/unicode/norm"
)

// DefaultGenerateTimeout bounds a single IR generation/update walk when the
// caller supplies neither a context deadline nor a GeneratorConfig override. It
// is a wall-clock ceiling on the whole walk (not per file), so a pathological
// repo or a stalled filesystem can no longer hang the indexer — or, critically,
// an MCP request that rebuilds a fresh IR on every call. Per insight-remediation
// assumption A-3, truncating a genuinely huge repo at the deadline is preferable
// to an unbounded hang; per-file cost is independently bounded by maxParseBytes.
const DefaultGenerateTimeout = 30 * time.Second

// Unbounded is the GeneratorConfig.GenerateTimeout value that disables the
// default walk deadline entirely (a caller-supplied ctx deadline still applies).
// Any negative duration has the same effect; this is the canonical name callers
// should use. For a one-shot CLI index of a legitimately huge or slow-FS repo
// where the default ceiling is too tight and a hang is acceptable.
const Unbounded = time.Duration(-1)

// withDeadline returns ctx unchanged (with a no-op cancel) when it already
// carries a deadline; otherwise a child bounded by the Generator's configured
// timeout, unless that timeout is the unbounded sentinel (no deadline applied).
// The returned cancel must always be called.
func (g *Generator) withDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	if g.genTimeout < 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, g.genTimeout)
}

// Generator creates and updates IR from source files.
type Generator struct {
	parsers      []parser.Parser
	ignoredPaths map[string]bool
	fileCap      int // 0 = unlimited; walk stops after this many files
	// maxParseBytes is the per-file parse size limit (see defaultMaxParseBytes).
	// A per-Generator field, not a package global, so a test that lowers it can
	// never race a parallel test.
	maxParseBytes int64
	// warn routes non-fatal walk/parse diagnostics. Defaults to stderr (set by
	// NewGenerator) so existing callers are unchanged; tests inject a sink to
	// assert the otherwise-silent skip branches actually fire.
	warn func(format string, args ...any)
	// genTimeout is the default wall-clock bound on a Generate/Update walk when the
	// caller passes no ctx deadline. NewGenerator resolves it: 0 → DefaultGenerateTimeout,
	// <0 → unbounded (the walk gets no default deadline). See withDeadline.
	genTimeout time.Duration
}

// GeneratorConfig configures IR generation behavior.
type GeneratorConfig struct {
	IgnoredPaths []string // Directory names to ignore
	FileCap      int      // Max files to index; 0 = unlimited. Walk stops after this many files are processed.
	// GenerateTimeout overrides the default wall-clock bound on a Generate/Update
	// walk (applied only when the caller passes no ctx deadline):
	// 0 → DefaultGenerateTimeout, >0 → that value, <0 → unbounded. The CLI maps
	// the RUNECHO_GENERATE_TIMEOUT env var onto this so a huge/slow-FS repo can
	// raise or disable the ceiling without a code change.
	GenerateTimeout time.Duration
}

// Stats reports honest-coverage counters from a Generate/Update walk.
type Stats struct {
	ParseErrors   int // supported files that failed to parse (not in the IR)
	SupportedSeen int // supported-extension files encountered, including beyond the cap
	Indexed       int // files in the IR (== len(IR.Files))
	// Skipped names the paths the walk declined and why. It is NOT the inverse of
	// Indexed: an unindexed language never reaches SupportedSeen, so it is
	// invisible to Coverage() by construction (the denominator counts supported
	// extensions only). That is the gap this field closes -- see skipped.go.
	Skipped []SkippedFile
	// SkippedTruncated reports that Skipped hit a cap. A consumer that fails
	// closed on "was this path examined?" must treat a truncated list as
	// "unknown", never as "not skipped".
	SkippedTruncated bool
	// SkippedCap is the cap that was hit, 0 when nothing truncated. Two caps can
	// fire (the overall list and the cap_reached sub-cap), and a message naming
	// the wrong one mis-scopes the blind spot for whoever reads it.
	SkippedCap int
	// RootUnreadable means the walk could not enter the repo root itself, so
	// NOTHING was examined. It needs its own flag: the root's repo-relative path
	// is ".", which matches nothing under the documented prefix rule, and with
	// SupportedSeen at 0 the coverage percentage is a vacuous 100. Without this,
	// every signal reads clean for a tree nothing opened.
	RootUnreadable bool
}

// Coverage returns Indexed as a percentage of SupportedSeen.
//
// CALLER CONTRACT: when SupportedSeen == 0 (a walk that saw no supported files)
// this returns a vacuous 100 — there is nothing to cover, so "fully covered" is
// the least-wrong scalar. The value is intentionally NOT changed (callers gate
// on it numerically), so callers that render coverage to users MUST first check
// SupportedSeen > 0 and suppress the figure otherwise — e.g. coverageSuffix in
// cmd/runecho-ir returns "" for the 0/0 case rather than printing "100%".
func (s Stats) Coverage() float64 {
	if s.SupportedSeen == 0 {
		return 100
	}
	return float64(s.Indexed) / float64(s.SupportedSeen) * 100
}

// capReached reports whether indexedCount has hit the configured file cap.
// indexedCount is the number of files already added to the IR — files that fail
// to parse never reach the IR, so they do not consume cap budget.
//
// Once the cap is reached the walk continues but only counts supported files
// (no stat/hash/parse work), so SupportedSeen stays an honest denominator for
// coverage instead of stopping at ~100% the moment the cap truncates.
func (g *Generator) capReached(indexedCount int) bool {
	return g.fileCap > 0 && indexedCount >= g.fileCap
}

// NewGenerator creates a new IR generator.
func NewGenerator(config GeneratorConfig) *Generator {
	paths := config.IgnoredPaths
	if len(paths) == 0 {
		paths = DefaultIgnoredPaths
	}
	ignored := make(map[string]bool, len(paths))
	for _, p := range paths {
		ignored[p] = true
	}
	// Resolve the timeout once: 0 means "unset" → the default ceiling; a negative
	// value is the explicit unbounded sentinel and is preserved as-is.
	genTimeout := config.GenerateTimeout
	if genTimeout == 0 {
		genTimeout = DefaultGenerateTimeout
	}
	return &Generator{
		parsers:       []parser.Parser{parser.NewJSParser(), parser.NewGoParser(), parser.NewPythonParser(), parser.NewShellParser(), parser.NewRustParser(), parser.NewRubyParser()},
		ignoredPaths:  ignored,
		fileCap:       config.FileCap,
		maxParseBytes: defaultMaxParseBytes,
		genTimeout:    genTimeout,
		warn: func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, format, args...)
		},
	}
}

// walkerFunc is called for each supported source file found during a walk.
// absRoot is the walk root; normalizedPath is the relative, normalized path.
// Returning an error from walkerFunc is propagated and stops the walk.
type walkerFunc func(absPath, normalizedPath string) error

// resolvedOrSame returns path with symlinks resolved, or path unchanged when it
// cannot be resolved (broken link, permission error, nonexistent path).
//
// Used on both sides of every root/file containment test. A repo reached through
// a link must resolve to the same string whichever way a caller names it, or the
// two disagree and the mismatch is silent.
func resolvedOrSame(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

// walkSourceFiles walks absRoot, calling fn for each supported source file.
// It skips ignored directories, symlinked directories, and unsupported extensions.
// The walk is checked for cancellation before each entry, so a done ctx
// (deadline or explicit cancel) aborts it between files and propagates ctx.Err()
// to the caller. Per-file granularity is sufficient: a single oversized file is
// already bounded by maxParseBytes.
//
// rec records the paths declined along the way; nil disables recording. Only the
// skips decided HERE are recorded (ignored directories, unindexed languages) --
// the ones decided inside fn (oversized, parse error, file cap) are recorded by
// fn, because only fn knows it took them.
func (g *Generator) walkSourceFiles(ctx context.Context, absRoot string, fn walkerFunc, rec *skipRecorder) error {
	// A symlinked ROOT is resolved before walking, not pruned.
	//
	// filepath.Walk lstats, so a root that is a symlink to a directory arrives at
	// the symlink branch, is recorded as pruned, and the walk ends after one
	// callback: zero files indexed, and the only trace is {"Path": "."} in the
	// INFORMATIONAL array -- a prefix matching no path a consumer could hold. A
	// repo reached through a link (`~/work -> /mnt/checkouts/x`, a pnpm workspace
	// link) was therefore a total, silent fail-open. The symlink rule exists to
	// prune links found INSIDE the tree; the directory the operator named is what
	// they asked to index, so follow it. Reporting stays relative to the resolved
	// root, which is what every path in the IR is already relative to.
	absRoot = resolvedOrSame(absRoot)
	// Skips are reported repo-relative, matching every other path in the IR and
	// in a diff payload. A path that cannot be made relative is not reported at
	// all rather than reported absolute: a consumer intersects these against its
	// own repo-relative paths, and an absolute entry would silently never match.
	relOf := func(path string) (string, error) {
		r, rerr := filepath.Rel(absRoot, path)
		if rerr != nil {
			return "", rerr
		}
		return normalizePath(r), nil
	}
	// record adds an entry, resolving the repo-relative path. A path that cannot
	// be made relative is not reported rather than reported absolute: a consumer
	// intersects these against its own repo-relative paths, and an absolute entry
	// would silently never match.
	record := func(path, reason string) {
		if rel, rerr := relOf(path); rerr == nil {
			rec.add(rel, reason)
		}
	}
	return filepath.Walk(absRoot, func(path string, info os.FileInfo, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if err != nil {
			g.warn("Warning: failed to access %s: %v\n", path, err)
			// Two different failures reach here, and they want opposite answers.
			//
			// ENOENT means the entry is GONE -- the ordinary race of a file
			// vanishing mid-walk during a git checkout or an editor's temp-file
			// churn. Nothing was hidden from us; there is nothing there, and
			// recording it puts a transient, self-healing false block in the
			// fail-closed array.
			//
			// Anything else (EACCES on a directory readdir could list but lstat
			// cannot enter, EIO, ELOOP) means something IS there and we could not
			// look at it. That is a real blind spot -- but it belongs to the
			// containing DIRECTORY, not to each child by name. Recording children
			// individually put `Makefile`, `LICENSE` and `Dockerfile` into the
			// fail-closed array, because an extension is not evidence for a path
			// that has none. The parent is the honest unit: it is what is
			// genuinely unenterable, one entry covers everything beneath it under
			// the documented prefix rule, and no non-code FILENAME ever enters.
			//
			// Which path to blame depends on which failure it was, and Walk tells
			// us via info. A non-nil info means the entry itself was statted and
			// the READDIR failed: the directory is the blind spot, record it. A
			// nil info means the LSTAT of a child failed, and the child's name is
			// no evidence of anything -- the containing directory is what could
			// not be entered.
			if !os.IsNotExist(err) {
				blame := path
				if info == nil {
					if parent := filepath.Dir(path); parent != path {
						blame = parent
					}
				}
				// An unreadable ROOT resolves to ".", which the recorder drops as
				// inert -- it matches no repo-relative path a consumer holds. But
				// dropping it silently is the worst outcome available: nothing was
				// indexed, `skipped` is empty, and Coverage() returns a VACUOUS
				// 100 because SupportedSeen is 0 too, so every signal reads clean
				// for a tree nothing opened. It needs its own flag, because there
				// is no path string that can express "all of it".
				if rel, rerr := relOf(blame); rerr == nil && rel == "." {
					rec.rootUnreadable = true
				} else {
					record(blame, SkipUnreadableDir)
				}
			}
			return nil
		}
		// A symlink whose NAME is in the ignore list is a configured ignore first
		// and a symlink second. pnpm and yarn workspaces routinely make
		// node_modules a link, and classifying that as a symlink put it in the
		// capability array -- a permanent, unfixable entry in the array a gate
		// fails closed on, for a directory the operator explicitly excluded. The
		// ignore rule is the operator's decision either way; how the directory
		// happens to be stored is not their problem.
		// Directories and symlinks only. Moving this check above the symlink
		// branch (so an ignored directory stored AS a link is still an ignore)
		// also put it ahead of the IsDir test, which made it fire for regular
		// FILES: a plain file named `dist` or `vendor` was recorded as
		// `ignored_dir`, a reason that is simply untrue for a file, and it
		// consumed recorder budget. None of the twelve default ignore names
		// carries an extension, so no source file could be caught -- but a `.git`
		// FILE (git worktrees and submodules use one) hit it on every walk.
		if path != absRoot && (info.IsDir() || info.Mode()&os.ModeSymlink != 0) &&
			g.ignoredPaths[filepath.Base(path)] {
			record(path, SkipIgnoredDir)
			if info.IsDir() {
				return filepath.SkipDir
			}
			// A symlink is not a directory to Walk, so SkipDir here would skip
			// the remaining siblings in the containing directory. Walk does not
			// descend through a link anyway.
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			// filepath.Walk lstats, so info.IsDir() is ALWAYS false here even when
			// the link points at a directory -- the mode bits describe the link,
			// not its target. The pre-existing `if info.IsDir() { return
			// filepath.SkipDir }` in this branch was therefore unreachable, and
			// reachable would have been a bug: Walk documents that SkipDir from a
			// non-directory entry skips the remaining files in the CONTAINING
			// directory. Resolve the target explicitly instead, and always return
			// nil -- Walk does not descend through a symlink regardless.
			target, serr := os.Stat(path)
			switch {
			case serr != nil:
				// The link did not resolve. "Did not resolve" is NOT "resolves to
				// nothing": os.Stat fails with EACCES, EPERM, EIO and ELOOP as
				// well as ENOENT, and in those cases the target is a real
				// directory full of real source that the walk simply cannot
				// enter. An earlier version reasoned "it is not a directory" and
				// applied the code filter -- which drops every such link, because
				// a directory link has no extension -- so a `legacy ->
				// $unreadable/legacy` produced `skipped: []` and a passing gate
				// for a subtree nothing had opened. That is the exact failure
				// policySkips was narrowed to close, reopened for symlinks.
				//
				// Since we cannot tell, take the same reading as the nil-info
				// branch above: record it, as a prefix, unless the extension
				// says it is plainly a non-code file.
				if !g.looksLikeNonCodeFile(path) {
					record(path, SkipUnreadableDir)
				}
			case target.IsDir():
				record(path, SkipSymlinkDir)
			default:
				// Only code: a symlinked README is not a blind spot in a symbol
				// index, and this list is fail-closed on.
				if g.isCode(path) {
					record(path, SkipSymlink)
				}
			}
			return nil
		}
		if info.IsDir() {
			// The ignore check happened above, before the symlink branch, so that
			// an ignored directory stored AS a link is still treated as an
			// ignore. The walk root is exempt from it: the rule exists to prune
			// subdirectories, and applying it to the directory the operator
			// explicitly pointed at pruned the entire walk, indexed zero files,
			// and -- because SupportedSeen was then 0 -- reported a vacuous 100%
			// coverage.
			return nil
		}
		ext := filepath.Ext(path)
		if !g.supportsExtension(ext) {
			// Only source code is reported. A README or a PNG is not a blind spot
			// in the symbol index, and reporting it would make a consumer that
			// fails closed on this list block every documentation change -- the
			// exact false-block that kept this reporting out of the payload
			// until the language table existed.
			if IsKnownSourceExtension(ext) {
				record(path, SkipUnsupportedLanguage)
			}
			return nil
		}
		relPath, rerr := relOf(path)
		if rerr != nil {
			g.warn("Warning: failed to compute relative path for %s: %v\n", path, rerr)
			return nil
		}
		return fn(path, relPath)
	})
}

// Generate creates IR for all supported files in the given root directory.
// When FileCap > 0, indexing stops after that many files; the walk continues
// counting supported files so Stats reports honest coverage.
//
// It is the context-free entry point and applies DefaultGenerateTimeout. Use
// GenerateCtx to supply a caller deadline (e.g. a per-request MCP budget).
func (g *Generator) Generate(rootPath string) (*IR, Stats, error) {
	return g.GenerateCtx(context.Background(), rootPath)
}

// GenerateCtx is Generate with an explicit context. If ctx carries no deadline,
// DefaultGenerateTimeout is applied so generation is always bounded. When the
// context is cancelled or its deadline passes, the walk stops between files, the
// partial result is discarded, and the (wrapped) ctx error is returned.
func (g *Generator) GenerateCtx(ctx context.Context, rootPath string) (*IR, Stats, error) {
	ctx, cancel := g.withDeadline(ctx)
	defer cancel()

	absRoot, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, Stats{}, fmt.Errorf("failed to resolve absolute path: %w", err)
	}
	absRoot = filepath.Clean(absRoot)

	result := &IR{Version: IRVersion, Files: make(map[string]FileIR)}
	var stats Stats
	rec := &skipRecorder{}

	if err := g.walkSourceFiles(ctx, absRoot, func(absPath, normPath string) error {
		stats.SupportedSeen++
		if g.capReached(len(result.Files)) {
			rec.add(normPath, SkipCapReached)
			return nil // count only; cap bounds parse work, not the denominator
		}
		fileIR, err := g.parseFile(absPath)
		if err != nil {
			g.warn("Warning: failed to parse %s: %v\n", absPath, err)
			stats.ParseErrors++
			rec.add(normPath, g.parseFailureReason(absPath))
			return nil
		}
		result.Files[normPath] = fileIR
		return nil
	}, rec); err != nil {
		return nil, Stats{}, fmt.Errorf("failed to walk directory: %w", err)
	}

	result.RootHash = ComputeRootHash(result.Files)
	stats.Indexed = len(result.Files)
	stats.Skipped, stats.SkippedTruncated, stats.SkippedCap = rec.result()
	stats.RootUnreadable = rec.rootUnreadable
	return result, stats, nil
}

// Update incrementally updates IR based on file hashes.
// Only re-parses files whose hash has changed. When FileCap > 0, indexing stops
// after that many files (consistent with Generate); the walk continues counting
// supported files so Stats reports honest coverage.
//
// A version-mismatched IR falls back to a full Generate: Update reuses entries
// for unchanged files verbatim, which would leave fields added by newer format
// versions (e.g. v2 refs) empty forever. Guarding here — not just at call
// sites — means no caller can perpetuate a stale format by mistake.
func (g *Generator) Update(existingIR *IR, rootPath string) (*IR, Stats, error) {
	return g.UpdateCtx(context.Background(), existingIR, rootPath)
}

// UpdateCtx is Update with an explicit context, bounded the same way as
// GenerateCtx (DefaultGenerateTimeout when ctx has no deadline). The
// version-mismatch fallback forwards ctx to GenerateCtx so the bound holds on
// either path.
func (g *Generator) UpdateCtx(ctx context.Context, existingIR *IR, rootPath string) (*IR, Stats, error) {
	if existingIR == nil || existingIR.Version != IRVersion {
		return g.GenerateCtx(ctx, rootPath)
	}
	ctx, cancel := g.withDeadline(ctx)
	defer cancel()

	absRoot, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, Stats{}, fmt.Errorf("failed to resolve absolute path: %w", err)
	}
	absRoot = filepath.Clean(absRoot)

	updated := &IR{Version: IRVersion, Files: make(map[string]FileIR)}
	var stats Stats
	rec := &skipRecorder{}

	if err := g.walkSourceFiles(ctx, absRoot, func(absPath, normPath string) error {
		stats.SupportedSeen++
		if g.capReached(len(updated.Files)) {
			rec.add(normPath, SkipCapReached)
			return nil // count only; cap bounds parse work, not the denominator
		}
		// Guard size before hashing: HashFile streams the whole file through
		// SHA-256, and parseFile rejects anything over maxParseBytes anyway, so
		// without this an oversized file is fully read on every Update only to be
		// rejected at parse. Generate guards inside parseFile; mirror it here.
		// A stat error falls through to HashFile, which surfaces it as before.
		if info, serr := os.Stat(absPath); serr == nil && info.Size() > g.maxParseBytes {
			g.warn("Warning: failed to parse %s: skipping oversized file (%d bytes)\n", absPath, info.Size())
			stats.ParseErrors++
			rec.add(normPath, SkipOversized)
			return nil
		}
		currentHash, err := HashFile(absPath)
		if err != nil {
			g.warn("Warning: failed to hash %s: %v\n", absPath, err)
			// Unhashable means unreadable means not in the updated IR. It is not
			// counted in ParseErrors (it never reached the parser) but it is
			// still a file the walk declined, and a consumer asking "did you
			// look at this?" is owed the same "no" as any other skip.
			rec.add(normPath, SkipParseError)
			return nil
		}
		if existing, ok := existingIR.Files[normPath]; ok && existing.Hash == currentHash {
			updated.Files[normPath] = existing
			return nil
		}
		fileIR, err := g.parseFile(absPath)
		if err != nil {
			g.warn("Warning: failed to parse %s: %v\n", absPath, err)
			stats.ParseErrors++
			rec.add(normPath, g.parseFailureReason(absPath))
			return nil
		}
		updated.Files[normPath] = fileIR
		return nil
	}, rec); err != nil {
		return nil, Stats{}, fmt.Errorf("failed to walk directory: %w", err)
	}

	updated.RootHash = ComputeRootHash(updated.Files)
	stats.Indexed = len(updated.Files)
	stats.Skipped, stats.SkippedTruncated, stats.SkippedCap = rec.result()
	stats.RootUnreadable = rec.rootUnreadable
	return updated, stats, nil
}

// parseFailureReason classifies a parseFile failure for the skip list.
//
// Generate has no separate size guard -- parseFile itself rejects an oversized
// file -- so on that path an oversized file and a syntactically broken one
// arrive at the same error branch. Re-stat rather than string-match the error:
// the message is a human diagnostic that is free to change, while the size is
// the fact the reason is asserting. A stat failure here means the file went
// away or became unreadable mid-walk; SkipParseError is the honest answer then,
// since all the caller is entitled to conclude is "not indexed".
func (g *Generator) parseFailureReason(absPath string) string {
	if info, err := os.Stat(absPath); err == nil && info.Size() > g.maxParseBytes {
		return SkipOversized
	}
	return SkipParseError
}

// UpdateFile refreshes a single file's entry in an existing IR and returns the
// new IR plus whether anything changed. It reparses just filePath (added or
// modified), drops it (deleted or no longer a supported source file), and leaves
// every other entry untouched — O(1) in repo size, for the per-edit auto-fresh
// hook where walking the whole tree on every keystroke would be wasteful.
//
// It is conservative: any condition it can't handle cleanly (file outside the
// repo, stat/parse error, IR version mismatch) returns the IR unchanged with
// changed=false, so the caller simply skips the refresh rather than corrupting
// state. RootHash is recomputed; changed is RootHash != existing.RootHash.
func (g *Generator) UpdateFile(existing *IR, rootPath, filePath string) (*IR, bool, error) {
	if existing == nil || existing.Version != IRVersion {
		return existing, false, nil
	}
	absRoot, err := filepath.Abs(rootPath)
	if err != nil {
		return existing, false, nil
	}
	absRoot = filepath.Clean(absRoot)
	absFile, err := filepath.Abs(filePath)
	if err != nil {
		return existing, false, nil
	}
	// Both sides resolved through symlinks before the containment test, the same
	// way walkSourceFiles resolves its root.
	//
	// Without this, a repo reached through a link indexes fine on a full walk and
	// then never refreshes incrementally: filepath.Rel("/home/u/work",
	// "/mnt/checkouts/x/main.go") starts with "..", so the edit is judged
	// "outside this repo" and the IR silently goes stale. It fails in both
	// directions -- root given as the link and file as the real path, or the
	// reverse -- because only one of the two is ever resolved by the caller.
	// Resolution failure falls through to the unresolved paths, which is the
	// previous behaviour.
	absRoot = resolvedOrSame(absRoot)
	absFile = resolvedOrSame(absFile)
	rel, err := filepath.Rel(absRoot, absFile)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return existing, false, nil // edited file is outside this repo
	}
	norm := normalizePath(rel)

	// Copy the map so the returned IR is independent of existing (callers may
	// keep using existing if changed=false).
	files := make(map[string]FileIR, len(existing.Files))
	for k, v := range existing.Files {
		files[k] = v
	}

	info, statErr := os.Stat(absFile)
	switch {
	case statErr != nil:
		if !os.IsNotExist(statErr) {
			return existing, false, nil // transient stat error — leave IR alone
		}
		if _, ok := files[norm]; !ok {
			return existing, false, nil // already absent
		}
		delete(files, norm) // file was deleted
	case info.IsDir() || !g.supportsExtension(filepath.Ext(absFile)) || pathCrossesSymlink(absRoot, absFile):
		// Not an indexed source file. A symlink — the edited target itself or any
		// directory component within the repo — mirrors walkSourceFiles, which skips
		// symlinked files and dirs: without this the per-edit refresh would os.Stat
		// through the link and pull an out-of-repo target's content into the IR under
		// an in-repo key, while a full walk skipped it (#143). If a real file at this
		// key used to be indexed (extension changed, or a file replaced by a symlink),
		// drop the stale entry; otherwise no-op.
		if _, ok := files[norm]; !ok {
			return existing, false, nil
		}
		delete(files, norm)
	default:
		fileIR, perr := g.parseFile(absFile)
		if perr != nil {
			return existing, false, nil // parse failed — keep the prior entry
		}
		files[norm] = fileIR
	}

	updated := &IR{Version: IRVersion, Files: files}
	updated.RootHash = ComputeRootHash(files)
	return updated, updated.RootHash != existing.RootHash, nil
}

// pathCrossesSymlink reports whether absFile, or any directory component strictly
// between absRoot and absFile, is a symlink. It mirrors walkSourceFiles, which skips
// symlinked files (return nil) and symlinked directories (SkipDir), so the per-edit
// UpdateFile refresh skips exactly the paths a full walk would — an in-repo symlink
// pointing OUTSIDE the repo can then never pull an external target into the IR (#143).
//
// absRoot's own ancestry is intentionally not inspected: filepath.Rel already placed
// absFile lexically under absRoot, and the walk likewise never examines its start
// dir's parents, so a repo legitimately checked out under a symlinked prefix still
// indexes normally. A missing/unreadable component (broken link, race) returns false
// and defers to the caller's os.Stat handling (deletion / no-op), never a false skip.
func pathCrossesSymlink(absRoot, absFile string) bool {
	for p := absFile; p != absRoot; {
		li, err := os.Lstat(p)
		if err != nil {
			return false
		}
		if li.Mode()&os.ModeSymlink != 0 {
			return true
		}
		parent := filepath.Dir(p)
		if parent == p {
			// Reached the filesystem root without matching absRoot (should not
			// happen once Rel has confirmed containment) — stop rather than loop.
			break
		}
		p = parent
	}
	return false
}

// normalizePath applies all path normalization rules:
// 1. Convert to forward slashes (filepath.ToSlash)
// 2. Strip repeated leading "./" segments if present
// 3. Apply Unicode NFC normalization
// This ensures cross-platform determinism (Windows/Linux/macOS). The function
// must be idempotent — normalizePath(normalizePath(p)) == normalizePath(p) —
// since its output is the IR's map key and may be re-normalized by a caller
// that doesn't know it's already normalized.
func normalizePath(relPath string) string {
	// Convert to forward slashes
	normalized := filepath.ToSlash(relPath)

	// Strip leading "./" if present — looped, not a single TrimPrefix, so a
	// doubled prefix ("././foo") fully resolves in one call instead of one
	// layer per call (which broke idempotence: FuzzNormalizePath found that
	// normalizePath("././foo") != normalizePath(normalizePath("././foo"))).
	for strings.HasPrefix(normalized, "./") {
		normalized = strings.TrimPrefix(normalized, "./")
	}

	// Apply Unicode NFC normalization
	// This ensures macOS NFD filenames and Linux NFC filenames produce identical output
	normalized = norm.NFC.String(normalized)

	return normalized
}

// isCode reports whether path looks like source, by either measure: a language
// we parse, or one we know of but do not. It is the guard on every capability
// skip that names a FILE.
//
// The rule it enforces is the one the whole contract rests on -- non-code is
// never in `skipped` -- because a consumer fails closed on that array, and a
// README or a PNG in it blocks a documentation-only change. Every branch that
// records a file skip must go through here; the dangling-symlink branch did not,
// and that was enough to reintroduce the false block.
func (g *Generator) isCode(path string) bool {
	ext := filepath.Ext(path)
	return g.supportsExtension(ext) || IsKnownSourceExtension(ext)
}

// supportsExtension returns true if any registered parser handles this extension.
//
// The extension is lowercased first. Every parser's SupportsExtension is an
// exact match (`ext == ".go"`), while IsKnownSourceExtension lowercases — so
// before this, `A.GO` fell through BOTH: not indexed (wrong case for the
// parser) and not reported (`.go` is deliberately absent from the source table,
// being a language we do parse). A file in a language RunEcho fully supports,
// invisible and unnamed. Lowercasing here closes the crack at the one
// chokepoint both the walk and parserFor already funnel through.
func (g *Generator) supportsExtension(ext string) bool {
	ext = strings.ToLower(ext)
	for _, p := range g.parsers {
		if p.SupportsExtension(ext) {
			return true
		}
	}
	return false
}

// parserFor returns the first parser that supports the given extension, or nil.
// Lowercased for the same reason as supportsExtension: the two must agree, or a
// file the walk accepted reaches parseFile with no parser to handle it.
func (g *Generator) parserFor(ext string) parser.Parser {
	ext = strings.ToLower(ext)
	for _, p := range g.parsers {
		if p.SupportsExtension(ext) {
			return p
		}
	}
	return nil
}

// defaultMaxParseBytes is the per-file size limit for source parsing. Files
// larger than this are skipped with a warning — oversized files are usually
// generated artifacts, not hand-authored source. It seeds Generator.maxParseBytes
// in NewGenerator; tests lower the per-Generator field, never a shared global.
const defaultMaxParseBytes int64 = 10 * 1024 * 1024

// parseFile parses a single file and returns its IR.
func (g *Generator) parseFile(path string) (FileIR, error) {
	info, err := os.Stat(path)
	if err != nil {
		return FileIR{}, fmt.Errorf("failed to stat file: %w", err)
	}
	if info.Size() > g.maxParseBytes {
		return FileIR{}, fmt.Errorf("skipping oversized file (%d bytes)", info.Size())
	}

	// Read file
	content, err := os.ReadFile(path)
	if err != nil {
		return FileIR{}, fmt.Errorf("failed to read file: %w", err)
	}

	// Hash the bytes already in memory — re-reading via HashFile would both
	// waste a syscall and race file modification between read and hash.
	hash := HashBytes(content)

	// Dispatch to the right parser by extension.
	//
	// Lowercased HERE, not just in parserFor. The extension is used twice: to
	// pick the parser, and (below) to pick the GRAMMAR inside an ExtAwareParser.
	// Lowercasing only the first made `A.TS` newly indexable while
	// jsLanguageFor's exact-match switch still fell through to the JavaScript
	// grammar -- so the file was parsed with the wrong grammar and silently lost
	// symbols (a TypeScript class method vanished). That is strictly worse than
	// the old behaviour of not indexing it at all: unindexed is reported as a
	// blind spot, mis-parsed is reported as a clean diff.
	ext := strings.ToLower(filepath.Ext(path))
	p := g.parserFor(ext)
	if p == nil {
		return FileIR{}, fmt.Errorf("no parser for extension %s", ext)
	}

	// Parse structure. Convert to string once and share with extractRefs below —
	// a 10 MiB file would otherwise hold three live copies of the source.
	src := string(content)
	// Pass the extension to parsers that need it to pick a grammar (JS/TS);
	// others use the plain Parse method.
	var structure parser.FileStructure
	if ep, ok := p.(parser.ExtAwareParser); ok {
		structure, err = ep.ParseExt(src, ext)
	} else {
		structure, err = p.Parse(src)
	}
	if err != nil {
		return FileIR{}, fmt.Errorf("failed to parse file: %w", err)
	}

	return FileIR{
		Hash:    hash,
		Symbols: symbolsFromStructure(structure, path, src),
		Refs:    extractRefs(path, src),
	}, nil
}

// symbolsFromStructure folds the parser's parallel arrays and "kind:name"-keyed
// hash/line maps into the canonical, sorted []Symbol. path and src additionally
// feed importedNames, which extracts the locally-bound names an import
// introduces (e.g. `Path` from `from pathlib import Path`) as a distinct
// "import_name" kind — the "import" kind above stays the parser's raw import
// paths ("pathlib"), preserving the legacy .ai/ir.json "imports" contract.
// SymbolsForLatestSnapshot reads symbol names regardless of kind, so adding
// "import_name" closes the gap where a bare call to an imported symbol read as
// unresolved because only its module path, never its bound name, was ever
// added to the known set (issues #76, #80).
func symbolsFromStructure(s parser.FileStructure, path, src string) []Symbol {
	var syms []Symbol
	add := func(names []string, kind string) {
		for _, n := range names {
			key := kind + ":" + n
			syms = append(syms, Symbol{Name: n, Kind: kind, Line: s.SymbolLines[key], Hash: s.SymbolHashes[key], Doc: s.SymbolDocs[key]})
		}
	}
	add(s.Functions, "function")
	add(s.Classes, "class")
	add(s.Exports, "export")
	add(s.Imports, "import")
	// Unexported top-level declarations (Go). Same mechanism as "import_name"
	// above and for the same reason: SymbolsForLatestSnapshot reads names
	// regardless of kind, so a distinct kind makes these resolvable at edit time
	// while leaving every exported-API-surface consumer — which filters on
	// function/class — showing exactly what it showed before.
	add(s.Unexported, "unexported")
	// Struct fields, receiver-qualified. Same kind mechanism again; needed because
	// a func-typed field is called exactly like a method, so the receiver-method
	// check cannot tell an invented member from a real field without them.
	add(s.Fields, "field")
	add(importedNames(path, src), "import_name")
	// Module specifiers behind a bare `export * from './mod'` re-export
	// (JS/TS). The names that re-export actually binds aren't enumerable from
	// this file alone (see FileStructure.WildcardReexports) — recording the
	// specifier under its own kind keeps the fact visible (`runecho-ir map`/
	// `locate`) instead of the prior silent drop, without fabricating export
	// names this file doesn't itself define.
	add(s.WildcardReexports, "export_wildcard")
	sortSymbols(syms)
	return syms
}

// importedNames returns the locally-bound names this file's import statements
// introduce, reusing the same extractor the PreToolUse hook already trusts
// (addInFileDefs in cmd/runecho-guard) so index-time and edit-time agree on
// what counts as resolved.
func importedNames(path, src string) []string {
	lang := guard.LangFor(path)
	return guard.ExtractImports(lang, guard.TextToAddedLines(src))
}

// extractRefs returns the sorted, deduplicated bare call targets in content,
// using the guard's extractor as the single source of truth (see FileIR.Refs).
// Always non-nil so the JSON form is a stable [] rather than null.
func extractRefs(path, content string) []string {
	lang := guard.LangFor(path)
	set := make(map[string]struct{})
	for _, ref := range guard.ExtractRefs(lang, guard.TextToAddedLines(content)) {
		set[ref.Name] = struct{}{}
	}
	refs := make([]string, 0, len(set))
	for name := range set {
		refs = append(refs, name)
	}
	sort.Strings(refs)
	return refs
}
