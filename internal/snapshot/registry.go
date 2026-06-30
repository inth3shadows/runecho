package snapshot

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/inth3shadows/runecho/internal/gitutil"
)

// Repo is an enrolled repository — the stable identity the oracle scopes all
// snapshot history by. The repos table is the single registry (no external
// manifest), foreign-keyed to snapshots so identity and history share one
// integrity boundary.
type Repo struct {
	ID          int64
	Name        string
	Path        string // enrollment lookup key (UNIQUE); never changes after enroll
	SourceRoot  string // directory walked for IR generation (schema V3; = Path for most repos)
	CommonDir   string // git-common-dir; stable lookup key across worktrees (schema V4; may be empty for pre-V4 rows)
	FileCap     int    // 0 = unlimited; honest-coverage cap for huge repos
	EnrolledAt  time.Time
	LastIndexed time.Time // zero if never indexed
	ParseErrors int
	// SupportedSeen is how many supported-extension files the last (re)index walk
	// encountered, including beyond the cap (schema V5; 0 = not yet measured).
	// Against the latest snapshot's file count it yields coverage %.
	SupportedSeen int
}

// EffectiveSourceRoot returns the directory to walk for IR generation. For repos
// enrolled before schema V3 where source_root may be empty, falls back to Path.
func (r *Repo) EffectiveSourceRoot() string {
	if r.SourceRoot != "" {
		return r.SourceRoot
	}
	return r.Path
}

