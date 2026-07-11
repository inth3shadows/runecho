package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/inth3shadows/runecho/internal/claims"
	"github.com/inth3shadows/runecho/internal/ir"
	"github.com/inth3shadows/runecho/internal/snapshot"
)

// runSnapshot saves a snapshot of the current ir.json.
func runSnapshot(args []string) int {
	fs := flag.NewFlagSet("snapshot", flag.ContinueOnError)
	label := fs.String("label", "manual", "snapshot label (e.g. session-start, session-end, manual)")
	sessionID := fs.String("session", "", "session ID")
	if code, ok := parseSub(fs, args); !ok {
		return code
	}

	root, code := resolveRoot(fs.Args())
	if code != 0 {
		return code
	}
	db, code := mustOpenDB()
	if code != 0 {
		return code
	}
	defer db.Close()

	// Resolve (auto-enrolling) first so the snapshot honors the repo's file cap —
	// otherwise an uncapped snapshot would never match a capped reindex of the same repo.
	repo, code := resolveRepoForWrite(db, root)
	if code != 0 {
		return code
	}
	irData, stats, code := buildIR(root, repo.FileCap) // always fresh: snapshot/diff/verify reflect current code, never a stale ir.json
	if code != 0 {
		return code
	}
	id, err := db.SaveSnapshot(repo.ID, *sessionID, *label, root, irData)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return ExitError
	}
	// Record the capture point (self-observing: last_indexed staleness).
	if err := db.TouchRepo(repo.ID, time.Now(), stats.ParseErrors, stats.SupportedSeen); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to record index time: %v\n", err)
	}
	shortHash := irData.RootHash
	if len(shortHash) > 12 {
		shortHash = shortHash[:12]
	}
	fmt.Printf("Snapshot saved: id=%d label=%s root_hash=%s... files=%d\n",
		id, *label, shortHash, len(irData.Files))
	return 0
}

// runDiff shows structural diff between two snapshots (or a snapshot vs live).
func runDiff(args []string) int {
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	since := fs.String("since", "", "diff since latest snapshot with this label vs live ir.json")
	sessionID := fs.String("session", "", "filter by session ID (used with --since)")
	compact := fs.Bool("compact", false, "single-line compact output")
	asJSON := fs.Bool("json", false, "machine-readable JSON (parity with the MCP diff tool)")
	if code, ok := parseSub(fs, args); !ok {
		return code
	}

	// Distinguish an explicit `--since=""` from the flag being absent. Snapshots
	// may legitimately carry an empty label (only "auto" is reserved by
	// SaveSnapshot), so keying the mode on `*since != ""` made an empty-label
	// snapshot permanently unreachable — the empty-string flag fell through to the
	// two-ID positional mode. fs.Visit reports only the flags actually set.
	sinceProvided := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "since" {
			sinceProvided = true
		}
	})

	db, code := mustOpenDB()
	if code != 0 {
		return code
	}
	defer db.Close()

	var result snapshot.DiffResult

	if sinceProvided {
		// --since mode: A = last snapshot by label, B = live ir.json. root is
		// resolved here, not at the top: in two-ID mode the leading positional
		// is a snapshot id, not a path, so resolving a root there is meaningless.
		root, code := resolveRoot(fs.Args())
		if code != 0 {
			return code
		}
		repoID := lookupRepoID(db, root)
		if repoID < 0 {
			fmt.Fprintf(os.Stderr, "Repo %q is not enrolled — run: runecho-ir repo add .\n", root)
			return ExitNoData
		}
		var meta *snapshot.SnapshotMeta
		var err error
		if *sessionID != "" {
			meta, err = db.GetLatestByLabelSession(repoID, *since, *sessionID)
		} else {
			meta, err = db.GetLatestByLabel(repoID, *since)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return ExitError
		}
		if meta == nil {
			suffix := ""
			if *sessionID != "" {
				suffix = fmt.Sprintf(" (session %q)", *sessionID)
			}
			fmt.Fprintf(os.Stderr, "No snapshot found with label %q%s for root %q\n", *since, suffix, root)
			return ExitNoData
		}
		irData, _, irCode := buildIR(root, repoFileCap(db, root)) // always fresh: snapshot/diff/verify reflect current code, never a stale ir.json
		if irCode != 0 {
			return irCode
		}
		var diffErr error
		result, diffErr = db.DiffLive(*meta, irData)
		if diffErr != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", diffErr)
			return ExitError
		}
	} else {
		// Two positional ID mode.
		positional := fs.Args()
		if len(positional) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: runecho-ir diff --since=<label> [root]")
			fmt.Fprintln(os.Stderr, "       runecho-ir diff <id-a> <id-b> [root]")
			return ExitError
		}
		idA, err := strconv.ParseInt(positional[0], 10, 64)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Invalid snapshot ID %q\n", positional[0])
			return ExitError
		}
		idB, err := strconv.ParseInt(positional[1], 10, 64)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Invalid snapshot ID %q\n", positional[1])
			return ExitError
		}
		metaA, err := db.GetByID(idA)
		if err != nil || metaA == nil {
			fmt.Fprintf(os.Stderr, "Snapshot %d not found\n", idA)
			return ExitError
		}
		metaB, err := db.GetByID(idB)
		if err != nil || metaB == nil {
			fmt.Fprintf(os.Stderr, "Snapshot %d not found\n", idB)
			return ExitError
		}
		// A diff must never cross repo boundaries (parity with the MCP oracle's
		// scopedSnapshot). RepoID 0 means an unowned/legacy snapshot — refuse it.
		if metaA.RepoID == 0 || metaA.RepoID != metaB.RepoID {
			fmt.Fprintf(os.Stderr, "Refusing cross-repo diff: snapshots %d and %d are not in the same enrolled repo\n", idA, idB)
			return ExitError
		}
		result, err = db.Diff(*metaA, *metaB)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return ExitError
		}
	}

	switch {
	case *asJSON:
		// Same shape as the MCP `diff` oracle tool (snapshot.DiffPayload), so a
		// machine consumer like the harness gate parses one stable contract.
		out, err := json.MarshalIndent(snapshot.DiffPayload(result), "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return ExitError
		}
		fmt.Println(string(out))
	case *compact:
		line := snapshot.FormatCompact(result)
		if line != "" {
			fmt.Println(line)
		}
	default:
		fmt.Print(snapshot.FormatFull(result))
	}
	return 0
}

