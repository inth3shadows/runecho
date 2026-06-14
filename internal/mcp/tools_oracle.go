package mcp

import (
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/inth3shadows/runecho/internal/ir"
	"github.com/inth3shadows/runecho/internal/snapshot"
)

// liveIR builds a deterministic full IR for the repo's current code.
// Uses ir.DefaultIgnoredPaths — the single canonical list shared with the CLI
// so oracle hashes are always identical to stored snapshot hashes.

// Oracle exposes deterministic, read-only structure/drift queries over the
// central store. Every tool resolves a repo by its enrolled name, so the oracle
// only ever answers for repos in the registry.
type Oracle struct {
	db     *snapshot.DB
	dbPath string
}

// NewOracle builds an oracle over an open central store.
func NewOracle(db *snapshot.DB, dbPath string) *Oracle {
	return &Oracle{db: db, dbPath: dbPath}
}

// Register wires the oracle's tools onto the server.
func (o *Oracle) Register(s *Server) {
	s.Register(Tool{
		Name:        "structure",
		Description: "Deterministic structure (files + symbols) of an enrolled repo's current code. Use to ground claims about what functions/types/exports exist. Scope with `paths` globs and pick a `detail` level to keep responses small.",
		InputSchema: structureSchema(),
		Handler:     o.structure,
	})
	s.Register(Tool{
		Name:        "diff",
		Description: "Structural drift for an enrolled repo. With a+b (snapshot ids) diffs those snapshots; with `since`=label diffs that snapshot vs live code; default diffs the latest snapshot vs live code.",
		InputSchema: diffSchema(),
		Handler:     o.diff,
	})
	s.Register(Tool{
		Name:        "hash",
		Description: "Deterministic root hash + file count of an enrolled repo's current code. Same code → identical hash across machines.",
		InputSchema: repoSchema("name of an enrolled repo"),
		Handler:     o.hash,
	})
	s.Register(Tool{
		Name:        "status",
		Description: "Per-repo health: last indexed, staleness, parse errors, coverage %, snapshot count, latest stored hash, file cap.",
		InputSchema: repoSchema("name of an enrolled repo"),
		Handler:     o.status,
	})
	s.Register(Tool{
		Name:        "health",
		Description: "Store-wide health: schema version, integrity check, number of enrolled repos, db path.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		Handler:     o.health,
	})
	s.Register(Tool{
		Name:        "locate",
		Description: "Deterministically locate symbols in an enrolled repo: name → file:line (+ short body hash). Pass `symbol` to find a specific definition without grepping; omit it to list all (capped). Defaults to functions+classes.",
		InputSchema: locateSchema(),
		Handler:     o.locate,
	})
}

// --- argument helpers ---

type repoArg struct {
	Repo string `json:"repo"`
}

func repoSchema(desc string) map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"repo": map[string]any{"type": "string", "description": desc},
		},
		"required": []string{"repo"},
	}
}

type structArg struct {
	Repo   string   `json:"repo"`
	Paths  []string `json:"paths"`
	Detail string   `json:"detail"`
}

func structureSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"repo":  map[string]any{"type": "string", "description": "name of an enrolled repo (see `health`/registry)"},
			"paths": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "optional glob filters; return only matching files (e.g. \"internal/mcp/**\" or \"*.go\"). `**` matches across directories. Omit for the whole repo."},
			"detail": map[string]any{"type": "string", "enum": []string{"tree", "symbols", "full"},
				"description": "tree = file paths + symbol counts only; symbols (default) = per-file symbols[] (name/kind/line/hash) + refs; full = also the legacy imports/functions/classes/exports arrays + symbol_hashes (redundant with symbols[], for back-compat)"},
		},
		"required": []string{"repo"},
	}
}

// matchAnyGlob reports whether p matches any of the patterns. Supports a single
// "**" (matches across directory separators) plus stdlib path.Match for simple
// patterns; a bare directory prefix (e.g. "internal/mcp") also matches its tree.
func matchAnyGlob(patterns []string, p string) bool {
	for _, pat := range patterns {
		if matchGlob(pat, p) {
			return true
		}
	}
	return false
}

