package mcp

import (
	"encoding/json"
	"fmt"
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
		Description: "Deterministic structure (files + symbols) of an enrolled repo's current code. Use to ground claims about what functions/types/exports exist.",
		InputSchema: repoSchema("name of an enrolled repo (see `health`/registry)"),
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
		Description: "Per-repo health: last indexed, staleness, parse errors, snapshot count, latest stored hash, file cap.",
		InputSchema: repoSchema("name of an enrolled repo"),
		Handler:     o.status,
	})
	s.Register(Tool{
		Name:        "health",
		Description: "Store-wide health: schema version, integrity check, number of enrolled repos, db path.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		Handler:     o.health,
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

func diffSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"repo":  map[string]any{"type": "string", "description": "name of an enrolled repo"},
			"a":     map[string]any{"type": "integer", "description": "snapshot id A (with b)"},
			"b":     map[string]any{"type": "integer", "description": "snapshot id B (with a)"},
			"since": map[string]any{"type": "string", "description": "diff latest snapshot with this label vs live code"},
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
		return nil, fmt.Errorf("repo %q is not enrolled; enroll it with `ai-ir repo add`", name)
	}
	return repo, nil
}

func liveIR(path string) (*ir.IR, error) {
	gen := ir.NewGenerator(ir.GeneratorConfig{IgnoredPaths: ir.DefaultIgnoredPaths})
	irData, _, err := gen.Generate(path)
	return irData, err
}

func jsonText(v any) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// --- tools ---

func (o *Oracle) structure(args json.RawMessage) (string, error) {
	var a repoArg
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	repo, err := o.resolveRepo(a.Repo)
	if err != nil {
		return "", err
	}
	irData, err := liveIR(repo.EffectiveSourceRoot())
	if err != nil {
		return "", err
	}
	symCount := 0
	for _, f := range irData.Files {
		symCount += len(f.Functions) + len(f.Classes) + len(f.Exports) + len(f.Imports)
	}
	return jsonText(map[string]any{
		"repo":         repo.Name,
		"root_hash":    irData.RootHash,
		"file_count":   len(irData.Files),
		"symbol_count": symCount,
		"files":        irData.Files,
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
	irData, err := liveIR(repo.EffectiveSourceRoot())
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
		Repo  string `json:"repo"`
		A     *int64 `json:"a"`
		B     *int64 `json:"b"`
		Since string `json:"since"`
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
		meta, err := o.db.GetLatestByLabel(repo.ID, a.Since)
		if err != nil {
			return "", err
		}
		if meta == nil {
			return "", fmt.Errorf("no snapshot labeled %q for repo %q", a.Since, repo.Name)
		}
		live, err := liveIR(repo.EffectiveSourceRoot())
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
			return "", fmt.Errorf("repo %q has no snapshots; run `ai-ir repo reindex %s`", repo.Name, repo.Name)
		}
		live, err := liveIR(repo.EffectiveSourceRoot())
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
	}
	return jsonText(out)
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
