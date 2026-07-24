package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/inth3shadows/runecho/internal/version"
)

// Exit codes returned by every runecho-ir subcommand.
const (
	ExitOK     = 0 // clean run — success or no notable findings
	ExitNoData = 1 // soft condition: not enrolled, no matching snapshot, stale claims found
	ExitError  = 2 // hard error: bad args, I/O failure, database error
)

// Usage: runecho-ir [root-path]
// Generates .ai/ir.json for the project at root-path (default: current directory).
// If .ai/ir.json already exists, performs incremental update (only re-parses changed files).
//
// Subcommands:
//
//	runecho-ir snapshot [--label=manual] [--session=""] [root]
//	runecho-ir diff [--since=label | id-a id-b] [--compact] [root]
//	runecho-ir log [--n=10] [root]
//	runecho-ir verify [--session=""] [root]
//	runecho-ir churn [--n=20] [--min-changes=2] [--compact] [--json] [root]
//	runecho-ir guard-stats [--days=30] [--top=10] [--json]
//	runecho-ir fpreport [--days=30] [--top=15] [--gv=V] [--json] [--max-rate=F]
//	runecho-ir truth-trail [--since=session-start] [--session=<id>] [--text=<file>] [root]
//	runecho-ir validate-claims --text=<file> [--ir=<path>]
//	runecho-ir contract list|show|activate|deactivate|check
func main() {
	os.Exit(run())
}

// parseSub parses a subcommand's flag set with ContinueOnError semantics so a bad
// flag never calls os.Exit behind the testable run() seam. Returns (code, ok):
// ok=true means parsing succeeded; ok=false means the caller should return code
// (ExitOK for -h/--help, ExitError for a malformed flag — the flag package has
// already printed usage/the error to stderr).
func parseSub(fs *flag.FlagSet, args []string) (int, bool) {
	switch err := fs.Parse(args); err {
	case nil:
		return 0, true
	case flag.ErrHelp:
		return ExitOK, false
	default:
		return ExitError, false
	}
}

// run is the testable entry point. All subcommand handlers return an int exit
// code; main() is the only caller of os.Exit. This mirrors the runecho-guard
// seam (run() int / main() { os.Exit(run()) }) so both commands are testable
// without subprocess overhead.
func run() int {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "snapshot":
			return runSnapshot(os.Args[2:])
		case "diff":
			return runDiff(os.Args[2:])
		case "map":
			return runMap(os.Args[2:])
		case "log":
			return runLog(os.Args[2:])
		case "verify":
			return runVerify(os.Args[2:])
		case "churn":
			return runChurn(os.Args[2:])
		case "guard-stats":
			return runGuardStats(os.Args[2:])
		case "fpreport":
			return runFPReport(os.Args[2:])
		case "repo":
			return runRepo(os.Args[2:])
		case "backup":
			return runBackup(os.Args[2:])
		case "install":
			return runInstall(os.Args[2:])
		case "version-check":
			return runVersionCheck(os.Args[2:])
		case "truth-trail":
			return runTruthTrail(os.Args[2:])
		case "validate-claims":
			return runValidateClaims(os.Args[2:])
		case "contract":
			return runContract(os.Args[2:])
		case "--help", "-h", "help":
			printUsage()
			return 0
		case "--version", "-v":
			fmt.Println("runecho-ir " + version.Version)
			return 0
		default:
			if strings.HasPrefix(os.Args[1], "-") {
				fmt.Fprintf(os.Stderr, "runecho-ir: unknown flag %q\n", os.Args[1])
				printUsage()
				return ExitError
			}
		}
	}
	// Default: index behavior (backward compat).
	return runIndex(os.Args)
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "Usage: runecho-ir [root-path]")
	fmt.Fprintln(os.Stderr, "       runecho-ir snapshot [--label=manual] [--session=<id>] [root]")
	fmt.Fprintln(os.Stderr, "       runecho-ir diff [--since=<label>] [--compact] [--json] [root]")
	fmt.Fprintln(os.Stderr, "       runecho-ir map [--by-file] [--kind=func|class|export|import] [--dir=<p>] [--since=<label>] [--compact] [--json] [root]")
	fmt.Fprintln(os.Stderr, "       runecho-ir log [--n=10] [root]")
	fmt.Fprintln(os.Stderr, "       runecho-ir verify [--session=<id>] [root]")
	fmt.Fprintln(os.Stderr, "       runecho-ir churn [--n=20] [--min-changes=2] [--compact] [--json] [root]")
	fmt.Fprintln(os.Stderr, "       runecho-ir guard-stats [--days=30] [--top=10] [--json]")
	fmt.Fprintln(os.Stderr, "       runecho-ir fpreport [--days=30] [--top=15] [--gv=V] [--json] [--max-rate=F]")
	fmt.Fprintln(os.Stderr, "       runecho-ir repo add <path> [--name=<n>] [--cap=<N>] [--source-root=<path>] [--no-hooks]")
	fmt.Fprintln(os.Stderr, "       runecho-ir repo list | rm <name> | reindex <name|.> [--all]")
	fmt.Fprintln(os.Stderr, "       runecho-ir install [--periodic] [--force] [root]")
	fmt.Fprintln(os.Stderr, "       runecho-ir version-check [--reinstall] [--quiet] [root]")
	fmt.Fprintln(os.Stderr, "       runecho-ir backup [dest.db]")
	fmt.Fprintln(os.Stderr, "       runecho-ir truth-trail [--since=session-start] [--session=<id>] [--text=<file>] [root]")
	fmt.Fprintln(os.Stderr, "       runecho-ir validate-claims --text=<file> [--ir=<path>]")
	fmt.Fprintln(os.Stderr, "       runecho-ir contract list | show <name> | activate --session=<id> <name> | deactivate --session=<id>")
	fmt.Fprintln(os.Stderr, "       runecho-ir contract check [--contract=<name>|--session=<id>] [--base=<ref>] [--dir=<p>]")
}
