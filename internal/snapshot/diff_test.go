package snapshot

import (
	"encoding/json"
	"testing"
)

// TestDiffPayloadShape locks the canonical JSON shape consumed by both the
// `runecho-ir diff --json` CLI flag and the MCP `diff` oracle tool. If this
// shape changes, the harness gate's parser must change with it — so the test
// breaks loud rather than letting the two surfaces drift silently.
func TestDiffPayloadShape(t *testing.T) {
	result := DiffResult{
		SnapshotA:    SnapshotMeta{RootHash: "aaaaaaaaaaaa"},
		SnapshotB:    SnapshotMeta{RootHash: "bbbbbbbbbbbb"},
		TotalAdded:   1,
		TotalRemoved: 1,
		Files: []FileDiff{
			{
				Path:    "main.go",
				Status:  "modified",
				Added:   []SymbolDelta{{Name: "NewFunc", Kind: "function"}},
				Removed: []SymbolDelta{{Name: "OldFunc", Kind: "function"}},
			},
		},
	}

	// Round-trip through JSON to assert the wire contract, not just the Go map.
	raw, err := json.Marshal(DiffPayload(result))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	for _, key := range []string{"summary", "total_added", "total_removed", "total_modified", "files"} {
		if _, ok := got[key]; !ok {
			t.Errorf("missing top-level key %q in diff payload", key)
		}
	}
	if got["total_added"].(float64) != 1 || got["total_removed"].(float64) != 1 {
		t.Errorf("totals wrong: %v / %v", got["total_added"], got["total_removed"])
	}

	files, ok := got["files"].([]any)
	if !ok || len(files) != 1 {
		t.Fatalf("files shape wrong: %v", got["files"])
	}
	f := files[0].(map[string]any)
	// FileDiff has no JSON tags -> Go field names. The harness gate's ir_contract
	// check reads "Removed" (symbols dropped from a file) to detect a removed
	// export; lock those key names here.
	for _, key := range []string{"Path", "Status", "Added", "Removed"} {
		if _, ok := f[key]; !ok {
			t.Errorf("missing file key %q", key)
		}
	}
	removed := f["Removed"].([]any)
	if len(removed) != 1 {
		t.Fatalf("expected 1 removed symbol, got %v", removed)
	}
	sym := removed[0].(map[string]any)
	if sym["Name"] != "OldFunc" || sym["Kind"] != "function" {
		t.Errorf("removed symbol fields wrong: %v", sym)
	}
}

// TestComputeDiff_ClassHashModification covers the integration point that
// issue #53's type-level hashing feeds: before that change, class-kind
// symbols always carried Hash == "" on both sides, so this branch of
// computeDiff was dead for structs/classes — a field edit was invisible to
// diff. Now that parsers populate a real class hash, the existing
// both-sides-non-empty check (diff.go) must catch it like any other symbol.
func TestComputeDiff_ClassHashModification(t *testing.T) {
	a := SnapshotMeta{RootHash: "aaa"}
	b := SnapshotMeta{RootHash: "bbb"}
	aFiles := map[string]string{"config.go": "filehash1"}
	bFiles := map[string]string{"config.go": "filehash2"} // file content changed along with the field
	aSymbols := map[string][]SymbolDelta{
		"config.go": {{Name: "Config", Kind: "class", Hash: "hash-before"}},
	}
	bSymbols := map[string][]SymbolDelta{
		"config.go": {{Name: "Config", Kind: "class", Hash: "hash-after"}},
	}

	result := computeDiff(a, b, aFiles, bFiles, aSymbols, bSymbols)

	if result.TotalModified != 1 {
		t.Fatalf("TotalModified = %d, want 1 (a struct field edit should surface as modified)", result.TotalModified)
	}
	if len(result.Files) != 1 || len(result.Files[0].Modified) != 1 {
		t.Fatalf("Files = %+v, want one file with one modified symbol", result.Files)
	}
	if result.Files[0].Modified[0].Name != "Config" {
		t.Errorf("modified symbol = %+v, want Config", result.Files[0].Modified[0])
	}
}

// TestDiffPayloadEmptyFilesIsArray asserts a zero-drift diff marshals
// "files": [] (not null) — the common baseline case must stay a JSON array so a
// machine consumer never has to null-guard before iterating.
func TestDiffPayloadEmptyFilesIsArray(t *testing.T) {
	raw, err := json.Marshal(DiffPayload(DiffResult{}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	files, ok := got["files"].([]any)
	if !ok {
		t.Fatalf("files should be a JSON array, got %T (%s)", got["files"], raw)
	}
	if len(files) != 0 {
		t.Errorf("empty diff should have 0 files, got %d", len(files))
	}
}
