package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/inth3shadows/runecho/internal/gitutil"
	"github.com/inth3shadows/runecho/internal/snapshot"
	"github.com/inth3shadows/runecho/internal/store"
)

// runechoDir is the package-local alias to the shared store helper.
func runechoDir() (string, error) { return store.RunechoDir() }

// mustOpenDB opens the central snapshot store (~/.runecho/history.db) or returns 1.
// History is centralized so the oracle serves all enrolled repos from one
// durable, integrity-checked store; the working ir.json stays repo-local.
func mustOpenDB() (*snapshot.DB, int) {
	dir, err := runechoDir()
	if err != nil {
		return nil, printErr(err)
	}
	// 0700: the central store aggregates paths, filenames, and symbol names across
	// every enrolled repo; keep other local users out of it on a shared host. The
	// dir mode gates traversal, so it also covers history.db and its WAL/SHM
	// sidecars without per-file chmod. (Only newly-created dirs are affected.)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, printErr(fmt.Errorf("create %s: %w", dir, err))
	}
	db, err := snapshot.Open(filepath.Join(dir, "history.db"))
	if err != nil {
		return nil, printErr(fmt.Errorf("open snapshot DB: %w", err))
	}
	return db, 0
}

// resolveRepoForWrite returns the enrolled repo for root, auto-enrolling on first
// write (snapshot). 3-tier resolution (common-dir → top-level → worktree shim)
// finds an already-enrolled repo from any worktree, preventing duplicate
// enrollments. When truly new, enroll at the git top-level path (canonical).
// Returning the full repo lets callers apply its FileCap when generating IR.
func resolveRepoForWrite(db *snapshot.DB, root string) (*snapshot.Repo, int) {
	if repo, _, ok := db.ResolveRepo(root); ok {
		return repo, 0
	}
	// Not enrolled — auto-enroll. Use git top-level as the canonical path so
	// worktrees of the same repo always enroll at the same location.
	enrollPath := root
	if topLevel, err := gitutil.TopLevel(root); err == nil {
		enrollPath = topLevel
	}
	uname, uErr := snapshot.UniqueName(db, snapshot.DeriveRepoName(enrollPath))
	if uErr != nil {
		return nil, printErr(uErr)
	}
	if _, err := db.EnrollRepo(uname, enrollPath, enrollPath, 0); err != nil {
		return nil, printErr(err)
	}
	repo, err := db.GetRepoByPath(enrollPath)
	if err != nil {
		return nil, printErr(err)
	}
	// Record the git-common-dir for O(1) cross-worktree lookup (schema V4).
	if repo != nil {
		if cd, cdErr := gitutil.CommonDir(enrollPath); cdErr == nil {
			_ = db.SetRepoCommonDir(repo.ID, cd)
		}
	}
	return repo, 0
}

// repoFileCap returns the enrolled repo's file cap for root, or 0 (unlimited) if
// not enrolled. 3-tier resolution finds the repo from any worktree/cwd so the
// cap matches the cap used when the baseline snapshot was stored.
func repoFileCap(db *snapshot.DB, root string) int {
	repo, _, ok := db.ResolveRepo(root)
	if !ok {
		return 0
	}
	return repo.FileCap
}

// lookupRepoID returns the repo_id for the enrolled repo containing root, or -1
// if none. Uses 3-tier resolution so linked worktrees of the same repo resolve
// to the same repo_id. Read commands treat -1 as "no history for this repo".
func lookupRepoID(db *snapshot.DB, root string) int64 {
	repo, _, ok := db.ResolveRepo(root)
	if !ok {
		return -1
	}
	return repo.ID
}

// printErr writes "Error: <err>" to stderr and returns ExitError.
// It replaces the old fatal() helper: instead of calling os.Exit directly,
// callers return the code so main() (and tests) control process exit.
func printErr(err error) int {
	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
	return ExitError
}
