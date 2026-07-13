package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/inth3shadows/runecho/internal/snapshot"
)

// newOracleRepo creates a temp central store + a temp repo dir with one Go file,
// enrolls it, and returns the oracle plus the repo name.
func newOracleRepo(t *testing.T) (*Oracle, string, *snapshot.DB) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "history.db")
	db, err := snapshot.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	repoDir := t.TempDir()
	src := "package demo\n\nfunc Alpha() {}\nfunc Beta() {}\n"
	if err := os.WriteFile(filepath.Join(repoDir, "demo.go"), []byte(src), 0644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	if _, err := db.EnrollRepo("demo", repoDir, "", 0); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	return NewOracle(db, dbPath), "demo", db
}

// TestOracleDiff_CappedRepoNoPhantomDrift is the regression guard for the P4-B
// cap-consistency bug: a repo enrolled with FileCap>0 stores a capped snapshot,
// and the oracle's live IR must be generated under the same cap. If liveIR were
// uncapped, latest-vs-live diff would report every file beyond the cap as a
// phantom addition on an unchanged repo.
func TestOracleDiff_CappedRepoNoPhantomDrift(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "history.db")
	db, err := snapshot.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// Repo with 5 source files, enrolled with a cap of 2.
	repoDir := t.TempDir()
	for _, n := range []string{"a.go", "b.go", "c.go", "d.go", "e.go"} {
		if err := os.WriteFile(filepath.Join(repoDir, n), []byte("package demo\n\nfunc F"+n[:1]+"() {}\n"), 0644); err != nil {
			t.Fatalf("write %s: %v", n, err)
		}
	}
	repoID, err := db.EnrollRepo("capped", repoDir, "", 2)
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}

	// Store a capped snapshot (mirrors `repo reindex` honoring FileCap=2).
	capped, err := liveIR(repoDir, 2)
	if err != nil {
		t.Fatalf("liveIR capped: %v", err)
	}
	if len(capped.Files) != 2 {
		t.Fatalf("capped snapshot has %d files, want 2", len(capped.Files))
	}
	if _, err := db.SaveSnapshot(repoID, "", "reindex", repoDir, capped); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}

	// Default latest-vs-live diff on the UNCHANGED repo must report zero drift.
	o := NewOracle(db, dbPath)
	out := call(t, o.diff, `{"repo":"capped"}`)
	if out["total_added"].(float64) != 0 || out["total_removed"].(float64) != 0 {
		t.Errorf("capped repo phantom drift: added=%v removed=%v (want 0/0) — liveIR not honoring FileCap",
			out["total_added"], out["total_removed"])
	}
}