// EnrollRepo registers a repo and returns its id. Strict: returns an error if the
// name or path is already taken (callers wanting auto-disambiguation handle that
// at their layer). sourceRoot is the directory walked for IR generation; when
// empty it defaults to path (the typical case for non-bare-repo layouts).
func (db *DB) EnrollRepo(name, path, sourceRoot string, fileCap int) (int64, error) {
	if sourceRoot == "" {
		sourceRoot = path
	}
	ts := time.Now().UTC().Format(time.RFC3339)
	res, err := db.conn.Exec(
		`INSERT INTO repos (name, path, source_root, file_cap, enrolled_at) VALUES (?, ?, ?, ?, ?)`,
		name, path, sourceRoot, fileCap, ts,
	)
	if err != nil {
		return 0, fmt.Errorf("enroll repo %q: %w", name, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("last insert id: %w", err)
	}
	return id, nil
}

// GetRepoByPath returns the repo enrolled at path, or nil, nil if none.
func (db *DB) GetRepoByPath(path string) (*Repo, error) {
	return scanRepo(db.conn.QueryRow(
		`SELECT id, name, path, source_root, common_dir, file_cap, enrolled_at, last_indexed, parse_errors, supported_seen
		 FROM repos WHERE path = ?`, path))
}

// GetRepoByName returns the repo with the given name, or nil, nil if none.
func (db *DB) GetRepoByName(name string) (*Repo, error) {
	return scanRepo(db.conn.QueryRow(
		`SELECT id, name, path, source_root, common_dir, file_cap, enrolled_at, last_indexed, parse_errors, supported_seen
		 FROM repos WHERE name = ?`, name))
}

// GetRepoByCommonDir returns the repo whose git-common-dir matches, or nil, nil
// if none. commonDir is the stable identity shared by all worktrees of a repo
// (schema V4); callers must pass a non-empty, normalized (absolute + cleaned)
// value — an empty arg must never match a pre-V4 row whose common_dir is NULL.
func (db *DB) GetRepoByCommonDir(commonDir string) (*Repo, error) {
	if commonDir == "" {
		return nil, nil
	}
	return scanRepo(db.conn.QueryRow(
		`SELECT id, name, path, source_root, common_dir, file_cap, enrolled_at, last_indexed, parse_errors, supported_seen
		 FROM repos WHERE common_dir = ?`, commonDir))
}

// GetReposByCommonDir returns all repos sharing the given git-common-dir, ordered
// by id. Usually a single row, but a bare repo whose worktrees are each
// independently enrolled yields several — ResolveRepo disambiguates those by the
// current worktree's path (issue #61). Empty commonDir matches nothing (a pre-V4
// NULL row must never match).
func (db *DB) GetReposByCommonDir(commonDir string) ([]*Repo, error) {
	if commonDir == "" {
		return nil, nil
	}
	rows, err := db.conn.Query(
		`SELECT id, name, path, source_root, common_dir, file_cap, enrolled_at, last_indexed, parse_errors, supported_seen
		 FROM repos WHERE common_dir = ? ORDER BY id`, commonDir)
	if err != nil {
		return nil, fmt.Errorf("repos by common_dir: %w", err)
	}
	defer rows.Close()

	var out []*Repo
	for rows.Next() {
		r, err := scanRepoRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetRepoCommonDir records a repo's git-common-dir (schema V4). Used both at
// enroll time and as a lazy backfill the first time the guard resolves a pre-V4
// repo via the worktree-list shim, after which lookup keys on common_dir in O(1).
func (db *DB) SetRepoCommonDir(id int64, commonDir string) error {
	if _, err := db.conn.Exec(
		`UPDATE repos SET common_dir = ? WHERE id = ?`, commonDir, id); err != nil {
		return fmt.Errorf("set common_dir for repo %d: %w", id, err)
	}
	return nil
}

// ListRepos returns all enrolled repos, ordered by name.
func (db *DB) ListRepos() ([]Repo, error) {
	rows, err := db.conn.Query(
		`SELECT id, name, path, source_root, common_dir, file_cap, enrolled_at, last_indexed, parse_errors, supported_seen
		 FROM repos ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list repos: %w", err)
	}
	defer rows.Close()

	var repos []Repo
	for rows.Next() {
		r, err := scanRepoRows(rows)
		if err != nil {
			return nil, err
		}
		repos = append(repos, *r)
	}
	return repos, rows.Err()
}

// RemoveRepo deletes a repo by id. Returns an error if the repo has any
// snapshots — use PurgeRepo to remove the repo and its full history atomically.
// Count and delete run in one transaction so a snapshot inserted between them
// can't be orphaned by the repo-row delete.
func (db *DB) RemoveRepo(id int64) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin remove: %w", err)
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM snapshots WHERE repo_id = ?`, id).Scan(&n); err != nil {
		return fmt.Errorf("count snapshots before remove: %w", err)
	}
	if n > 0 {
		return fmt.Errorf("repo %d has %d snapshot(s); use PurgeRepo to delete them along with the repo", id, n)
	}
	if _, err := tx.Exec(`DELETE FROM repos WHERE id = ?`, id); err != nil {
		return fmt.Errorf("remove repo %d: %w", id, err)
	}
	return tx.Commit()
}

// PurgeRepo deletes a repo and its entire snapshot history (symbols, files,
// snapshots, then the repo row) in one transaction — no orphaned rows.
func (db *DB) PurgeRepo(id int64) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin purge: %w", err)
	}
	defer tx.Rollback()
	stmts := []string{
		`DELETE FROM refs WHERE file_id IN (
			SELECT f.id FROM files f JOIN snapshots s ON f.snapshot_id = s.id WHERE s.repo_id = ?)`,
		`DELETE FROM symbols WHERE file_id IN (
			SELECT f.id FROM files f JOIN snapshots s ON f.snapshot_id = s.id WHERE s.repo_id = ?)`,
		`DELETE FROM files WHERE snapshot_id IN (SELECT id FROM snapshots WHERE repo_id = ?)`,
		`DELETE FROM snapshots WHERE repo_id = ?`,
		`DELETE FROM repos WHERE id = ?`,
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s, id); err != nil {
			return fmt.Errorf("purge repo %d: %w", id, err)
		}
	}
	return tx.Commit()
}

// DeleteAutoSnapshots removes the repo's rolling auto-fresh snapshots (label
// "auto") and their child rows. The PostToolUse auto-fresh hook (E6) calls this
// before writing a new "auto" snapshot, so at most one exists per repo at a time:
// the guard always reads the latest snapshot's symbols, but history is never
// bloated by a snapshot-per-edit, and manual snapshots (reindex/session-start/…)
// are never touched. Child rows are deleted explicitly because the schema has no
// ON DELETE CASCADE yet.
func (db *DB) DeleteAutoSnapshots(repoID int64) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin delete auto snapshots: %w", err)
	}
	defer tx.Rollback()
	if err := deleteAutoSnapshotsTx(tx, repoID); err != nil {
		return err
	}
	return tx.Commit()
}