// runLog prints a table of recent snapshots.
func runLog(args []string) int {
	fs := flag.NewFlagSet("log", flag.ContinueOnError)
	n := fs.Int("n", 10, "number of snapshots to show")
	if code, ok := parseSub(fs, args); !ok {
		return code
	}

	root, code := resolveRoot(fs.Args())
	if code != 0 {
		return code
	}
	db, dbCode := mustOpenDB()
	if dbCode != 0 {
		return dbCode
	}
	defer db.Close()

	repoID := lookupRepoID(db, root)
	if repoID < 0 {
		fmt.Fprintf(os.Stderr, "Repo %q is not enrolled — run: runecho-ir repo add .\n", root)
		return ExitNoData
	}
	metas, err := db.List(repoID, *n)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return ExitError
	}
	if len(metas) == 0 {
		fmt.Println("No snapshots found.")
		return ExitNoData
	}

	fmt.Printf("%-5s  %-15s  %-25s  %-10s  %-8s  %s\n",
		"ID", "LABEL", "SESSION", "TIMESTAMP", "FILES", "HASH")
	fmt.Println(strings.Repeat("-", 90))
	for _, m := range metas {
		shortHash := m.RootHash
		if len(shortHash) > 8 {
			shortHash = shortHash[:8]
		}
		// Date portion only; guard the slice like session above (a zero or
		// malformed Timestamp must not panic the listing).
		ts := m.Timestamp.Format(time.RFC3339)
		if len(ts) > 10 {
			ts = ts[:10]
		}
		session := m.SessionID
		if len(session) > 25 {
			session = session[:22] + "..."
		}
		fmt.Printf("%-5d  %-15s  %-25s  %-10s  %-8d  %s...\n",
			m.ID, m.Label, session, ts, m.FileCount, shortHash)
	}
	return 0
}

