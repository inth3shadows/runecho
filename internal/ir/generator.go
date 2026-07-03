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
		parsers:       []parser.Parser{parser.NewJSParser(), parser.NewGoParser(), parser.NewPythonParser()},
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

// walkSourceFiles walks absRoot, calling fn for each supported source file.
// It skips ignored directories, symlinked directories, and unsupported extensions.
// The walk is checked for cancellation before each entry, so a done ctx
// (deadline or explicit cancel) aborts it between files and propagates ctx.Err()
// to the caller. Per-file granularity is sufficient: a single oversized file is
// already bounded by maxParseBytes.
func (g *Generator) walkSourceFiles(ctx context.Context, absRoot string, fn walkerFunc) error {
	return filepath.Walk(absRoot, func(path string, info os.FileInfo, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if err != nil {
			g.warn("Warning: failed to access %s: %v\n", path, err)
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			if g.ignoredPaths[filepath.Base(path)] {
				return filepath.SkipDir
			}
			return nil
		}
		if !g.supportsExtension(filepath.Ext(path)) {
			return nil
		}
		relPath, err := filepath.Rel(absRoot, path)
		if err != nil {
			g.warn("Warning: failed to compute relative path for %s: %v\n", path, err)
			return nil
		}
		return fn(path, normalizePath(relPath))
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

	if err := g.walkSourceFiles(ctx, absRoot, func(absPath, normPath string) error {
		stats.SupportedSeen++
		if g.capReached(len(result.Files)) {
			return nil // count only; cap bounds parse work, not the denominator
		}
		fileIR, err := g.parseFile(absPath)
		if err != nil {
			g.warn("Warning: failed to parse %s: %v\n", absPath, err)
			stats.ParseErrors++
			return nil
		}
		result.Files[normPath] = fileIR
		return nil
	}); err != nil {
		return nil, Stats{}, fmt.Errorf("failed to walk directory: %w", err)
	}

	result.RootHash = ComputeRootHash(result.Files)
	stats.Indexed = len(result.Files)
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

	if err := g.walkSourceFiles(ctx, absRoot, func(absPath, normPath string) error {
		stats.SupportedSeen++
		if g.capReached(len(updated.Files)) {
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
			return nil
		}
		currentHash, err := HashFile(absPath)
		if err != nil {
			g.warn("Warning: failed to hash %s: %v\n", absPath, err)
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
			return nil
		}
		updated.Files[normPath] = fileIR
		return nil
	}); err != nil {
		return nil, Stats{}, fmt.Errorf("failed to walk directory: %w", err)
	}

	updated.RootHash = ComputeRootHash(updated.Files)
	stats.Indexed = len(updated.Files)
	return updated, stats, nil
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
	case info.IsDir() || !g.supportsExtension(filepath.Ext(absFile)):
		// Not an indexed source file. If it used to be one (extension changed),
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

// supportsExtension returns true if any registered parser handles this extension.
func (g *Generator) supportsExtension(ext string) bool {
	for _, p := range g.parsers {
		if p.SupportsExtension(ext) {
			return true
		}
	}
	return false
}

// parserFor returns the first parser that supports the given extension, or nil.
func (g *Generator) parserFor(ext string) parser.Parser {
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

	// Dispatch to the right parser by extension
	ext := filepath.Ext(path)
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
			syms = append(syms, Symbol{Name: n, Kind: kind, Line: s.SymbolLines[key], Hash: s.SymbolHashes[key]})
		}
	}
	add(s.Functions, "function")
	add(s.Classes, "class")
	add(s.Exports, "export")
	add(s.Imports, "import")
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