func call(t *testing.T, fn func(json.RawMessage) (string, error), args string) map[string]any {
	t.Helper()
	out, err := fn([]byte(args))
	if err != nil {
		t.Fatalf("tool error: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("decode tool output: %v\n%s", err, out)
	}
	return m
}

func TestOracleHashAndStructure(t *testing.T) {
	o, name, _ := newOracleRepo(t)
	args := `{"repo":"` + name + `"}`

	h := call(t, o.hash, args)
	if h["file_count"].(float64) != 1 {
		t.Errorf("hash file_count = %v, want 1", h["file_count"])
	}
	hash1 := h["root_hash"].(string)
	if hash1 == "" {
		t.Fatal("empty root_hash")
	}
	// Determinism: same code → identical hash on a second call.
	if h2 := call(t, o.hash, args); h2["root_hash"] != hash1 {
		t.Errorf("non-deterministic hash: %v vs %v", h2["root_hash"], hash1)
	}

	s := call(t, o.structure, args)
	if s["symbol_count"].(float64) != 2 { // Alpha, Beta
		t.Errorf("structure symbol_count = %v, want 2", s["symbol_count"])
	}
}

// TestOracleStructureProjection covers the token-reduction projection: detail
// levels (tree|symbols|full) and `paths` glob scoping. The default (symbols)
// must drop the legacy arrays that `full` keeps for on-disk back-compat.
func TestOracleStructureProjection(t *testing.T) {
	o, name, _ := newOracleRepo(t) // demo.go: funcs Alpha, Beta

	fileOf := func(m map[string]any) map[string]any {
		f, ok := m["files"].(map[string]any)["demo.go"].(map[string]any)
		if !ok {
			t.Fatalf("demo.go missing from files: %v", m["files"])
		}
		return f
	}

	// Default detail = symbols: symbols[] present, legacy functions[] dropped.
	def := call(t, o.structure, `{"repo":"`+name+`"}`)
	if def["detail"] != "symbols" {
		t.Errorf("default detail = %v, want symbols", def["detail"])
	}
	f := fileOf(def)
	if _, ok := f["symbols"]; !ok {
		t.Error("symbols detail: expected symbols[]")
	}
	if _, ok := f["functions"]; ok {
		t.Error("symbols detail: legacy functions[] should be dropped")
	}

	// detail=tree: hash + symbol_count only, no symbols[].
	ft := fileOf(call(t, o.structure, `{"repo":"`+name+`","detail":"tree"}`))
	if _, ok := ft["symbols"]; ok {
		t.Error("tree detail: symbols[] should be absent")
	}
	if ft["symbol_count"].(float64) != 2 {
		t.Errorf("tree symbol_count = %v, want 2", ft["symbol_count"])
	}

	// detail=full: legacy functions[] present (back-compat shape).
	if _, ok := fileOf(call(t, o.structure, `{"repo":"`+name+`","detail":"full"}`))["functions"]; !ok {
		t.Error("full detail: expected legacy functions[]")
	}

	// Unknown detail is rejected, not silently coerced.
	if _, err := o.structure([]byte(`{"repo":"` + name + `","detail":"nope"}`)); err == nil {
		t.Error("bad detail should error")
	}

	// paths glob scoping.
	if hit := call(t, o.structure, `{"repo":"`+name+`","paths":["*.go"]}`); hit["file_count"].(float64) != 1 {
		t.Errorf("paths=*.go file_count = %v, want 1", hit["file_count"])
	}
	if miss := call(t, o.structure, `{"repo":"`+name+`","paths":["nomatch/**"]}`); miss["file_count"].(float64) != 0 {
		t.Errorf("paths=nomatch file_count = %v, want 0", miss["file_count"])
	}
}

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pattern, p string
		want       bool
	}{
		{"internal/mcp/**", "internal/mcp/tools_oracle.go", true},
		{"internal/mcp/**", "internal/ir/storage.go", false},
		{"internal/mcp/**", "internal/mcp2/x.go", false}, // sibling prefix must not match
		{"**/storage.go", "internal/ir/storage.go", true},
		{"**/storage.go", "internal/ir/mystorage.go", false}, // suffix must be on a boundary
		{"*.go", "demo.go", true},
		{"*.go", "internal/ir/storage.go", false},       // path.Match: * does not cross /
		{"internal/ir", "internal/ir/storage.go", true}, // bare dir prefix selects subtree
		{"internal/ir", "internal/irx/storage.go", false},
		// Multiple globstars: literal segments must appear in order on boundaries.
		{"internal/**/parser/**/go.go", "internal/x/parser/y/go.go", true},
		{"internal/**/parser/**/go.go", "internal/x/y/go.go", false},          // missing middle "parser"
		{"internal/**/parser/**/go.go", "internal/x/parser/y/mygo.go", false}, // suffix not on a boundary
		{"a/**/b/**/c", "a/b/c", true},                                        // each ** matches zero components
	}
	for _, c := range cases {
		if got := matchGlob(c.pattern, c.p); got != c.want {
			t.Errorf("matchGlob(%q,%q)=%v want %v", c.pattern, c.p, got, c.want)
		}
	}
}

func TestOracleLocate(t *testing.T) {
	o, name, _ := newOracleRepo(t) // demo.go defines exported Alpha, Beta

	// Targeted lookup returns just the match.
	one := call(t, o.locate, `{"repo":"`+name+`","symbol":"Alpha"}`)
	if one["count"].(float64) != 1 {
		t.Fatalf("locate Alpha count = %v, want 1", one["count"])
	}
	syms := one["symbols"].([]any)
	got := syms[0].(map[string]any)
	if got["name"] != "Alpha" || got["kind"] != "function" {
		t.Errorf("locate Alpha = %+v, want name=Alpha kind=function", got)
	}
	if got["file"] != "demo.go" {
		t.Errorf("locate Alpha file = %v, want demo.go", got["file"])
	}

	// No symbol → all function+class symbols (Alpha, Beta).
	all := call(t, o.locate, `{"repo":"`+name+`"}`)
	if all["count"].(float64) != 2 {
		t.Errorf("locate all count = %v, want 2", all["count"])
	}
	if all["truncated"].(bool) {
		t.Error("2 symbols should not be truncated")
	}

	// No match → empty, not an error.
	none := call(t, o.locate, `{"repo":"`+name+`","symbol":"Nonexistent"}`)
	if none["count"].(float64) != 0 {
		t.Errorf("locate miss count = %v, want 0", none["count"])
	}

	// Invalid kind is rejected.
	if _, err := o.locate([]byte(`{"repo":"` + name + `","kind":"bogus"}`)); err == nil {
		t.Error("expected error for invalid kind")
	}
}

