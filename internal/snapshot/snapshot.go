package snapshot

import (
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/inth3shadows/runecho/internal/ir"
)

// SaveSnapshot persists a full IR snapshot in a single transaction, scoped to
// repoID (the stable repo identity). Returns the new snapshot ID.
func (db *DB) SaveSnapshot(repoID int64, sessionID, label, root string, irData *ir.IR) (int64, error) {
	tx, err := db.conn.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	ts := time.Now().UTC().Format(time.RFC3339)
	res, err := tx.Exec(
		`INSERT INTO snapshots (repo_id, session_id, label, timestamp, root, root_hash) VALUES (?, ?, ?, ?, ?, ?)`,
		repoID, sessionID, label, ts, root, irData.RootHash,
	)
	if err != nil {
		return 0, fmt.Errorf("insert snapshot: %w", err)
	}
	snapshotID, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("last insert id: %w", err)
	}

	// Sort paths for determinism.
	paths := make([]string, 0, len(irData.Files))
	for p := range irData.Files {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	// Prepare the per-row inserts once — thousands of rows go through these
	// per snapshot, and re-parsing the SQL per Exec is pure waste.
	fileStmt, err := tx.Prepare(`INSERT INTO files (snapshot_id, path, content_hash) VALUES (?, ?, ?)`)
	if err != nil {
		return 0, fmt.Errorf("prepare file insert: %w", err)
	}
	defer fileStmt.Close()
	symStmt, err := tx.Prepare(`INSERT INTO symbols (file_id, name, kind) VALUES (?, ?, ?)`)
	if err != nil {
		return 0, fmt.Errorf("prepare symbol insert: %w", err)
	}
	defer symStmt.Close()
	// OR IGNORE backstops the (file_id, name) unique index (schema V7); the
	// generator already dedupes per file.
	refStmt, err := tx.Prepare(`INSERT OR IGNORE INTO refs (file_id, name) VALUES (?, ?)`)
	if err != nil {
		return 0, fmt.Errorf("prepare ref insert: %w", err)
	}
	defer refStmt.Close()

	for _, path := range paths {
		file := irData.Files[path]
		fRes, err := fileStmt.Exec(snapshotID, path, file.Hash)
		if err != nil {
			return 0, fmt.Errorf("insert file %q: %w", path, err)
		}
		fileID, err := fRes.LastInsertId()
		if err != nil {
			return 0, fmt.Errorf("last insert id for file: %w", err)
		}

		// Insert symbols: functions, classes, exports, imports.
		type entry struct {
			name string
			kind string
		}
		var symbols []entry
		for _, name := range file.Functions {
			symbols = append(symbols, entry{name, "function"})
		}
		for _, name := range file.Classes {
			symbols = append(symbols, entry{name, "class"})
		}
		for _, name := range file.Exports {
			symbols = append(symbols, entry{name, "export"})
		}
		for _, name := range file.Imports {
			symbols = append(symbols, entry{name, "import"})
		}

		for _, sym := range symbols {
			if _, err := symStmt.Exec(fileID, sym.name, sym.kind); err != nil {
				return 0, fmt.Errorf("insert symbol %q: %w", sym.name, err)
			}
		}

		// Refs live in their own table (see migrateV6) so they never leak into
		// symbol loaders or structural diffs.
		for _, name := range file.Refs {
			if _, err := refStmt.Exec(fileID, name); err != nil {
				return 0, fmt.Errorf("insert ref %q: %w", name, err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return snapshotID, nil
}

// GetLatestByLabel returns the most recent snapshot for repoID with the given label.
// Returns nil, nil if no matching snapshot exists.
func (db *DB) GetLatestByLabel(repoID int64, label string) (*SnapshotMeta, error) {
	row := db.conn.QueryRow(
		`SELECT s.id, s.repo_id, s.session_id, s.label, s.timestamp, s.root, s.root_hash,
		        (SELECT COUNT(*) FROM files WHERE snapshot_id = s.id) AS file_count
		 FROM snapshots s
		 WHERE s.repo_id = ? AND s.label = ?
		 ORDER BY s.timestamp DESC, s.id DESC
		 LIMIT 1`,
		repoID, label,
	)
	return scanMeta(row)
}

// GetLatestByLabelSession returns the most recent snapshot for repoID with the
// given label AND session id. Returns nil, nil if no matching snapshot exists.
// Used by `diff --since=<label> --session=<id>` (CLI and MCP) to pin the
// reference point to a specific session instead of whatever session most
// recently used the label.
func (db *DB) GetLatestByLabelSession(repoID int64, label, sessionID string) (*SnapshotMeta, error) {
	row := db.conn.QueryRow(
		`SELECT s.id, s.repo_id, s.session_id, s.label, s.timestamp, s.root, s.root_hash,
		        (SELECT COUNT(*) FROM files WHERE snapshot_id = s.id) AS file_count
		 FROM snapshots s
		 WHERE s.repo_id = ? AND s.label = ? AND s.session_id = ?
		 ORDER BY s.timestamp DESC, s.id DESC
		 LIMIT 1`,
		repoID, label, sessionID,
	)
	return scanMeta(row)
}

// RefsToName returns the sorted file paths in snapshot snapshotID whose indexed
// refs include name — "who calls X" as of that snapshot. Empty for pre-V6
// snapshots (no refs rows were stored). The answer is deterministic: it derives
// from the stored IR, not from any live scan.
func (db *DB) RefsToName(snapshotID int64, name string) ([]string, error) {
	// No DISTINCT needed: (file_id, name) is unique by schema (V7).
	rows, err := db.conn.Query(
		`SELECT f.path FROM refs r
		 JOIN files f ON r.file_id = f.id
		 WHERE f.snapshot_id = ? AND r.name = ?
		 ORDER BY f.path`,
		snapshotID, name,
	)
	if err != nil {
		return nil, fmt.Errorf("refs to %q: %w", name, err)
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	return paths, rows.Err()
}

// GetByID returns a snapshot by its primary key.
func (db *DB) GetByID(id int64) (*SnapshotMeta, error) {
	row := db.conn.QueryRow(
		`SELECT s.id, s.repo_id, s.session_id, s.label, s.timestamp, s.root, s.root_hash,
		        (SELECT COUNT(*) FROM files WHERE snapshot_id = s.id) AS file_count
		 FROM snapshots s
		 WHERE s.id = ?`,
		id,
	)
	return scanMeta(row)
}

// List returns the n most recent snapshots for repoID (all labels).
func (db *DB) List(repoID int64, n int) ([]SnapshotMeta, error) {
	rows, err := db.conn.Query(
		`SELECT s.id, s.repo_id, s.session_id, s.label, s.timestamp, s.root, s.root_hash,
		        (SELECT COUNT(*) FROM files WHERE snapshot_id = s.id) AS file_count
		 FROM snapshots s
		 WHERE s.repo_id = ?
		 ORDER BY s.timestamp DESC, s.id DESC
		 LIMIT ?`,
		repoID, n,
	)
	if err != nil {
		return nil, fmt.Errorf("list snapshots: %w", err)
	}
	defer rows.Close()

	var metas []SnapshotMeta
	for rows.Next() {
		m, err := scanMetaRow(rows)
		if err != nil {
			return nil, err
		}
		metas = append(metas, *m)
	}
	return metas, rows.Err()
}

// loadFilesBySnapshot returns path→content_hash for all files in a snapshot.
func (db *DB) loadFilesBySnapshot(snapshotID int64) (map[string]string, error) {
	rows, err := db.conn.Query(
		`SELECT path, content_hash FROM files WHERE snapshot_id = ?`, snapshotID,
	)
	if err != nil {
		return nil, fmt.Errorf("load files: %w", err)
	}
	defer rows.Close()

	m := make(map[string]string)
	for rows.Next() {
		var path, hash string
		if err := rows.Scan(&path, &hash); err != nil {
			return nil, err
		}
		m[path] = hash
	}
	return m, rows.Err()
}

// loadSymbolsBySnapshot returns path→[]SymbolDelta for all symbols in a snapshot.
func (db *DB) loadSymbolsBySnapshot(snapshotID int64) (map[string][]SymbolDelta, error) {
	rows, err := db.conn.Query(
		`SELECT f.path, s.name, s.kind
		 FROM symbols s
		 JOIN files f ON f.id = s.file_id
		 WHERE f.snapshot_id = ?
		 ORDER BY f.path, s.kind, s.name`,
		snapshotID,
	)
	if err != nil {
		return nil, fmt.Errorf("load symbols: %w", err)
	}
	defer rows.Close()

	m := make(map[string][]SymbolDelta)
	for rows.Next() {
		var path, name, kind string
		if err := rows.Scan(&path, &name, &kind); err != nil {
			return nil, err
		}
		m[path] = append(m[path], SymbolDelta{Name: name, Kind: kind})
	}
	return m, rows.Err()
}

// scanMeta scans a single *sql.Row into a SnapshotMeta.
func scanMeta(row *sql.Row) (*SnapshotMeta, error) {
	m, err := scanMetaCols(row.Scan)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan snapshot: %w", err)
	}
	return m, nil
}

// scanMetaRow scans a *sql.Rows row into a SnapshotMeta.
func scanMetaRow(rows *sql.Rows) (*SnapshotMeta, error) {
	m, err := scanMetaCols(rows.Scan)
	if err != nil {
		return nil, fmt.Errorf("scan snapshot row: %w", err)
	}
	return m, nil
}

// scanMetaCols scans the standard snapshot column order (id, repo_id, session_id,
// label, timestamp, root, root_hash, file_count) via the given Scan closure.
// repo_id is nullable for legacy (pre-central-store) rows.
func scanMetaCols(scan func(...any) error) (*SnapshotMeta, error) {
	var m SnapshotMeta
	var tsStr string
	var repoID sql.NullInt64
	if err := scan(&m.ID, &repoID, &m.SessionID, &m.Label, &tsStr, &m.Root, &m.RootHash, &m.FileCount); err != nil {
		return nil, err
	}
	m.RepoID = repoID.Int64
	m.Timestamp, _ = time.Parse(time.RFC3339, tsStr)
	return &m, nil
}