// deleteAutoSnapshotsTx removes the repo's "auto" snapshots and their child rows
// using the provided transaction. Shared by DeleteAutoSnapshots and the atomic
// RollAutoSnapshot (which deletes + inserts in one tx). Child rows are deleted
// explicitly because the schema has no ON DELETE CASCADE yet.
func deleteAutoSnapshotsTx(tx *sql.Tx, repoID int64) error {
	stmts := []string{
		`DELETE FROM refs WHERE file_id IN (
			SELECT f.id FROM files f JOIN snapshots s ON f.snapshot_id = s.id
			WHERE s.repo_id = ? AND s.label = 'auto')`,
		`DELETE FROM symbols WHERE file_id IN (
			SELECT f.id FROM files f JOIN snapshots s ON f.snapshot_id = s.id
			WHERE s.repo_id = ? AND s.label = 'auto')`,
		`DELETE FROM files WHERE snapshot_id IN (
			SELECT id FROM snapshots WHERE repo_id = ? AND label = 'auto')`,
		`DELETE FROM snapshots WHERE repo_id = ? AND label = 'auto'`,
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s, repoID); err != nil {
			return fmt.Errorf("delete auto snapshots for repo %d: %w", repoID, err)
		}
	}
	return nil
}

// TouchRepo records indexing state after a (re)index: when it last ran, how
// many parse errors were seen, and how many supported files the walk
// encountered (self-observing / honest-coverage guarantees).
func (db *DB) TouchRepo(id int64, lastIndexed time.Time, parseErrors, supportedSeen int) error {
	res, err := db.conn.Exec(
		`UPDATE repos SET last_indexed = ?, parse_errors = ?, supported_seen = ? WHERE id = ?`,
		lastIndexed.UTC().Format(time.RFC3339), parseErrors, supportedSeen, id,
	)
	if err != nil {
		return fmt.Errorf("touch repo %d: %w", id, err)
	}
	// A zero-row update means id matched no repo (stale/wrong ID). Surface it
	// rather than reporting success — a silent no-op here hides a caller bug.
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("touch repo %d: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("touch repo %d: no such repo", id)
	}
	return nil
}

func scanRepo(row *sql.Row) (*Repo, error) {
	r, err := scanRepoCols(row.Scan)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return r, err
}

func scanRepoRows(rows *sql.Rows) (*Repo, error) {
	return scanRepoCols(rows.Scan)
}

// scanRepoCols scans the standard repo column order via the given Scan closure,
// shared by *sql.Row and *sql.Rows. last_indexed is nullable; source_root is
// nullable for pre-V3 rows (though migrateV3 backfills it to path for all existing rows).
func scanRepoCols(scan func(...any) error) (*Repo, error) {
	var r Repo
	var sourceRoot sql.NullString
	var commonDir sql.NullString
	var enrolled string
	var lastIndexed sql.NullString
	if err := scan(&r.ID, &r.Name, &r.Path, &sourceRoot, &commonDir, &r.FileCap, &enrolled, &lastIndexed, &r.ParseErrors, &r.SupportedSeen); err != nil {
		return nil, err
	}
	if sourceRoot.Valid {
		r.SourceRoot = sourceRoot.String
	}
	if commonDir.Valid {
		r.CommonDir = commonDir.String
	}
	var parseErr error
	r.EnrolledAt, parseErr = time.Parse(time.RFC3339, enrolled)
	if parseErr != nil {
		fmt.Fprintf(os.Stderr, "runecho: warning: repo %d enrolled_at %q: %v\n", r.ID, enrolled, parseErr)
	}
	if lastIndexed.Valid {
		r.LastIndexed, parseErr = time.Parse(time.RFC3339, lastIndexed.String)
		if parseErr != nil {
			fmt.Fprintf(os.Stderr, "runecho: warning: repo %d last_indexed %q: %v\n", r.ID, lastIndexed.String, parseErr)
			r.LastIndexed = time.Time{}
		}
	}
	return &r, nil
}

// DeriveRepoName builds a default enrollment name from the last two path
// segments (e.g. runecho/master → "runecho-master"), avoiding collisions
// in bare-worktree layouts. Falls back to the basename at a filesystem root.
func DeriveRepoName(root string) string {
	base := filepath.Base(root)
	parent := filepath.Base(filepath.Dir(root))
	if parent == "" || parent == "." || parent == base || parent == string(filepath.Separator) {
		return base
	}
	return parent + "-" + base
}

// UniqueName returns desired if no repo with that name exists in db, otherwise
// returns desired-2, desired-3, … until a free name is found.
func UniqueName(db *DB, desired string) (string, error) {
	name := desired
	for i := 2; ; i++ {
		existing, err := db.GetRepoByName(name)
		if err != nil {
			return "", err
		}
		if existing == nil {
			return name, nil
		}
		name = fmt.Sprintf("%s-%d", desired, i)
	}
}

