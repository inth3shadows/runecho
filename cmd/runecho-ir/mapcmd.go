package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/inth3shadows/runecho/internal/ir"
	"github.com/inth3shadows/runecho/internal/snapshot"
)

// mapSym is one symbol's location for the repo map: a deterministic projection
// of FileIR (name, kind, file, 1-based line, short body hash). No prose, no
// ranking — just where each indexed symbol lives.
type mapSym struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // function | class | export | import
	File string `json:"file"`
	Line int    `json:"line"` // 1-based; 0 = unknown (parser had no span / pre-v4 index)
	Hash string `json:"hash,omitempty"`
}

var kindAbbrev = map[string]string{
	"function": "fn",
	"class":    "cls",
	"export":   "exp",
	"import":   "imp",
}

// normalizeKind maps user-facing --kind values to internal kind names.
func normalizeKind(k string) (string, bool) {
	switch k {
	case "", "func", "function":
		if k == "" {
			return "", true
		}
		return "function", true
	case "class", "cls":
		return "class", true
	case "export", "exp":
		return "export", true
	case "import", "imp":
		return "import", true
	default:
		return "", false
	}
}

// runMap renders a deterministic symbol → location map from the live IR. With
// --since it scopes to symbols added or modified versus the named snapshot — a
// "map of what changed", reusing the existing diff machinery.
func runMap(args []string) int {
	fs := flag.NewFlagSet("map", flag.ExitOnError)
	byFile := fs.Bool("by-file", false, "group symbols under their file instead of a flat symbol index")
	kindFlag := fs.String("kind", "", "restrict to one kind: func|class|export|import (default: func+class)")
	dirPrefix := fs.String("dir", "", "only files under this path prefix")
	since := fs.String("since", "", "only symbols changed since the latest snapshot with this label")
	sessionID := fs.String("session", "", "filter --since by session ID")
	compact := fs.Bool("compact", false, "terser output (omit the hash column)")
	header := fs.Bool("header", false, "print a <200-token repo summary for session-start injection, not the full map")
	asJSON := fs.Bool("json", false, "machine-readable JSON")
	fs.Parse(args)

	kind, ok := normalizeKind(*kindFlag)
	if !ok {
		fmt.Fprintf(os.Stderr, "Invalid --kind %q (want func|class|export|import)\n", *kindFlag)
		return ExitError
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

	// Always build a fresh IR: the map reflects current code, never a stale
	// ir.json. fileCap honors the enrolled repo's cap (0 if not enrolled).
	irData, _, irCode := buildIR(root, repoFileCap(db, root))
	if irCode != 0 {
		return irCode
	}

	// --header: a tiny deterministic summary for session-start injection. Bypasses
	// the per-symbol rendering entirely — the point is to NOT spend context on the
	// full map up front, just to tell the agent the map exists and how to query it.
	if *header {
		emitMapHeader(irData)
		return 0
	}

	// --since: compute the changed-symbol set (added ∪ modified) from a diff.
	var changed map[string]map[string]bool
	if *since != "" {
		set, code := changedSymbols(db, root, *since, *sessionID, irData)
		if code != 0 {
			return code
		}
		changed = set
	}

	syms := collectMapSymbols(irData, kind, *dirPrefix, changed)

	if *asJSON {
		return emitMapJSON(root, syms, *byFile, *since != "")
	}
	if *byFile {
		emitMapByFile(syms, *compact)
	} else {
		emitMapBySymbol(syms, *compact)
	}
	return 0
}

// changedSymbols resolves the --since baseline and returns file → "kind:name" →
// true for every symbol the live IR added or modified versus that snapshot.
func changedSymbols(db *snapshot.DB, root, since, sessionID string, irData *ir.IR) (map[string]map[string]bool, int) {
	repoID := lookupRepoID(db, root)
	if repoID < 0 {
		fmt.Fprintf(os.Stderr, "Repo %q is not enrolled — --since needs snapshots. Run: runecho-ir repo add .\n", root)
		return nil, ExitNoData
	}
	var meta *snapshot.SnapshotMeta
	var err error
	if sessionID != "" {
		meta, err = db.GetLatestByLabelSession(repoID, since, sessionID)
	} else {
		meta, err = db.GetLatestByLabel(repoID, since)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return nil, ExitError
	}
	if meta == nil {
		fmt.Fprintf(os.Stderr, "No snapshot found with label %q for root %q\n", since, root)
		return nil, ExitNoData
	}
	res, err := db.DiffLive(*meta, irData)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return nil, ExitError
	}
	changed := make(map[string]map[string]bool)
	mark := func(path string, syms []snapshot.SymbolDelta) {
		if changed[path] == nil {
			changed[path] = make(map[string]bool)
		}
		for _, s := range syms {
			changed[path][s.Kind+":"+s.Name] = true
		}
	}
	for _, fd := range res.Files {
		mark(fd.Path, fd.Added)
		mark(fd.Path, fd.Modified)
	}
	return changed, 0
}