// TestOracleLocate_NamedQuerySearchesAllKinds pins the definitive-zero-match
// fix: a named lookup must find a symbol that exists only under an internal kind
// — here import_name, the bound name of `import { readFileSync } from 'fs'`,
// which lives nowhere else in the IR. Before the fix the functions+classes browse
// default gated named lookups too, so `locate readFileSync` returned a false
// "definitive" zero. Browse mode (no symbol) must still stay scoped to
// functions+classes so it does not dump every import binding.
func TestOracleLocate_NamedQuerySearchesAllKinds(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "history.db")
	db, err := snapshot.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	repoDir := t.TempDir()
	src := "import { readFileSync } from 'fs';\n\nexport function useIt() {\n  return readFileSync('x');\n}\n"
	if err := os.WriteFile(filepath.Join(repoDir, "mod.js"), []byte(src), 0644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	if _, err := db.EnrollRepo("jsdemo", repoDir, "", 0); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	o := NewOracle(db, dbPath)

	// Named lookup of the bound import name must find it — a zero here would be a
	// false "definitive not found".
	got := call(t, o.locate, `{"repo":"jsdemo","symbol":"readFileSync"}`)
	if got["count"].(float64) < 1 {
		t.Fatalf("locate readFileSync count = %v, want >=1 (import_name must be reachable by name)", got["count"])
	}
	if first := got["symbols"].([]any)[0].(map[string]any); first["name"] != "readFileSync" {
		t.Errorf("locate readFileSync first match = %v, want name=readFileSync", first)
	}

	// Browse mode (no symbol) must NOT dump import bindings — stays functions+classes.
	all := call(t, o.locate, `{"repo":"jsdemo"}`)
	for _, s := range all["symbols"].([]any) {
		if k := s.(map[string]any)["kind"]; k == "import_name" || k == "import" {
			t.Errorf("browse mode should not list %v symbols: %+v", k, s)
		}
	}
}

// TestOracleLocate_Offset covers the offset-slicing mechanics on a fixture
// well under locateMatchCap: offset skips already-seen matches, next_offset
// is absent once nothing remains, and out-of-range/negative offsets are
// handled without panicking or double-counting. It does NOT exercise a page
// actually being clipped by locateMatchCap — see
// TestOracleLocate_OffsetAcrossCapBoundary for that. Regression test for #88
// (locate had no way to page past locateMatchCap).
func TestOracleLocate_Offset(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "history.db")
	db, err := snapshot.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	repoDir := t.TempDir()
	var src string
	for i := 0; i < 5; i++ {
		src += fmt.Sprintf("func Fn%d() {}\n", i)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "demo.go"), []byte("package demo\n\n"+src), 0644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	if _, err := db.EnrollRepo("demo", repoDir, "", 0); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	o := NewOracle(db, dbPath)

	// No offset: all 5, no next_offset (well under locateMatchCap).
	all := call(t, o.locate, `{"repo":"demo"}`)
	if all["total"].(float64) != 5 || all["count"].(float64) != 5 {
		t.Fatalf("locate all = %+v, want total=5 count=5", all)
	}
	if _, ok := all["next_offset"]; ok {
		t.Errorf("next_offset should be absent when everything fit in one page: %+v", all)
	}

	// Manually page 2 at a time and confirm the union matches the full set,
	// with no duplicates and no gaps.
	seen := map[string]bool{}
	offset := 0
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("too many pages — pagination likely stuck")
		}
		page := call(t, o.locate, fmt.Sprintf(`{"repo":"demo","offset":%d}`, offset))
		if page["offset"].(float64) != float64(offset) {
			t.Errorf("page offset = %v, want %d", page["offset"], offset)
		}
		syms := page["symbols"].([]any)
		for _, s := range syms {
			name := s.(map[string]any)["name"].(string)
			if seen[name] {
				t.Errorf("symbol %q returned in more than one page", name)
			}
			seen[name] = true
		}
		next, hasNext := page["next_offset"]
		if !hasNext {
			break
		}
		offset = int(next.(float64))
	}
	if len(seen) != 5 {
		t.Errorf("paged through %d distinct symbols, want 5: %v", len(seen), seen)
	}

	// Offset past the end: empty page, not an error, total still reports 5.
	past := call(t, o.locate, `{"repo":"demo","offset":100}`)
	if past["count"].(float64) != 0 {
		t.Errorf("offset past end count = %v, want 0", past["count"])
	}
	if past["total"].(float64) != 5 {
		t.Errorf("offset past end total = %v, want 5", past["total"])
	}
	if _, ok := past["next_offset"]; ok {
		t.Errorf("next_offset should be absent past the end: %+v", past)
	}

	// Negative offset is rejected.
	if _, err := o.locate([]byte(`{"repo":"demo","offset":-1}`)); err == nil {
		t.Error("expected error for negative offset")
	}
}

