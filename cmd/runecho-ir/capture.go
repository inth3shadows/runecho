package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/inth3shadows/runecho/internal/ir"
)

// generateTimeoutEnv is the env var that overrides the IR-generation wall-clock
// deadline for the CLI. A Go duration ("5m", "90s") raises or lowers the ceiling;
// "0", "off", or "none" disables it for a one-shot index of a huge/slow-FS repo.
const generateTimeoutEnv = "RUNECHO_GENERATE_TIMEOUT"

// cliGenerateTimeout reads generateTimeoutEnv into an ir.GeneratorConfig value:
// unset → 0 (the package default applies); "0"/"off"/"none" → ir.Unbounded; a
// valid Go duration → that value. An unparseable value warns and falls through
// to the default rather than failing the command. Env parsing lives at the CLI
// boundary so the ir package stays free of process-environment coupling.
func cliGenerateTimeout() time.Duration {
	raw := strings.TrimSpace(os.Getenv(generateTimeoutEnv))
	if raw == "" {
		return 0
	}
	switch strings.ToLower(raw) {
	case "0", "off", "none":
		return ir.Unbounded
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		fmt.Fprintf(os.Stderr, "Warning: ignoring invalid %s=%q (want a Go duration like 5m, or off): %v\n", generateTimeoutEnv, raw, err)
		return 0
	}
	return d
}

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
		// 0700: the backup is a full copy of history.db (same repo paths and symbol
		// names); keep it owner-only, consistent with the central store dir.
		if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
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

// generateIR builds absRoot's IR, reusing the prior .ai/ir.json (if one
// exists and loads cleanly) via Generator.Update() instead of a full
// Generator.Generate() — Update() re-parses only files whose hash changed
// and reuses unchanged entries verbatim, and already falls back to Generate()
// itself on a nil or version-mismatched prior IR (see Generator.UpdateCtx), so
// that fallback isn't duplicated here. Shared by runIndex and buildIR so both
// the legacy `runecho-ir [root]` command and the central-store `repo add` /
// `repo reindex` path get the same incremental-reuse behavior (issue #92).
func generateIR(generator *ir.Generator, absRoot string) (*ir.IR, ir.Stats, error) {
	irPath := filepath.Join(absRoot, ".ai", "ir.json")
	if _, err := os.Stat(irPath); err == nil {
		existing, loadErr := ir.Load(irPath)
		if loadErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to load existing IR, regenerating: %v\n", loadErr)
			return generator.Generate(absRoot)
		}
		if existing.Version != ir.IRVersion {
			// An old-format IR cannot be incrementally updated: Update reuses
			// unchanged files verbatim, which would leave fields added by newer
			// versions (e.g. v2 refs) empty forever. Update() would already fall
			// back to Generate() here on its own, but the warning is worth
			// keeping visible to the caller.
			fmt.Fprintf(os.Stderr, "IR format v%d -> v%d: full regenerate\n", existing.Version, ir.IRVersion)
		}
		return generator.Update(existing, absRoot)
	}
	return generator.Generate(absRoot)
}

// buildIR builds root's IR, incrementally reusing the prior .ai/ir.json when
// one is already on disk (see generateIR) rather than always doing a full
// re-parse.
// fileCap limits the number of files indexed (0 = unlimited).
// Returns the IR, the walk's honest-coverage stats, and an exit code (0 = ok).
func buildIR(root string, fileCap int) (*ir.IR, ir.Stats, int) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, ir.Stats{}, printErr(err)
	}
	generator := ir.NewGenerator(ir.GeneratorConfig{
		IgnoredPaths:    ir.DefaultIgnoredPaths,
		FileCap:         fileCap,
		GenerateTimeout: cliGenerateTimeout(),
	})
	result, stats, err := generateIR(generator, abs)
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

	generator := ir.NewGenerator(ir.GeneratorConfig{IgnoredPaths: ir.DefaultIgnoredPaths, GenerateTimeout: cliGenerateTimeout()})

	result, stats, err := generateIR(generator, absRoot)
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