func matchGlob(pattern, p string) bool {
	if strings.Contains(pattern, "**") {
		parts := strings.SplitN(pattern, "**", 2)
		pre := strings.TrimSuffix(parts[0], "/")
		suf := strings.TrimPrefix(parts[1], "/")
		if pre != "" && !strings.HasPrefix(p, pre) {
			return false
		}
		if suf != "" && !strings.HasSuffix(p, suf) {
			return false
		}
		return true
	}
	if ok, _ := path.Match(pattern, p); ok {
		return true
	}
	// allow a plain directory prefix to select its whole subtree
	return strings.HasPrefix(p, strings.TrimSuffix(pattern, "/")+"/")
}

func diffSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"repo":    map[string]any{"type": "string", "description": "name of an enrolled repo"},
			"a":       map[string]any{"type": "integer", "description": "snapshot id A (with b)"},
			"b":       map[string]any{"type": "integer", "description": "snapshot id B (with a)"},
			"since":   map[string]any{"type": "string", "description": "diff latest snapshot with this label vs live code"},
			"session": map[string]any{"type": "string", "description": "with `since`: pin the reference snapshot to this session id"},
		},
		"required": []string{"repo"},
	}
}

func locateSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"repo":   map[string]any{"type": "string", "description": "name of an enrolled repo"},
			"symbol": map[string]any{"type": "string", "description": "symbol to locate: matches by exact name, name prefix, or last dotted segment (e.g. \"fetch\" finds \"Reader.fetch\"). Omit to list all (capped)."},
			"kind":   map[string]any{"type": "string", "description": "restrict to func|class|export|import (default: func+class)"},
		},
		"required": []string{"repo"},
	}
}

// resolveRepo looks up an enrolled repo by name.
func (o *Oracle) resolveRepo(name string) (*snapshot.Repo, error) {
	if name == "" {
		return nil, fmt.Errorf("missing required arg: repo")
	}
	repo, err := o.db.GetRepoByName(name)
	if err != nil {
		return nil, err
	}
	if repo == nil {
		return nil, fmt.Errorf("repo %q is not enrolled; enroll it with `runecho-ir repo add`", name)
	}
	return repo, nil
}

// liveIR builds a deterministic full IR for path, applying fileCap (0 = unlimited)
// so the live IR is generated under the same cap as the repo's stored snapshots.
// A mismatch here would make every diff/hash report phantom drift for capped repos.
func liveIR(path string, fileCap int) (*ir.IR, error) {
	gen := ir.NewGenerator(ir.GeneratorConfig{IgnoredPaths: ir.DefaultIgnoredPaths, FileCap: fileCap})
	irData, _, err := gen.Generate(path)
	return irData, err
}