// ResolveRepo finds the enrolled repo whose git tree contains dir and returns
// it with the enrolled path (repoRoot) and ok=true. Returns ok=false when no
// enrolled repo is reachable from dir (not enrolled, non-git dir, or DB error).
//
// Three-tier resolution mirrors the guard's logic so CLI and guard always agree:
//
//  1. CommonDir fast path — git-common-dir keyed lookup. The common-dir is
//     stable across all worktrees of a repo (bare or not), so a bare-repo
//     claudew worktree resolves in O(1) once common_dir is populated.
//  2. TopLevel tier — git rev-parse --show-toplevel → GetRepoByPath. Handles
//     regular repos enrolled before V4 populated common_dir; backfills on hit.
//  3. Worktree shim — git worktree list → try each registered worktree path.
//     Covers bare-repo layouts where the enrollment used a specific worktree.
//     Also backfills common_dir so the next call takes the fast path.
//
// repoRoot is the enrolled path (repo.Path) in all tiers — callers that need
// a different directory (e.g. the user's actual cwd for live IR generation)
// should ignore repoRoot and use their own path.
func (db *DB) ResolveRepo(dir string) (repo *Repo, repoRoot string, ok bool) {
	// The Get* helpers return (nil, nil) for "not enrolled" (sql.ErrNoRows), so a
	// non-nil error here is ALWAYS a real DB fault. We still degrade to ok=false
	// (callers — guard included — must stay fail-open), but a transient DB error
	// is otherwise indistinguishable from "not enrolled"; warn so the fault is
	// debuggable rather than silent. Matches the package's degraded-state warnings.
	warnResolve := func(tier string, err error) {
		fmt.Fprintf(os.Stderr, "runecho: ResolveRepo %s lookup failed (treating as not-enrolled): %v\n", tier, err)
	}

	// Tier 1: common-dir fast path. A bare repo's worktrees share one common-dir,
	// so several independently-enrolled worktrees can match here (issue #61). When
	// they do, disambiguate to the enrollment whose path is THIS worktree's
	// top-level, so the edit is validated against its own snapshot rather than a
	// sibling worktree's (which may be on different code). A single match keeps the
	// O(1) fast path; the path tie-break only runs in the rare multi-enrollment case.
	commonDir, cdErr := gitutil.CommonDir(dir)
	if cdErr == nil {
		repos, err := db.GetReposByCommonDir(commonDir)
		switch {
		case err != nil:
			warnResolve("common-dir", err)
		case len(repos) == 1:
			return repos[0], repos[0].Path, true
		case len(repos) > 1:
			if top, e := gitutil.TopLevel(dir); e == nil {
				for _, r := range repos {
					if filepath.Clean(r.Path) == filepath.Clean(top) {
						return r, r.Path, true
					}
				}
			}
			// No worktree-specific enrollment matched (e.g. an unenrolled sibling
			// worktree): fall back to the canonical lowest-id row so resolution
			// still succeeds, preferring the oldest enrollment deterministically.
			return repos[0], repos[0].Path, true
		}
	}
	// Tier 2: git top-level → exact path lookup.
	topLevel, err := gitutil.TopLevel(dir)
	if err != nil {
		return nil, "", false
	}
	if r, err := db.GetRepoByPath(topLevel); err != nil {
		warnResolve("top-level", err)
	} else if r != nil {
		db.backfillCommonDir(r.ID, commonDir, cdErr)
		return r, topLevel, true
	}
	// Tier 3: worktree shim — check all registered worktree paths.
	for _, wt := range gitutil.WorktreePaths(topLevel) {
		if wt == topLevel {
			continue
		}
		if r, err := db.GetRepoByPath(wt); err != nil {
			warnResolve("worktree", err)
		} else if r != nil {
			db.backfillCommonDir(r.ID, commonDir, cdErr)
			return r, wt, true
		}
	}
	return nil, "", false
}

// backfillCommonDir records common_dir on a repo resolved via a compat tier so
// the next ResolveRepo call takes the O(1) fast path. Best-effort: errors are
// silently dropped (the compat tier remains as fallback).
func (db *DB) backfillCommonDir(repoID int64, commonDir string, cdErr error) {
	if cdErr == nil && commonDir != "" {
		_ = db.SetRepoCommonDir(repoID, commonDir)
	}
}
