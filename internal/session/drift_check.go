package session

import (
	"fmt"
	"path/filepath"

	"github.com/inth3shadows/runecho/internal/snapshot"
	"github.com/inth3shadows/runecho/internal/task"
)

// RunDriftCheck loads the two most recent snapshots from history.db, diffs them
// to obtain changed file paths, then intersects those paths with active task scopes.
// Matching entries are appended to .ai/faults.jsonl.
//
// This function is safe to call even when no snapshots exist — it returns nil in that case.
func RunDriftCheck(root, sessionID string) error {
	dbPath := filepath.Join(root, ".ai", "history.db")
	db, err := snapshot.Open(dbPath)
	if err != nil {
		return fmt.Errorf("drift check: open snapshot db: %w", err)
	}
	defer db.Close()

	// Get the two most recent snapshots.
	metas, err := db.List(root, 2)
	if err != nil {
		return fmt.Errorf("drift check: list snapshots: %w", err)
	}
	if len(metas) < 2 {
		// Not enough history to diff — skip silently.
		return nil
	}

	// metas[0] is newer, metas[1] is older (List returns DESC order).
	newer := metas[0]
	older := metas[1]

	diff, err := db.Diff(older, newer)
	if err != nil {
		return fmt.Errorf("drift check: diff snapshots %d→%d: %w", older.ID, newer.ID, err)
	}

	if len(diff.Files) == 0 {
		return nil
	}

	// Collect changed file paths.
	changedFiles := make([]string, 0, len(diff.Files))
	for _, fd := range diff.Files {
		changedFiles = append(changedFiles, fd.Path)
	}

	// Load tasks.
	taskDB, err := task.Load(root)
	if err != nil {
		return fmt.Errorf("drift check: load tasks: %w", err)
	}

	// Intersect changed files with task scopes.
	entries := task.CheckDrift(taskDB.Tasks, changedFiles)
	if len(entries) == 0 {
		return nil
	}

	// Append faults (idempotent by task_id + session_id).
	if err := task.AppendDriftFaults(root, entries, sessionID); err != nil {
		return fmt.Errorf("drift check: append faults: %w", err)
	}

	return nil
}
