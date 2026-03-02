package snapshot

import (
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/inth3shadows/runecho/internal/ir"
)

// SaveSnapshot persists a full IR snapshot in a single transaction.
// Returns the new snapshot ID.
func (db *DB) SaveSnapshot(sessionID, label, root string, irData *ir.IR) (int64, error) {
	tx, err := db.conn.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	ts := time.Now().UTC().Format(time.RFC3339)
	res, err := tx.Exec(
		`INSERT INTO snapshots (session_id, label, timestamp, root, root_hash) VALUES (?, ?, ?, ?, ?)`,
		sessionID, label, ts, root, irData.RootHash,
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

	for _, path := range paths {
		file := irData.Files[path]
		fRes, err := tx.Exec(
			`INSERT INTO files (snapshot_id, path, content_hash) VALUES (?, ?, ?)`,
			snapshotID, path, file.Hash,
		)
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
			if _, err := tx.Exec(
				`INSERT INTO symbols (file_id, name, kind) VALUES (?, ?, ?)`,
				fileID, sym.name, sym.kind,
			); err != nil {
				return 0, fmt.Errorf("insert symbol %q: %w", sym.name, err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return snapshotID, nil
}

// GetLatestByLabel returns the most recent snapshot for root with the given label.
// Returns nil, nil if no matching snapshot exists.
func (db *DB) GetLatestByLabel(root, label string) (*SnapshotMeta, error) {
	row := db.conn.QueryRow(
		`SELECT s.id, s.session_id, s.label, s.timestamp, s.root, s.root_hash,
		        (SELECT COUNT(*) FROM files WHERE snapshot_id = s.id) AS file_count
		 FROM snapshots s
		 WHERE s.root = ? AND s.label = ?
		 ORDER BY s.timestamp DESC
		 LIMIT 1`,
		root, label,
	)
	return scanMeta(row)
}

// GetByID returns a snapshot by its primary key.
func (db *DB) GetByID(id int64) (*SnapshotMeta, error) {
	row := db.conn.QueryRow(
		`SELECT s.id, s.session_id, s.label, s.timestamp, s.root, s.root_hash,
		        (SELECT COUNT(*) FROM files WHERE snapshot_id = s.id) AS file_count
		 FROM snapshots s
		 WHERE s.id = ?`,
		id,
	)
	return scanMeta(row)
}

// List returns the n most recent snapshots for root (all labels).
func (db *DB) List(root string, n int) ([]SnapshotMeta, error) {
	rows, err := db.conn.Query(
		`SELECT s.id, s.session_id, s.label, s.timestamp, s.root, s.root_hash,
		        (SELECT COUNT(*) FROM files WHERE snapshot_id = s.id) AS file_count
		 FROM snapshots s
		 WHERE s.root = ?
		 ORDER BY s.timestamp DESC
		 LIMIT ?`,
		root, n,
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
	var m SnapshotMeta
	var tsStr string
	err := row.Scan(&m.ID, &m.SessionID, &m.Label, &tsStr, &m.Root, &m.RootHash, &m.FileCount)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan snapshot: %w", err)
	}
	m.Timestamp, _ = time.Parse(time.RFC3339, tsStr)
	return &m, nil
}

// scanMetaRow scans a *sql.Rows row into a SnapshotMeta.
func scanMetaRow(rows *sql.Rows) (*SnapshotMeta, error) {
	var m SnapshotMeta
	var tsStr string
	if err := rows.Scan(&m.ID, &m.SessionID, &m.Label, &tsStr, &m.Root, &m.RootHash, &m.FileCount); err != nil {
		return nil, fmt.Errorf("scan snapshot row: %w", err)
	}
	m.Timestamp, _ = time.Parse(time.RFC3339, tsStr)
	return &m, nil
}
