package snapshot

import (
	"database/sql"
	"errors"
	"fmt"

	_ "modernc.org/sqlite"
)

// DB wraps a SQLite connection for snapshot storage.
type DB struct {
	conn *sql.DB
}

// Open opens (or creates) the snapshot DB at path, sets pragmas, and migrates schema.
func Open(path string) (*DB, error) {
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}

	// Single writer — avoid "database is locked" on WAL.
	conn.SetMaxOpenConns(1)

	db := &DB{conn: conn}
	if err := db.setPragmas(); err != nil {
		conn.Close()
		return nil, err
	}
	if err := db.integrityCheck(); err != nil {
		conn.Close()
		return nil, err
	}
	if err := db.migrate(); err != nil {
		conn.Close()
		return nil, err
	}
	return db, nil
}

// OpenFast opens the snapshot DB for latency-sensitive read paths, skipping the
// on-open PRAGMA quick_check integrity scan that Open performs. That scan reads
// the whole file (~137ms on a 48 MiB store) — acceptable for the writer/CLI, but
// far too costly for the PreToolUse guard hook, which fires on every edit and only
// reads. Integrity is the generator's responsibility on write; a read that races a
// corruption simply yields a query error, and the guard degrades to defer. Pragmas
// and migration (both cheap when the schema is current) are still applied.
func OpenFast(path string) (*DB, error) {
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}
	conn.SetMaxOpenConns(1)

	db := &DB{conn: conn}
	if err := db.setPragmas(); err != nil {
		conn.Close()
		return nil, err
	}
	if err := db.migrate(); err != nil {
		conn.Close()
		return nil, err
	}
	return db, nil
}

// integrityCheck runs PRAGMA quick_check (cheaper than integrity_check, sufficient
// for catching corruption on open). Durability guarantee: never serve a corrupt DB.
func (db *DB) integrityCheck() error {
	var result string
	if err := db.conn.QueryRow("PRAGMA quick_check").Scan(&result); err != nil {
		return fmt.Errorf("quick_check: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("integrity check failed: %s", result)
	}
	return nil
}

// BackupTo writes an atomic, consistent single-file backup using VACUUM INTO.
// The destination must not already exist (SQLite requirement).
func (db *DB) BackupTo(path string) error {
	if _, err := db.conn.Exec("VACUUM INTO ?", path); err != nil {
		return fmt.Errorf("vacuum into %q: %w", path, err)
	}
	return nil
}

func (db *DB) setPragmas() error {
	for _, p := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA synchronous=NORMAL",
		// Wait up to 5s for a competing writer instead of failing immediately
		// with "database is locked" — MaxOpenConns(1) only serializes in-process,
		// so a second runecho process (CLI write vs. MCP first-run migrate) can
		// still contend for the write lock.
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.conn.Exec(p); err != nil {
			return fmt.Errorf("pragma %q: %w", p, err)
		}
	}
	return nil
}

// ErrSchemaNewer marks the permanent (non-transient) open failure where the
// store was migrated by a newer binary. Callers that degrade gracefully on
// open errors (the guard is fail-open by design) must still surface THIS one
// loudly: it means validation is silently disabled until the stale binary is
// rebuilt, and it never resolves on its own.
var ErrSchemaNewer = errors.New("store schema is newer than this binary")

// migration brings the schema from version N to N+1, where N is its index in the
// migrations slice. Each runs in its own transaction; user_version is bumped only
// on commit, so a partial upgrade can never leave a torn schema.
type migration func(*sql.Tx) error

var migrations = []migration{
	migrateV1, // 0 → 1: baseline snapshots/files/symbols
	migrateV2, // 1 → 2: central-store repos registry + snapshots.repo_id
	migrateV3, // 2 → 3: split repo.Path into lookup key (path) + source root (source_root)
	migrateV4, // 3 → 4: add common_dir — stable cross-worktree lookup key
	migrateV5, // 4 → 5: add supported_seen — honest-coverage denominator
	migrateV6, // 5 → 6: refs table — bare call sites per snapshot file
	migrateV7, // 6 → 7: refs uniqueness — (file_id, name) enforced by the schema
	migrateV8, // 7 → 8: symbols.sig_hash — per-symbol body hash for modified-symbol diff
}

// SchemaVersion is the latest schema version this binary understands.
var SchemaVersion = len(migrations)

