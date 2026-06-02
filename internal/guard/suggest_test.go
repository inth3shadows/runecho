package guard

import "testing"

func TestSuggest_CaseSlip(t *testing.T) {
	known := map[string]struct{}{"GetUserByID": {}, "DeleteUser": {}}
	got, ok := Suggest("getUserByID", known)
	if !ok || got != "GetUserByID" {
		t.Fatalf("Suggest = %q,%v; want GetUserByID,true", got, ok)
	}
}

func TestSuggest_OneEdit(t *testing.T) {
	known := map[string]struct{}{"FetchUser": {}, "FlushCache": {}}
	got, ok := Suggest("FetchUserr", known) // doubled trailing char, dist 1
	if !ok || got != "FetchUser" {
		t.Fatalf("Suggest = %q,%v; want FetchUser,true", got, ok)
	}
}

func TestSuggest_NoCloseMatch(t *testing.T) {
	known := map[string]struct{}{"ProcessOrder": {}, "RenderTemplate": {}}
	if got, ok := Suggest("Xyzzy", known); ok {
		t.Fatalf("Suggest = %q,true; want no match", got)
	}
}

func TestSuggest_BeyondThreshold(t *testing.T) {
	known := map[string]struct{}{"Handler": {}}
	// "Handlerxyz" is distance 3 from "Handler" — past suggestMaxDist (2).
	if got, ok := Suggest("Handlerxyz", known); ok {
		t.Fatalf("Suggest = %q,true; want no match (distance 3)", got)
	}
}

func TestSuggest_DeterministicTie(t *testing.T) {
	// Both "Abc" and "Abd" are distance 1 from "Abe"; the lexicographically
	// smaller candidate must win, regardless of map iteration order.
	known := map[string]struct{}{"Abd": {}, "Abc": {}}
	for i := 0; i < 20; i++ {
		got, ok := Suggest("Abe", known)
		if !ok || got != "Abc" {
			t.Fatalf("Suggest = %q,%v; want Abc,true (deterministic tie)", got, ok)
		}
	}
}

func TestSuggest_EmptyKnown(t *testing.T) {
	if _, ok := Suggest("Anything", map[string]struct{}{}); ok {
		t.Fatal("Suggest on empty known set should not match")
	}
}

func TestRun_AttachesSuggestion(t *testing.T) {
	symbols := map[string]struct{}{"ProcessOrder": {}}
	diffs := []FileDiff{{
		Path:       "main.go",
		AddedLines: lines(`x := ProcesOrder(o)`), // missing 's', dist 1
	}}
	violations := Run(symbols, "", diffs)
	if len(violations) != 1 {
		t.Fatalf("expected 1 violation, got %v", violations)
	}
	if violations[0].Suggestion != "ProcessOrder" {
		t.Errorf("Suggestion = %q, want ProcessOrder", violations[0].Suggestion)
	}
}

func TestLevenshtein_KnownDistances(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "", 3},
		{"", "abc", 3},
		{"kitten", "sitting", 3},
		{"flaw", "lawn", 2},
		{"same", "same", 0},
	}
	for _, c := range cases {
		// Use a high cap so the short-circuit never truncates the exact distance.
		if got := levenshtein(c.a, c.b, 100); got != c.want {
			t.Errorf("levenshtein(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