func jsonText(v any) (string, error) {
	// Minified (no indentation): MCP responses are consumed by an LLM, which
	// parses JSON identically regardless of whitespace. Dropping the 2-space
	// indent measured ~23% fewer tokens on structure/diff/locate payloads,
	// losslessly. See ~/.claude/plans/headroom-compression-eval-test-plan.md.
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// --- tools ---

func (o *Oracle) structure(args json.RawMessage) (string, error) {
	var a structArg
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	repo, err := o.resolveRepo(a.Repo)
	if err != nil {
		return "", err
	}
	detail := a.Detail
	if detail == "" {
		detail = "symbols"
	}
	if detail != "tree" && detail != "symbols" && detail != "full" {
		return "", fmt.Errorf("bad detail %q: want tree|symbols|full", detail)
	}
	irData, err := liveIR(repo.EffectiveSourceRoot(), repo.FileCap)
	if err != nil {
		return "", err
	}

	// Select (and sort) the file paths to include, applying `paths` globs.
	paths := make([]string, 0, len(irData.Files))
	for p := range irData.Files {
		if len(a.Paths) == 0 || matchAnyGlob(a.Paths, p) {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)

	// Project each file to the requested detail level. `symbols` (default) drops
	// the legacy imports/functions/classes/exports arrays + symbol_hashes that
	// FileIR.MarshalJSON emits for on-disk back-compat — all redundant with
	// symbols[] — keeping responses lean without losing information.
	symCount := 0
	files := make(map[string]any, len(paths))
	for _, p := range paths {
		f := irData.Files[p]
		symCount += len(f.Symbols)
		switch detail {
		case "tree":
			files[p] = map[string]any{"hash": f.Hash, "symbol_count": len(f.Symbols)}
		case "symbols":
			fv := map[string]any{"hash": f.Hash, "symbols": f.Symbols}
			if len(f.Refs) > 0 {
				fv["refs"] = f.Refs
			}
			files[p] = fv
		case "full":
			files[p] = f // FileIR.MarshalJSON -> legacy arrays + symbols
		}
	}
	return jsonText(map[string]any{
		"repo":         repo.Name,
		"root_hash":    irData.RootHash,
		"file_count":   len(paths),
		"symbol_count": symCount,
		"detail":       detail,
		"files":        files,
	})
}

func (o *Oracle) hash(args json.RawMessage) (string, error) {
	var a repoArg
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	repo, err := o.resolveRepo(a.Repo)
	if err != nil {
		return "", err
	}
	irData, err := liveIR(repo.EffectiveSourceRoot(), repo.FileCap)
	if err != nil {
		return "", err
	}
	return jsonText(map[string]any{
		"repo":       repo.Name,
		"root_hash":  irData.RootHash,
		"file_count": len(irData.Files),
	})
}

func (o *Oracle) diff(args json.RawMessage) (string, error) {
	var a struct {
		Repo    string `json:"repo"`
		A       *int64 `json:"a"`
		B       *int64 `json:"b"`
		Since   string `json:"since"`
		Session string `json:"session"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	repo, err := o.resolveRepo(a.Repo)
	if err != nil {
		return "", err
	}

	// Reject a half-specified pair: with only one of a/b the request would
	// silently fall through to latest-vs-live and answer a different question.
	if (a.A != nil) != (a.B != nil) {
		return "", fmt.Errorf("diff requires both `a` and `b` snapshot ids together, or neither")
	}
	// session only qualifies a since-label lookup; alone (or with a/b) it would
	// silently do nothing — refuse rather than answer a different question.
	if a.Session != "" && a.Since == "" {
		return "", fmt.Errorf("`session` requires `since`")
	}

	var result snapshot.DiffResult

	switch {
	case a.A != nil && a.B != nil:
		metaA, err := o.scopedSnapshot(*a.A, repo.ID)
		if err != nil {
			return "", err
		}
		metaB, err := o.scopedSnapshot(*a.B, repo.ID)
		if err != nil {
			return "", err
		}
		result, err = o.db.Diff(*metaA, *metaB)
		if err != nil {
			return "", err
		}
	case a.Since != "":
		var meta *snapshot.SnapshotMeta
		var err error
		if a.Session != "" {
			meta, err = o.db.GetLatestByLabelSession(repo.ID, a.Since, a.Session)
		} else {
			meta, err = o.db.GetLatestByLabel(repo.ID, a.Since)
		}
		if err != nil {
			return "", err
		}
		if meta == nil {
			if a.Session != "" {
				return "", fmt.Errorf("no snapshot labeled %q with session %q for repo %q", a.Since, a.Session, repo.Name)
			}
			return "", fmt.Errorf("no snapshot labeled %q for repo %q", a.Since, repo.Name)
		}
		live, err := liveIR(repo.EffectiveSourceRoot(), repo.FileCap)
		if err != nil {
			return "", err
		}
		result, err = o.db.DiffLive(*meta, live)
		if err != nil {
			return "", err
		}
	default:
		latest, err := o.db.List(repo.ID, 1)
		if err != nil {
			return "", err
		}
		if len(latest) == 0 {
			return "", fmt.Errorf("repo %q has no snapshots; run `runecho-ir repo reindex %s`", repo.Name, repo.Name)
		}
		live, err := liveIR(repo.EffectiveSourceRoot(), repo.FileCap)
		if err != nil {
			return "", err
		}
		result, err = o.db.DiffLive(latest[0], live)
		if err != nil {
			return "", err
		}
	}

	// Single source of truth for the diff JSON shape, shared with the
	// `runecho-ir diff --json` CLI so the two surfaces cannot drift.
	payload := snapshot.DiffPayload(result)
	payload["repo"] = repo.Name
	return jsonText(payload)
}

// scopedSnapshot fetches a snapshot by id but rejects it if it belongs to a
// different repo — diffs never cross repo boundaries.
func (o *Oracle) scopedSnapshot(id, repoID int64) (*snapshot.SnapshotMeta, error) {
	meta, err := o.db.GetByID(id)
	if err != nil {
		return nil, err
	}
	if meta == nil {
		return nil, fmt.Errorf("snapshot %d not found", id)
	}
	if meta.RepoID != repoID {
		return nil, fmt.Errorf("snapshot %d does not belong to this repo", id)
	}
	return meta, nil
}

func (o *Oracle) status(args json.RawMessage) (string, error) {
	var a repoArg
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	repo, err := o.resolveRepo(a.Repo)
	if err != nil {
		return "", err
	}
	count, err := o.db.CountSnapshots(repo.ID)
	if err != nil {
		return "", err
	}
	out := map[string]any{
		"repo":           repo.Name,
		"id":             repo.ID,
		"path":           repo.Path,
		"file_cap":       repo.FileCap,
		"parse_errors":   repo.ParseErrors,
		"snapshot_count": count,
		"last_indexed":   nil,
		"stale_seconds":  nil,
	}
	if !repo.LastIndexed.IsZero() {
		out["last_indexed"] = repo.LastIndexed.Format(time.RFC3339)
		out["stale_seconds"] = int64(time.Since(repo.LastIndexed).Seconds())
	}
	if latest, err := o.db.List(repo.ID, 1); err == nil && len(latest) == 1 {
		out["latest_root_hash"] = latest[0].RootHash
		// Coverage as of the last walk: indexed (latest snapshot's file count) over
		// supported files seen. supported_seen=0 means no post-V5 reindex yet.
		if repo.SupportedSeen > 0 {
			out["supported_files"] = repo.SupportedSeen
			out["coverage_percent"] = snapshot.CoveragePercent(latest[0].FileCount, repo.SupportedSeen)
		}
	}
	return jsonText(out)
}

// locateMatchCap bounds how many symbols a single locate call returns. A query
// usually narrows to a handful; an unfiltered call on a large repo would dump
// the whole table into the agent's context, defeating the on-demand design.
const locateMatchCap = 200

func (o *Oracle) locate(args json.RawMessage) (string, error) {
	var a struct {
		Repo   string `json:"repo"`
		Symbol string `json:"symbol"`
		Kind   string `json:"kind"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	kind, ok := normalizeLocateKind(a.Kind)
	if !ok {
		return "", fmt.Errorf("invalid kind %q (want func|class|export|import)", a.Kind)
	}
	repo, err := o.resolveRepo(a.Repo)
	if err != nil {
		return "", err
	}
	irData, err := liveIR(repo.EffectiveSourceRoot(), repo.FileCap)
	if err != nil {
		return "", err
	}

	keep := func(k string) bool {
		if kind != "" {
			return k == kind
		}
		return k == "function" || k == "class"
	}

	var matches []ir.SymbolLoc
	for _, s := range irData.SymbolLocations() { // deterministically sorted
		if !keep(s.Kind) {
			continue
		}
		if a.Symbol != "" && !symbolMatches(s.Name, a.Symbol) {
			continue
		}
		// Shorten the hash for the wire — same 4-char convention as the CLI map.
		if len(s.Hash) >= 4 {
			s.Hash = s.Hash[:4]
		}
		matches = append(matches, s)
	}

	total := len(matches)
	truncated := false
	if len(matches) > locateMatchCap {
		matches = matches[:locateMatchCap]
		truncated = true
	}
	if matches == nil {
		matches = []ir.SymbolLoc{}
	}
	return jsonText(map[string]any{
		"repo":      repo.Name,
		"query":     a.Symbol,
		"count":     len(matches),
		"total":     total,
		"truncated": truncated,
		"symbols":   matches,
	})
}

// symbolMatches reports whether a symbol name satisfies a locate query: exact
// match, name prefix, or an exact match on the last dotted segment (so "fetch"
// finds the method "Reader.fetch").
func symbolMatches(name, query string) bool {
	if name == query || strings.HasPrefix(name, query) {
		return true
	}
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[i+1:] == query
	}
	return false
}

// normalizeLocateKind maps user-facing kind values to internal kind names.
func normalizeLocateKind(k string) (string, bool) {
	switch k {
	case "":
		return "", true
	case "func", "function":
		return "function", true
	case "class", "cls":
		return "class", true
	case "export", "exp":
		return "export", true
	case "import", "imp":
		return "import", true
	default:
		return "", false
	}
}

func (o *Oracle) health(_ json.RawMessage) (string, error) {
	h, err := o.db.Health()
	if err != nil {
		return "", err
	}
	return jsonText(map[string]any{
		"server":         "runecho",
		"db_path":        o.dbPath,
		"schema_version": h.SchemaVersion,
		"integrity":      h.Integrity,
		"repo_count":     h.RepoCount,
	})
}