func (db *DB) migrate() error {
	var current int
	if err := db.conn.QueryRow("PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	if current > len(migrations) {
		return fmt.Errorf("db schema version %d is newer than this binary supports (%d); upgrade runecho: %w", current, len(migrations), ErrSchemaNewer)
	}
	for v := current; v < len(migrations); v++ {
		tx, err := db.conn.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", v+1, err)
		}
		if err := migrations[v](tx); err != nil {
			tx.Rollback()
			return fmt.Errorf("migrate to v%d: %w", v+1, err)
		}
		// PRAGMA user_version cannot be parameterized; v+1 is a trusted loop index.
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", v+1)); err != nil {
			tx.Rollback()
			return fmt.Errorf("set user_version %d: %w", v+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", v+1, err)
		}
	}
	return nil
}

func migrateV1(tx *sql.Tx) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS snapshots (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id  TEXT NOT NULL,
			label       TEXT NOT NULL,
			timestamp   TEXT NOT NULL,
			root        TEXT NOT NULL,
			root_hash   TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_snapshots_session ON snapshots(session_id)`,
		`CREATE INDEX IF NOT EXISTS idx_snapshots_label   ON snapshots(label)`,
		`CREATE INDEX IF NOT EXISTS idx_snapshots_ts      ON snapshots(timestamp DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_snapshots_root    ON snapshots(root)`,

		`CREATE TABLE IF NOT EXISTS files (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			snapshot_id  INTEGER NOT NULL REFERENCES snapshots(id),
			path         TEXT NOT NULL,
			content_hash TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_files_snapshot ON files(snapshot_id)`,

		`CREATE TABLE IF NOT EXISTS symbols (
			id      INTEGER PRIMARY KEY AUTOINCREMENT,
			file_id INTEGER NOT NULL REFERENCES files(id),
			name    TEXT NOT NULL,
			kind    TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_symbols_file ON symbols(file_id)`,
		`CREATE INDEX IF NOT EXISTS idx_symbols_name ON symbols(name)`,
	}
	return execAll(tx, stmts)
}

// migrateV2 introduces the central-store repos registry. snapshots.repo_id is the
// stable identity spine: a repo keeps its id across path moves, and all reads scope
// by repo_id instead of the root path string. Existing rows get repo_id=NULL (the
// central store starts empty, so there are none in practice).
func migrateV2(tx *sql.Tx) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS repos (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			name         TEXT NOT NULL UNIQUE,
			path         TEXT NOT NULL UNIQUE,
			file_cap     INTEGER NOT NULL DEFAULT 0,
			enrolled_at  TEXT NOT NULL,
			last_indexed TEXT,
			parse_errors INTEGER NOT NULL DEFAULT 0
		)`,
		`ALTER TABLE snapshots ADD COLUMN repo_id INTEGER REFERENCES repos(id)`,
		`CREATE INDEX IF NOT EXISTS idx_snapshots_repo ON snapshots(repo_id)`,
	}
	return execAll(tx, stmts)
}

// migrateV3 splits repo.Path into two roles: the lookup key (path, unchanged,
// UNIQUE) and the source root for IR generation (source_root). Existing rows get
// source_root = path. Newly enrolled repos may differ (bare-repo worktrees where
// the enrolled path is a worktree dir but indexing should walk a different root).
func migrateV3(tx *sql.Tx) error {
	stmts := []string{
		`ALTER TABLE repos ADD COLUMN source_root TEXT`,
		`UPDATE repos SET source_root = path WHERE source_root IS NULL`,
	}
	return execAll(tx, stmts)
}

// migrateV4 adds common_dir — the git-common-dir of an enrolled repo, a stable
// identity shared by every worktree of that repo (bare or not). Lookup keyed on
// it resolves bare-repo claudew worktrees in O(1) without the worktree-list
// fallback. Existing rows get NULL (the column is populated lazily on enroll or
// on the guard's first resolution; pre-V4 repos fall back to the compat shim
// until then). The DB layer never shells to git, so there is no in-migration
// backfill.
func migrateV4(tx *sql.Tx) error {
	stmts := []string{
		`ALTER TABLE repos ADD COLUMN common_dir TEXT`,
		`CREATE INDEX IF NOT EXISTS idx_repos_common_dir ON repos(common_dir)`,
	}
	return execAll(tx, stmts)
}

// migrateV5 adds supported_seen — how many supported-extension files the last
// (re)index walk encountered, including those beyond the file cap. Together with
// the latest snapshot's file count this yields coverage % (the denominator the
// generator previously never reported). Existing rows get 0 = "not yet measured";
// the next reindex populates it.
func migrateV5(tx *sql.Tx) error {
	stmts := []string{
		`ALTER TABLE repos ADD COLUMN supported_seen INTEGER NOT NULL DEFAULT 0`,
	}
	return execAll(tx, stmts)
}

// migrateV6 adds the refs table — the bare function-call targets each snapshot
// file contains (IR v2). Deliberately a separate table, NOT symbols.kind='ref':
// refs are derived usage facts, not declared structure. Folding them into
// symbols would silently widen the guard's known-symbol set (loaders read all
// kinds) and pollute structural diffs with usage noise. Pre-V6 snapshots simply
// have no refs rows — "who calls X" answers from post-V6 snapshots only.
func migrateV6(tx *sql.Tx) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS refs (
			id      INTEGER PRIMARY KEY AUTOINCREMENT,
			file_id INTEGER NOT NULL REFERENCES files(id),
			name    TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_refs_file ON refs(file_id)`,
		`CREATE INDEX IF NOT EXISTS idx_refs_name ON refs(name)`,
	}
	return execAll(tx, stmts)
}

// migrateV7 enforces refs uniqueness per (file_id, name) at the schema level.
// Extraction dedupes upstream, but a constraint fails loudly if that ever
// loosens instead of bloating silently. Any pre-existing duplicates (none
// expected) are removed first; the plain file_id index is dropped because the
// unique index's prefix covers it.
func migrateV7(tx *sql.Tx) error {
	stmts := []string{
		`DELETE FROM refs WHERE id NOT IN (SELECT MIN(id) FROM refs GROUP BY file_id, name)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_refs_unique ON refs(file_id, name)`,
		`DROP INDEX IF EXISTS idx_refs_file`,
	}
	return execAll(tx, stmts)
}

// migrateV8 adds the per-symbol body hash. Existing rows get an empty sig_hash
// (the column default), which the diff treats as "no hash available" — those
// symbols fall back to add/remove only, never a false "modified". New snapshots
// written after this migration carry real hashes for AST-extracted symbols.
func migrateV8(tx *sql.Tx) error {
	stmts := []string{
		`ALTER TABLE symbols ADD COLUMN sig_hash TEXT NOT NULL DEFAULT ''`,
	}
	return execAll(tx, stmts)
}

func execAll(tx *sql.Tx, stmts []string) error {
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			return fmt.Errorf("exec %q: %w", s, err)
		}
	}
	return nil
}

// HealthInfo summarizes store-wide health (self-observing guarantee).
type HealthInfo struct {
	SchemaVersion int    `json:"schema_version"`
	Integrity     string `json:"integrity"` // "ok" or the failure detail
	RepoCount     int    `json:"repo_count"`
}

// Health reports schema version, integrity (live quick_check), and repo count.
func (db *DB) Health() (HealthInfo, error) {
	var h HealthInfo
	if err := db.conn.QueryRow("PRAGMA user_version").Scan(&h.SchemaVersion); err != nil {
		return h, fmt.Errorf("read user_version: %w", err)
	}
	var qc string
	if err := db.conn.QueryRow("PRAGMA quick_check").Scan(&qc); err != nil {
		return h, fmt.Errorf("quick_check: %w", err)
	}
	h.Integrity = qc
	if err := db.conn.QueryRow("SELECT COUNT(*) FROM repos").Scan(&h.RepoCount); err != nil {
		return h, fmt.Errorf("count repos: %w", err)
	}
	return h, nil
}

// CountSnapshots returns the number of snapshots stored for repoID.
func (db *DB) CountSnapshots(repoID int64) (int, error) {
	var n int
	if err := db.conn.QueryRow(`SELECT COUNT(*) FROM snapshots WHERE repo_id = ?`, repoID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count snapshots: %w", err)
	}
	return n, nil
}

// SymbolsForLatestSnapshot returns the set of symbol names recorded in the most
// recent snapshot for repoID. Returns an empty map (no error) when there are no
// snapshots yet.
func (db *DB) SymbolsForLatestSnapshot(repoID int64) (map[string]struct{}, error) {
	rows, err := db.conn.Query(`
		SELECT DISTINCT s.name
		FROM symbols s
		JOIN files f ON s.file_id = f.id
		WHERE f.snapshot_id = (
			SELECT id FROM snapshots WHERE repo_id = ? ORDER BY timestamp DESC, id DESC LIMIT 1
		)`, repoID)
	if err != nil {
		return nil, fmt.Errorf("symbols for latest snapshot: %w", err)
	}
	defer rows.Close()

	out := make(map[string]struct{})
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan symbol: %w", err)
		}
		out[name] = struct{}{}
	}
	return out, rows.Err()
}

// Close closes the underlying connection.
func (db *DB) Close() error {
	return db.conn.Close()
}