// runVerify diffs the most recent session-start snapshot against live ir.json.
func runVerify(args []string) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	sessionID := fs.String("session", "", "session ID to verify (optional)")
	if code, ok := parseSub(fs, args); !ok {
		return code
	}

	root, code := resolveRoot(fs.Args())
	if code != 0 {
		return code
	}
	db, dbCode := mustOpenDB()
	if dbCode != 0 {
		return dbCode
	}
	defer db.Close()

	repoID := lookupRepoID(db, root)
	if repoID < 0 {
		fmt.Fprintf(os.Stderr, "Repo %q is not enrolled — run: runecho-ir repo add .\n", root)
		return ExitNoData
	}

	var meta *snapshot.SnapshotMeta
	var err error

	if *sessionID != "" {
		// Direct SQL lookup — a List(100) scan silently missed the snapshot when
		// more than 100 newer snapshots existed.
		meta, err = db.GetLatestByLabelSession(repoID, "session-start", *sessionID)
	} else {
		meta, err = db.GetLatestByLabel(repoID, "session-start")
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return ExitError
	}

	if meta == nil {
		fmt.Println("No session-start snapshot found.")
		fmt.Println("Run: runecho-ir snapshot --label=session-start")
		return ExitNoData
	}

	irData, _, irCode := buildIR(root, repoFileCap(db, root)) // always fresh: snapshot/diff/verify reflect current code, never a stale ir.json
	if irCode != 0 {
		return irCode
	}
	result, err := db.DiffLive(*meta, irData)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return ExitError
	}

	fmt.Printf("Verifying against snapshot id=%d label=%s session=%s ts=%s\n\n",
		meta.ID, meta.Label, meta.SessionID, meta.Timestamp.Format(time.RFC3339))
	fmt.Print(snapshot.FormatFull(result))
	return 0
}

// resolveRoot returns the absolute project root from optional positional args,
// and an exit code (0 = ok, 1 = error already printed).
func resolveRoot(args []string) (string, int) {
	rootPath := "."
	if len(args) > 0 {
		if strings.HasPrefix(args[0], "-") {
			fmt.Fprintf(os.Stderr, "runecho-ir: unexpected flag %q where root path was expected\n", args[0])
			return "", ExitError
		}
		rootPath = args[0]
	}
	abs, err := filepath.Abs(rootPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to resolve root %q: %v\n", rootPath, err)
		return "", ExitError
	}
	return abs, 0
}

// runChurn reports file and symbol churn rate across recent snapshots.
func runChurn(args []string) int {
	fs := flag.NewFlagSet("churn", flag.ContinueOnError)
	n := fs.Int("n", 20, "number of snapshots to analyze")
	minChanges := fs.Int("min-changes", 2, "minimum diffs a file/symbol must appear in to be considered hot")
	compact := fs.Bool("compact", false, "single-line compact output")
	asJSON := fs.Bool("json", false, "machine-readable JSON (parity with diff --json)")
	if code, ok := parseSub(fs, args); !ok {
		return code
	}

	root, code := resolveRoot(fs.Args())
	if code != 0 {
		return code
	}
	db, dbCode := mustOpenDB()
	if dbCode != 0 {
		return dbCode
	}
	defer db.Close()

	repoID := lookupRepoID(db, root)
	if repoID < 0 {
		fmt.Fprintf(os.Stderr, "Repo %q is not enrolled — run: runecho-ir repo add .\n", root)
		return ExitNoData
	}
	report, err := db.Churn(repoID, *n)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return ExitError
	}

	switch {
	case *asJSON:
		out, err := json.MarshalIndent(snapshot.ChurnPayload(report, *minChanges), "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return ExitError
		}
		fmt.Println(string(out))
	case *compact:
		fmt.Println(snapshot.FormatChurnCompact(report))
	default:
		fmt.Print(snapshot.FormatChurn(report, *minChanges))
	}
	return 0
}