// TestOracleLocate_OffsetAcrossCapBoundary forces a real locateMatchCap-sized
// result set (unlike TestOracleLocate_Offset's 5-symbol fixture, which is too
// small to ever trip the cap) so a page is actually clipped by locateMatchCap
// and next_offset genuinely drives a second, cap-triggered page.
func TestOracleLocate_OffsetAcrossCapBoundary(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "history.db")
	db, err := snapshot.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	repoDir := t.TempDir()
	const total = locateMatchCap + 5
	var src string
	for i := 0; i < total; i++ {
		src += fmt.Sprintf("func Fn%04d() {}\n", i)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "demo.go"), []byte("package demo\n\n"+src), 0644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	if _, err := db.EnrollRepo("demo", repoDir, "", 0); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	o := NewOracle(db, dbPath)

	first := call(t, o.locate, `{"repo":"demo"}`)
	if first["total"].(float64) != float64(total) {
		t.Fatalf("first page total = %v, want %d", first["total"], total)
	}
	if first["count"].(float64) != locateMatchCap {
		t.Fatalf("first page count = %v, want %d (capped)", first["count"], locateMatchCap)
	}
	if !first["truncated"].(bool) {
		t.Error("first page should be truncated")
	}
	next, ok := first["next_offset"]
	if !ok || next.(float64) != float64(locateMatchCap) {
		t.Fatalf("next_offset = %v, want %d", first["next_offset"], locateMatchCap)
	}

	second := call(t, o.locate, fmt.Sprintf(`{"repo":"demo","offset":%d}`, int(next.(float64))))
	if second["count"].(float64) != 5 {
		t.Fatalf("second page count = %v, want 5 (the remainder)", second["count"])
	}
	if second["truncated"].(bool) {
		t.Error("second page should not be truncated — it's the last one")
	}
	if _, ok := second["next_offset"]; ok {
		t.Errorf("next_offset should be absent on the final page: %+v", second)
	}
}

func TestSymbolMatches(t *testing.T) {
	cases := []struct {
		name, query string
		want        bool
	}{
		{"Reader.fetch", "Reader.fetch", true}, // exact
		{"Reader.fetch", "Reader", true},       // prefix
		{"Reader.fetch", "fetch", true},        // last dotted segment
		{"Reader.fetch", "etch", false},        // not a segment/prefix
		{"get_scope", "get", true},             // prefix
		{"get_scope", "scope", false},          // not a prefix, no dot
	}
	for _, c := range cases {
		if got := symbolMatches(c.name, c.query); got != c.want {
			t.Errorf("symbolMatches(%q, %q) = %v, want %v", c.name, c.query, got, c.want)
		}
	}
}

func TestOracleStatusAndHealth(t *testing.T) {
	o, name, _ := newOracleRepo(t)

	st := call(t, o.status, `{"repo":"`+name+`"}`)
	if st["snapshot_count"].(float64) != 0 {
		t.Errorf("fresh repo snapshot_count = %v, want 0", st["snapshot_count"])
	}
	if st["last_indexed"] != nil {
		t.Errorf("fresh repo last_indexed = %v, want nil", st["last_indexed"])
	}

	h := call(t, o.health, `{}`)
	if h["integrity"] != "ok" {
		t.Errorf("integrity = %v, want ok", h["integrity"])
	}
	if h["repo_count"].(float64) != 1 {
		t.Errorf("repo_count = %v, want 1", h["repo_count"])
	}
}

