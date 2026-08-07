// #323 spike: does an external gopls oracle catch v.Method() cases the local
// GoVarType heuristic (internal/guard/vartype.go) misses, and at what latency
// against the guard's ~12ms hook budget?
//
// METHOD. Reuses the exact compiler-oracle differential machinery from
// resolve_differential_test.go (corpusRoot, copyModule, collectSites,
// applyMutation, buildPackage, whichCheckCatches) to find the same
// proven-unresolved, currently-missed selector-value (`v.Method()`) sites
// #312's "N/60" number is drawn from — then, for each MISSED site only, asks
// gopls (via the hand-rolled stdio client in oracle_gopls_client_test.go) to
// hover the receiver `v` on the ORIGINAL (unmutated) file. A resolved hover
// is the spike's hit signal: a real oracle-backed check would have a
// concrete type to check method existence against, where the local text
// heuristic had none.
//
// This never mutates cmd/runecho-guard or ships anything — see the plan at
// ~/.claude/plans/graceful-strolling-hennessy.md for the go/no-go framing.
package guard_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/inth3shadows/runecho/internal/depindex"
	"github.com/inth3shadows/runecho/internal/guard"
)

// defaultSpikeMutations is this spike's own budget knob (RUNECHO_SPIKE_MUTATIONS),
// distinct from RUNECHO_ORACLE_MUTATIONS: the sibling differential test splits
// that budget across 4 shapes, but this spike only ever samples
// shapeSelectorValue, so reusing the same env var under a different meaning
// would silently change the sibling test's semantics for anyone who exports
// it repo-wide.
const defaultSpikeMutations = 60

// oracleSite is a proven-unresolved, missed-by-every-check selector-value
// mutation, plus the receiver's own position (recovered from the ORIGINAL
// line, never the mutated one) for the gopls hover query.
type oracleSite struct {
	relPath, absPath string
	line             int // 1-based
	receiverCol      int // byte offset, original (pre-mutation) line
	receiverName     string
	memberName       string
}