// collectMapSymbols projects the IR into a sorted []mapSym (via the shared
// ir.SymbolLocations), applying the kind, dir, and changed-set filters. Default
// kinds are function+class (the navigable definitions); export/import are
// included only when explicitly requested via --kind.
func collectMapSymbols(irData *ir.IR, kind, dirPrefix string, changed map[string]map[string]bool) []mapSym {
	keep := func(k string) bool {
		if kind != "" {
			return k == kind
		}
		return k == "function" || k == "class"
	}

	var syms []mapSym
	for _, s := range irData.SymbolLocations() { // already deterministically sorted
		if !keep(s.Kind) {
			continue
		}
		if dirPrefix != "" && !strings.HasPrefix(s.File, dirPrefix) {
			continue
		}
		if changed != nil && !changed[s.File][s.Kind+":"+s.Name] {
			continue
		}
		syms = append(syms, mapSym{
			Name: s.Name,
			Kind: s.Kind,
			File: s.File,
			Line: s.Line,
			Hash: shortSym(s.Hash),
		})
	}
	return syms
}

// emitMapHeader prints a compact, deterministic repo summary (file/symbol counts,
// the busiest top-level directories, and a pointer to the lookup tools). Designed
// to fit a Claude Code SessionStart hook in under ~200 tokens — it tells an agent
// the map exists and how to query it, without dumping the map itself.
func emitMapHeader(irData *ir.IR) {
	funcs, classes := 0, 0
	dirFiles := make(map[string]int)
	for path, f := range irData.Files {
		for _, s := range f.Symbols {
			switch s.Kind {
			case "function":
				funcs++
			case "class":
				classes++
			}
		}
		dir := path
		if i := strings.IndexByte(path, '/'); i >= 0 {
			dir = path[:i] + "/"
		}
		dirFiles[dir]++
	}

	type dc struct {
		dir   string
		count int
	}
	dirs := make([]dc, 0, len(dirFiles))
	for d, c := range dirFiles {
		dirs = append(dirs, dc{d, c})
	}
	// Busiest dirs first; ties broken by name so the header is deterministic.
	sort.Slice(dirs, func(i, j int) bool {
		if dirs[i].count != dirs[j].count {
			return dirs[i].count > dirs[j].count
		}
		return dirs[i].dir < dirs[j].dir
	})
	top := make([]string, 0, 5)
	for i := 0; i < len(dirs) && i < 5; i++ {
		top = append(top, dirs[i].dir)
	}

	fmt.Printf("runecho map: %d files, %d functions, %d classes.\n", len(irData.Files), funcs, classes)
	if len(top) > 0 {
		fmt.Printf("Busiest: %s\n", strings.Join(top, " "))
	}
	fmt.Println("To locate a definition, query the map instead of grepping: " +
		"`runecho-ir map --kind=func --dir=<p>` or the runecho MCP `locate` tool " +
		"(symbol → file:line, deterministic).")
}

// shortSym truncates a body hash to 4 hex chars for inline display (collision-
// safe within a repo, cheap in an agent's context window). Empty stays empty.
func shortSym(h string) string {
	if len(h) >= 4 {
		return h[:4]
	}
	return h
}

func lineStr(line int) string {
	if line <= 0 {
		return "?"
	}
	return fmt.Sprintf("%d", line)
}

// emitMapBySymbol renders the flat symbol index: name, kind, file:line, hash.
func emitMapBySymbol(syms []mapSym, compact bool) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, s := range syms {
		loc := s.File + ":" + lineStr(s.Line)
		if compact {
			fmt.Fprintf(w, "%s\t%s\n", s.Name, loc)
			continue
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", s.Name, kindAbbrev[s.Kind], loc, s.Hash)
	}
	w.Flush()
}

// emitMapByFile groups symbols under their file, sorted by line then name.
func emitMapByFile(syms []mapSym, compact bool) {
	byFile := make(map[string][]mapSym)
	var files []string
	for _, s := range syms {
		if _, seen := byFile[s.File]; !seen {
			files = append(files, s.File)
		}
		byFile[s.File] = append(byFile[s.File], s)
	}
	sort.Strings(files)

	for _, file := range files {
		group := byFile[file]
		sort.Slice(group, func(i, j int) bool {
			if group[i].Line != group[j].Line {
				return group[i].Line < group[j].Line
			}
			return group[i].Name < group[j].Name
		})
		fmt.Println(file)
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		for _, s := range group {
			if compact {
				fmt.Fprintf(w, "  \t%s\t%s\n", s.Name, lineStr(s.Line))
				continue
			}
			fmt.Fprintf(w, "  \t%s\t%s:%s\t%s\n", kindAbbrev[s.Kind], s.Name, lineStr(s.Line), s.Hash)
		}
		w.Flush()
	}
}

// emitMapJSON marshals the canonical machine shape: mode tells the consumer
// which key to read without introspecting (parity with diff --json).
func emitMapJSON(root string, syms []mapSym, byFile, changedOnly bool) int {
	payload := map[string]interface{}{
		"root":         root,
		"changed_only": changedOnly,
		"count":        len(syms),
	}
	if byFile {
		payload["mode"] = "files"
		// files is a JSON object keyed by path (always {} when empty, never null).
		files := make(map[string][]mapSym)
		for _, s := range syms {
			files[s.File] = append(files[s.File], s)
		}
		payload["files"] = files
	} else {
		payload["mode"] = "symbols"
		if syms == nil {
			syms = []mapSym{}
		}
		payload["symbols"] = syms
	}
	out, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return ExitError
	}
	fmt.Println(string(out))
	return 0
}