func TestOracleDiffDetectsDrift(t *testing.T) {
	o, name, db := newOracleRepo(t)
	repo, _ := db.GetRepoByName(name)

	// Snapshot the current state (Alpha, Beta), then add Gamma to the live code.
	live, err := liveIR(repo.Path, 0)
	if err != nil {
		t.Fatalf("liveIR: %v", err)
	}
	if _, err := db.SaveSnapshot(repo.ID, "", "base", repo.Path, live); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	add := "package demo\n\nfunc Alpha() {}\nfunc Beta() {}\nfunc Gamma() {}\n"
	if err := os.WriteFile(filepath.Join(repo.Path, "demo.go"), []byte(add), 0644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	// Default diff = latest snapshot vs live → should see +1 (Gamma).
	d := call(t, o.diff, `{"repo":"`+name+`"}`)
	if d["total_added"].(float64) != 1 {
		t.Errorf("diff total_added = %v, want 1 (Gamma)", d["total_added"])
	}
}

func TestOracleUnenrolledRepoErrors(t *testing.T) {
	o, _, _ := newOracleRepo(t)
	if _, err := o.hash([]byte(`{"repo":"ghost"}`)); err == nil {
		t.Fatal("expected error for unenrolled repo")
	}
}

// F3: a half-specified pair (only `a`, no `b`) must error, not silently fall
// through to latest-vs-live and answer a different question.
func TestOracleDiffPartialPairErrors(t *testing.T) {
	o, name, _ := newOracleRepo(t)
	if _, err := o.diff([]byte(`{"repo":"` + name + `","a":1}`)); err == nil {
		t.Error("diff with only `a` should error, got success")
	}
	if _, err := o.diff([]byte(`{"repo":"` + name + `","b":2}`)); err == nil {
		t.Error("diff with only `b` should error, got success")
	}
}

// TestOracleDiffCrossRepoBlocked verifies that snapshot IDs from a different
// repo are rejected — diffs must never cross repo boundaries.
func TestOracleDiffCrossRepoBlocked(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "history.db")
	db, err := snapshot.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	dir1, dir2 := t.TempDir(), t.TempDir()
	srcA := []byte("package demo\nfunc FuncA() {}\n")
	srcB := []byte("package demo\nfunc FuncB() {}\n")
	os.WriteFile(filepath.Join(dir1, "a.go"), srcA, 0644)
	os.WriteFile(filepath.Join(dir2, "b.go"), srcB, 0644)

	repoID1, _ := db.EnrollRepo("repoA", dir1, "", 0)
	repoID2, _ := db.EnrollRepo("repoB", dir2, "", 0)

	live1, _ := liveIR(dir1, 0)
	live2, _ := liveIR(dir2, 0)
	snapID1, _ := db.SaveSnapshot(repoID1, "", "s", dir1, live1)
	snapID2, _ := db.SaveSnapshot(repoID2, "", "s", dir2, live2)

	o := NewOracle(db, dbPath)

	args := []byte(fmt.Sprintf(`{"repo":"repoA","a":%d,"b":%d}`, snapID1, snapID2))
	if _, err := o.diff(args); err == nil {
		t.Error("cross-repo diff should be rejected, got success")
	}
}

// TestOracleDiffSinceMode verifies the `since` label mode: diff latest snapshot
// with that label against live code.
func TestOracleDiffSinceMode(t *testing.T) {
	o, name, db := newOracleRepo(t)
	repo, _ := db.GetRepoByName(name)

	live, _ := liveIR(repo.Path, 0)
	db.SaveSnapshot(repo.ID, "", "session-start", repo.Path, live)

	// Add a third function to the live code.
	extra := []byte("package demo\n\nfunc Alpha() {}\nfunc Beta() {}\nfunc Gamma() {}\n")
	os.WriteFile(filepath.Join(repo.Path, "demo.go"), extra, 0644)

	d := call(t, o.diff, `{"repo":"`+name+`","since":"session-start"}`)
	if d["total_added"].(float64) != 1 {
		t.Errorf("since-mode diff total_added = %v, want 1 (Gamma)", d["total_added"])
	}
}

// TestOracleDiffSinceMissingLabel errors cleanly when the label does not exist.
func TestOracleDiffSinceMissingLabel(t *testing.T) {
	o, name, _ := newOracleRepo(t)
	if _, err := o.diff([]byte(`{"repo":"` + name + `","since":"no-such-label"}`)); err == nil {
		t.Error("unknown since label should return error")
	}
}

// TestMatchGlob_GlobstarFinalComponent pins the F67 fix: `**/a/**` never
// matched a path whose literal segment is the FINAL component (a trailing
// `**` matches zero components), so structure/path filters silently missed
// those files.
func TestMatchGlob_GlobstarFinalComponent(t *testing.T) {
	cases := []struct {
		pattern, p string
		want       bool
	}{
		{"**/a/**", "x/a", true},       // literal is the last component
		{"**/a/**", "a", true},         // literal is the whole path
		{"**/a/**", "x/a/y", true},     // unchanged: literal mid-path
		{"**/a/**", "x/ab", false},     // no boundary: not the segment "a"
		{"**/a/**/b/**", "x/a", false}, // later non-empty segment must still fail
	}
	for _, c := range cases {
		if got := matchGlob(c.pattern, c.p); got != c.want {
			t.Errorf("matchGlob(%q,%q)=%v want %v", c.pattern, c.p, got, c.want)
		}
	}
}
