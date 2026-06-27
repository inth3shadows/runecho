package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/inth3shadows/runecho/internal/ir"
)

// runBackup writes an atomic backup of the central store via VACUUM INTO.
func runBackup(args []string) int {
	dest := ""
	if len(args) > 0 {
		dest = args[0]
	}
	if dest == "" {
		dir, err := runechoDir()
		if err != nil {
			return printErr(err)
		}
		dest = filepath.Join(dir, "backups", "history-backup.db")
		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			return printErr(err)
		}
	}
	if _, err := os.Stat(dest); err == nil {
		return printErr(fmt.Errorf("backup destination already exists: %s (VACUUM INTO requires a new file)", dest))
	}
	db, code := mustOpenDB()
	if code != 0 {
		return code
	}
	defer db.Close()
	if err := db.BackupTo(dest); err != nil {
		return printErr(err)
	}
	fmt.Printf("Backup written: %s\n", dest)
	return 0
}

// buildIR generates a fresh IR for root (full, not incremental).
// fileCap limits the number of files indexed (0 = unlimited).
// Returns the IR, the walk's honest-coverage stats, and an exit code (0 = ok).
func buildIR(root string, fileCap int) (*ir.IR, ir.Stats, int) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, ir.Stats{}, printErr(err)
	}
	result, stats, err := ir.NewGenerator(ir.GeneratorConfig{
		IgnoredPaths: ir.DefaultIgnoredPaths,
		FileCap:      fileCap,
	}).Generate(abs)
	if err != nil {
		return nil, ir.Stats{}, printErr(fmt.Errorf("generate IR for %q: %w", abs, err))
	}
	return result, stats, 0
}

// coverageSuffix formats " coverage=N/M (P%)" from walk stats, or "" when the
// walk saw no supported files (nothing meaningful to report).
func coverageSuffix(stats ir.Stats) string {
	if stats.SupportedSeen == 0 {
		return ""
	}
	return fmt.Sprintf(" coverage=%d/%d (%.0f%%)", stats.Indexed, stats.SupportedSeen, stats.Coverage())
}

// runIndex is the original runecho-ir [root] behavior.
func runIndex(args []string) int {
	rootPath := "."
	if len(args) > 1 {
		if strings.HasPrefix(args[1], "-") {
			fmt.Fprintf(os.Stderr, "runecho-ir: unknown flag %q\n", args[1])
			printUsage()
			return ExitError
		}
		rootPath = args[1]
	}

	absRoot, err := filepath.Abs(rootPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to resolve path %q: %v\n", rootPath, err)
		return ExitError
	}

	irPath := filepath.Join(absRoot, ".ai", "ir.json")

	generator := ir.NewGenerator(ir.GeneratorConfig{IgnoredPaths: ir.DefaultIgnoredPaths})

	var result *ir.IR
	var stats ir.Stats
	var genErr error

	if _, err := os.Stat(irPath); err == nil {
		existing, loadErr := ir.Load(irPath)
		switch {
		case loadErr != nil:
			fmt.Fprintf(os.Stderr, "Warning: failed to load existing IR, regenerating: %v\n", loadErr)
			result, stats, genErr = generator.Generate(absRoot)
		case existing.Version != ir.IRVersion:
			// An old-format IR cannot be incrementally updated: Update reuses
			// unchanged files verbatim, which would leave fields added by newer
			// versions (e.g. v2 refs) empty forever.
			fmt.Fprintf(os.Stderr, "IR format v%d -> v%d: full regenerate\n", existing.Version, ir.IRVersion)
			result, stats, genErr = generator.Generate(absRoot)
		default:
			result, stats, genErr = generator.Update(existing, absRoot)
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(irPath), 0755); err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to create .ai directory: %v\n", err)
			return ExitError
		}
		result, stats, genErr = generator.Generate(absRoot)
	}

	err = genErr

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return ExitError
	}

	if err := result.Save(irPath); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to save IR: %v\n", err)
		return ExitError
	}

	shortHash := result.RootHash
	if len(shortHash) > 12 {
		shortHash = shortHash[:12]
	}
	fmt.Printf("Indexed %d files — root_hash: %s...%s\n", len(result.Files), shortHash, coverageSuffix(stats))
	return 0
}
