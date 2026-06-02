package claims

import "testing"

func TestIsCodeSymbol(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"IRProvider", true},
		{"ValidateClaims", true},
		{"FileIR", true},
		{"ParseData", true},
		{"ALL_CAPS", false},
		{"UPPERCASE", false},
		{"snake_case", false},
		{"emit_fault", false},
		{"__init__", false},
		{"x", false},
		{"Ab", false}, // len==2, filtered by minimum length guard
		{"Abc", true}, // len==3, valid CamelCase
	}
	for _, tc := range cases {
		if got := IsCodeSymbol(tc.name); got != tc.want {
			t.Errorf("IsCodeSymbol(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestExtractSymbolRefs_Backtick(t *testing.T) {
	text := "Call `ParseData` to load then `FileIR` to wrap."
	refs := ExtractSymbolRefs(text)
	if _, ok := refs["ParseData"]; !ok {
		t.Errorf("expected ParseData in refs: %v", refs)
	}
	if _, ok := refs["FileIR"]; !ok {
		t.Errorf("expected FileIR in refs: %v", refs)
	}
}

func TestExtractSymbolRefs_DeclPattern(t *testing.T) {
	text := "The function func ValidateClaims handles this.\ntype MyHandler struct"
	refs := ExtractSymbolRefs(text)
	if _, ok := refs["ValidateClaims"]; !ok {
		t.Errorf("expected ValidateClaims in refs: %v", refs)
	}
	if _, ok := refs["MyHandler"]; !ok {
		t.Errorf("expected MyHandler in refs: %v", refs)
	}
}

func TestExtractSymbolRefs_AllCapsFiltered(t *testing.T) {
	text := "`ALL_CAPS` should not appear.\n`UPPERCASE` same."
	refs := ExtractSymbolRefs(text)
	if _, ok := refs["ALL_CAPS"]; ok {
		t.Errorf("ALL_CAPS should be filtered: %v", refs)
	}
	if _, ok := refs["UPPERCASE"]; ok {
		t.Errorf("UPPERCASE should be filtered: %v", refs)
	}
}

func TestExtractSymbolRefs_SnakeCaseFiltered(t *testing.T) {
	text := "Use `emit_fault` or `validate_claims` here."
	refs := ExtractSymbolRefs(text)
	if _, ok := refs["emit_fault"]; ok {
		t.Errorf("snake_case should be filtered: %v", refs)
	}
}

func TestExtractSymbolRefs_Dedup(t *testing.T) {
	text := "`ParseData` is used here.\n`ParseData` is used again."
	refs := ExtractSymbolRefs(text)
	// Should appear exactly once; map guarantees this but verify count.
	if len(refs) != 1 {
		t.Errorf("dedup: expected 1 unique ref, got %d: %v", len(refs), refs)
	}
}