// runValidateClaims extracts code symbol references from a text file and
// cross-checks them against the IR. Reports identifiers referenced but not
// found in the IR (potential hallucinations).
func runValidateClaims(args []string) int {
	fs := flag.NewFlagSet("validate-claims", flag.ContinueOnError)
	textFile := fs.String("text", "", "path to text file containing assistant message")
	irPath := fs.String("ir", ".ai/ir.json", "path to ir.json")
	if code, ok := parseSub(fs, args); !ok {
		return code
	}

	if *textFile == "" {
		fmt.Fprintln(os.Stderr, "Error: --text=<file> required")
		return ExitError
	}

	// Load text.
	textData, err := os.ReadFile(*textFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot read text file %q: %v\n", *textFile, err)
		return ExitError
	}
	text := string(textData)

	// Load IR symbols.
	irData, err := ir.Load(*irPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot load IR %q: %v\n", *irPath, err)
		return ExitError
	}
	// A claim-existence check is a named lookup, so include EVERY indexed kind —
	// not just function/class. Parity with the MCP `locate` tool: a name bound only
	// under an internal kind (import_name, export, export_wildcard) genuinely exists
	// in the code, so restricting to func+class would flag a real imported/exported
	// name (e.g. `readFileSync`) as an invented reference. claims.KnownSet completes
	// that parity on the other axis — it also accepts a qualified symbol by its bare
	// last segment, so a method claimed by its natural name (`fetchData`) resolves
	// against `Reader.fetchData`. Widening the known set only ever suppresses false
	// positives — the safe direction for this check.
	var names []string
	for _, fileEntry := range irData.Files {
		for _, s := range fileEntry.Symbols {
			names = append(names, s.Name)
		}
	}
	knownSymbols := claims.KnownSet(names)

	// Extract symbol references from text.
	refs := claims.ExtractSymbolRefs(text)

	type Mismatch struct {
		Ref     string `json:"ref"`
		Context string `json:"context"`
	}
	var mismatches []Mismatch
	for ref, ctx := range refs {
		if !knownSymbols[ref] {
			mismatches = append(mismatches, Mismatch{Ref: ref, Context: ctx})
		}
	}
	// Stable output: refs is a map, so without an explicit sort the order is
	// non-deterministic — unacceptable in a tool whose contract is determinism.
	sort.Slice(mismatches, func(i, j int) bool { return mismatches[i].Ref < mismatches[j].Ref })

	out := map[string]interface{}{
		"checked":    len(refs),
		"mismatches": mismatches,
	}
	if mismatches == nil {
		out["mismatches"] = []Mismatch{}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(out)
	if len(mismatches) > 0 {
		return ExitNoData
	}
	return ExitOK
}

// runTruthTrail fuses diff, callers, churn, and stale-claims into one change receipt.
// Exits ExitNoData (1) when --text finds stale claims, not enrolled, or no baseline snapshot.
// Exits ExitError (2) on I/O or database failure.
func runTruthTrail(args []string) int {
	fs := flag.NewFlagSet("truth-trail", flag.ContinueOnError)
	since := fs.String("since", "session-start", "baseline label (diff since latest snapshot with this label vs live code)")
	sessionID := fs.String("session", "", "filter by session ID (used with --since)")
	textFile := fs.String("text", "", "path to prose file to check for stale symbol refs")
	if code, ok := parseSub(fs, args); !ok {
		return code
	}

	root, code := resolveRoot(fs.Args())
	if code != 0 {
		return code
	}
	db, code := mustOpenDB()
	if code != 0 {
		return code
	}
	defer db.Close()

	repoID := lookupRepoID(db, root)
	if repoID < 0 {
		fmt.Fprintf(os.Stderr, "Repo %q is not enrolled — run: runecho-ir repo add .\n", root)
		return ExitNoData
	}

	var meta *snapshot.SnapshotMeta
	var err error
	if *sessionID != "" {
		meta, err = db.GetLatestByLabelSession(repoID, *since, *sessionID)
	} else {
		meta, err = db.GetLatestByLabel(repoID, *since)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return ExitError
	}
	if meta == nil {
		suffix := ""
		if *sessionID != "" {
			suffix = fmt.Sprintf(" (session %q)", *sessionID)
		}
		fmt.Fprintf(os.Stderr, "No snapshot found with label %q%s\n", *since, suffix)
		fmt.Fprintf(os.Stderr, "Run: runecho-ir snapshot --label=%s\n", *since)
		return ExitNoData
	}

	liveIR, _, irCode := buildIR(root, repoFileCap(db, root))
	if irCode != 0 {
		return irCode
	}

	text := ""
	if *textFile != "" {
		data, err := os.ReadFile(*textFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: cannot read text file %q: %v\n", *textFile, err)
			return ExitError
		}
		text = string(data)
	}

	trail, err := snapshot.TruthTrail(db, repoID, *meta, liveIR, 0, text)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return ExitError
	}

	fmt.Print(snapshot.FormatTrail(trail))

	if len(trail.StaleClaims) > 0 {
		return ExitNoData
	}
	return ExitOK
}