func TestSpikeGoplsOracleVarType(t *testing.T) {
	goplsBin := os.Getenv("GOPLS_BIN")
	if os.Getenv("RUNECHO_SPIKE_GOPLS") != "1" || goplsBin == "" {
		t.Skip("set RUNECHO_SPIKE_GOPLS=1 and GOPLS_BIN=/path/to/gopls to run the #323 oracle spike")
	}
	if info, err := os.Stat(goplsBin); err != nil || info.IsDir() {
		t.Skipf("GOPLS_BIN %q is not an executable file: %v", goplsBin, err)
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH — needed to stage and build the corpus copy")
	}

	root := corpusRoot(t)
	if len(listPackages(t, root)) == 0 {
		t.Skipf("no buildable Go packages under %s", root)
	}

	work := t.TempDir()
	const maxCorpusBytes = 512 << 20
	if !copyModule(t, root, work, maxCorpusBytes) {
		t.Skip("could not stage a writable copy of the corpus")
	}
	workPkgs := listPackages(t, work)
	if len(workPkgs) == 0 {
		t.Skip("staged copy does not list any buildable packages")
	}
	known := knownSet(t, work)
	modulePath := guard.GoModulePath(work)
	depIdx := depindex.NewGoIndex(work)

	budget := defaultSpikeMutations
	if v := os.Getenv("RUNECHO_SPIKE_MUTATIONS"); v != "" {
		n := 0
		if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n <= 0 {
			t.Fatalf("RUNECHO_SPIKE_MUTATIONS must be a positive integer, got %q", v)
		}
		budget = n
	}

	// Population: every selector-value site across the corpus, deterministically
	// ordered and evenly strided — same discipline as the sibling differential
	// test, so an early file does not dominate the sample.
	var pool []mutationSite
	pkgOf := map[string]string{}
	for _, p := range workPkgs {
		pkgOf[p.Dir] = p.ImportPath
		for _, s := range collectSites(t, work, p) {
			if s.shape == shapeSelectorValue {
				pool = append(pool, s)
			}
		}
	}
	if len(pool) == 0 {
		t.Skip("no selector-value (v.Method()) call sites in the corpus")
	}
	sort.Slice(pool, func(i, j int) bool {
		if pool[i].relPath != pool[j].relPath {
			return pool[i].relPath < pool[j].relPath
		}
		if pool[i].line != pool[j].line {
			return pool[i].line < pool[j].line
		}
		return pool[i].col < pool[j].col
	})
	sampleN := budget
	if sampleN > len(pool) {
		sampleN = len(pool)
	}
	stride := 1
	if len(pool) > sampleN {
		stride = len(pool) / sampleN
	}
	var chosen []mutationSite
	for i, n := 0, 0; i < len(pool) && n < sampleN; i, n = i+stride, n+1 {
		chosen = append(chosen, pool[i])
	}

	// Phase A: reproduce #312's own adjudication for exactly this shape/sample —
	// which sites are PROVEN unresolved by the compiler, and which of those are
	// MISSED by every existing check. Only the missed ones are candidates for an
	// oracle fallback; a site any check already catches needs no oracle.
	proven, missed, discarded := 0, 0, 0
	caughtBy := map[string]int{}
	var missedSites []mutationSite
	for _, s := range chosen {
		importPath, ok := pkgOf[filepath.Dir(s.absPath)]
		if !ok {
			discarded++
			continue
		}
		mutated, original, ok := applyMutation(s)
		if !ok {
			discarded++
			continue
		}
		if err := os.WriteFile(s.absPath, []byte(mutated), 0o644); err != nil {
			t.Fatalf("write mutation: %v", err)
		}
		built, refs := buildPackage(t, work, importPath)
		isProven := false
		if !built {
			for _, r := range refs {
				if r.Line == s.line && (r.Name == s.fresh || strings.HasSuffix(r.Name, "."+s.fresh)) {
					isProven = true
					break
				}
			}
		}
		if !isProven {
			discarded++
			if err := os.WriteFile(s.absPath, []byte(original), 0o644); err != nil {
				t.Fatalf("restore file: %v", err)
			}
			continue
		}
		by := whichCheckCatches(known, modulePath, s.relPath, mutated, s.fresh, depIdx)
		if err := os.WriteFile(s.absPath, []byte(original), 0o644); err != nil {
			t.Fatalf("restore file: %v", err)
		}
		proven++
		if by != "" {
			caughtBy[by]++
			continue
		}
		missed++
		missedSites = append(missedSites, s)
	}

	t.Logf("corpus=%s selector-value population=%d sampled=%d (stride %d)", root, len(pool), len(chosen), stride)
	t.Logf("PROVEN=%d MISSED=%d (baseline, no oracle) discarded=%d caught-by=%v", proven, missed, discarded, caughtBy)
	if proven == 0 {
		t.Skip("no selector-value mutation survived compiler adjudication — raise RUNECHO_SPIKE_MUTATIONS")
	}
	if missed == 0 {
		t.Log("every proven site was already caught by an existing check — nothing for an oracle to add here")
		return
	}

	// Recover the RECEIVER's position (not the member's — collectSites only
	// stores the member's column) by re-matching reSelectorCall against the
	// original, now-restored line and picking the match whose member group
	// lines up with this site.
	var oracleSites []oracleSite
	for _, s := range missedSites {
		data, err := os.ReadFile(s.absPath)
		if err != nil {
			continue
		}
		lines := strings.Split(string(data), "\n")
		if s.line-1 >= len(lines) {
			continue
		}
		line := lines[s.line-1]
		found := false
		for _, m := range reSelectorCall.FindAllStringSubmatchIndex(line, -1) {
			if m[6] == s.col && line[m[6]:m[7]] == s.name {
				oracleSites = append(oracleSites, oracleSite{
					relPath: s.relPath, absPath: s.absPath, line: s.line,
					receiverCol: m[4], receiverName: line[m[4]:m[5]], memberName: s.name,
				})
				found = true
				break
			}
		}
		if !found {
			t.Logf("NOTE: could not recover receiver position for %s:%d %s — skipped for oracle query", s.relPath, s.line, s.name)
		}
	}
	if len(oracleSites) == 0 {
		t.Skip("no missed site's receiver position could be recovered")
	}

	// Phase B: for each missed site, ask gopls to hover the receiver on the
	// ORIGINAL file — no mutation involved, since the question is only "can an
	// oracle resolve v's type here", not "does gopls also flag the mutation".
	client, err := startGoplsClient(goplsBin)
	if err != nil {
		t.Fatalf("start gopls: %v", err)
	}
	defer client.close()

	rootURI := "file://" + work
	initStart := time.Now()
	if err := client.initialize(rootURI); err != nil {
		t.Fatalf("gopls initialize: %v", err)
	}
	initLatency := time.Since(initStart)

	opened := map[string]bool{}
	hits, total := 0, 0
	var warmLatencies []time.Duration
	var coldLatency time.Duration
	var examples []string

	for _, os_ := range oracleSites {
		uri := "file://" + os_.absPath
		if !opened[os_.absPath] {
			data, err := os.ReadFile(os_.absPath)
			if err != nil {
				continue
			}
			if err := client.didOpen(uri, string(data)); err != nil {
				t.Logf("didOpen %s: %v", os_.relPath, err)
				continue
			}
			opened[os_.absPath] = true
		}

		start := time.Now()
		resolved, _, err := client.hover(uri, os_.line-1, os_.receiverCol)
		elapsed := time.Since(start)
		if err != nil {
			t.Logf("hover %s:%d %s: %v", os_.relPath, os_.line, os_.receiverName, err)
			continue
		}
		total++
		if total == 1 {
			coldLatency = elapsed
		} else {
			warmLatencies = append(warmLatencies, elapsed)
		}
		if resolved {
			hits++
			if len(examples) < 8 {
				examples = append(examples, fmt.Sprintf("%s:%d  %s.%s", os_.relPath, os_.line, os_.receiverName, os_.memberName))
			}
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\n=== #323 gopls-oracle spike ===\n")
	fmt.Fprintf(&b, "baseline (no oracle):  PROVEN=%d MISSED=%d  (i.e. %d/%d caught today)\n", proven, missed, proven-missed, proven)
	fmt.Fprintf(&b, "oracle queried %d of the %d missed sites (rest: receiver position unrecoverable)\n", total, len(oracleSites))
	fmt.Fprintf(&b, "oracle RESOLVED a type for %d/%d queried  -> if wired, hit rate becomes %d/%d\n",
		hits, total, proven-missed+hits, proven)
	fmt.Fprintf(&b, "gopls initialize (cold, one-time per daemon lifetime): %v\n", initLatency)
	fmt.Fprintf(&b, "first hover (cold, includes this file's own indexing): %v\n", coldLatency)
	if len(warmLatencies) > 0 {
		fmt.Fprintf(&b, "warm hover latency: p50=%v p90=%v  (n=%d)\n",
			percentile(warmLatencies, 50), percentile(warmLatencies, 90), len(warmLatencies))
	} else {
		fmt.Fprintf(&b, "warm hover latency: no samples (only one query ran)\n")
	}
	fmt.Fprintf(&b, "pivot line named in #323: ~5-8ms p50 for synchronous composition to fit the guard's ~12ms hook budget\n")
	for _, e := range examples {
		fmt.Fprintf(&b, "  RESOLVED: %s\n", e)
	}
	t.Log(b.String())
}

func percentile(durs []time.Duration, p float64) time.Duration {
	if len(durs) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), durs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := int(p / 100 * float64(len(sorted)-1))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
