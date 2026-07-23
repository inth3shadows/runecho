package guard

import "testing"

func TestSuggest_CaseSlip(t *testing.T) {
	known := map[string]struct{}{"GetUserByID": {}, "DeleteUser": {}}
	got, ok := Suggest("getUserByID", known)
	if !ok || got[0] != "GetUserByID" {
		t.Fatalf("Suggest = %q,%v; want GetUserByID first,true", got, ok)
	}
}

func TestSuggest_OneEdit(t *testing.T) {
	known := map[string]struct{}{"FetchUser": {}, "FlushCache": {}}
	got, ok := Suggest("FetchUserr", known) // doubled trailing char, dist 1
	if !ok || got[0] != "FetchUser" {
		t.Fatalf("Suggest = %q,%v; want FetchUser first,true", got, ok)
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
		if !ok || got[0] != "Abc" {
			t.Fatalf("Suggest = %q,%v; want Abc first,true (deterministic tie)", got, ok)
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
	if len(violations[0].Suggestions) == 0 || violations[0].Suggestions[0] != "ProcessOrder" {
		t.Errorf("Suggestions = %q, want ProcessOrder first", violations[0].Suggestions)
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

// TestSuggest_MultipleCandidates pins the #200 behaviour: several near names are
// offered, nearest first, capped — a single suggestion made the agent guess when
// two names were equally plausible.
func TestSuggest_MultipleCandidates(t *testing.T) {
	known := map[string]struct{}{
		"ProcessOrder": {}, "ProcessOrders": {}, "ProcessOrdur": {},
		"RenderTemplate": {},
	}
	got, ok := Suggest("ProcesOrder", known)
	if !ok {
		t.Fatal("expected suggestions")
	}
	// ProcessOrder is distance 1; the other two are distance 2 and tie-break
	// lexicographically. RenderTemplate is far and must not appear.
	want := []string{"ProcessOrder", "ProcessOrders", "ProcessOrdur"}
	if len(got) != len(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %q, want %q (nearest first, then lexicographic)", got, want)
		}
	}
}

// TestSuggest_CapsCandidates: more than the cap must be truncated, not dumped.
func TestSuggest_CapsCandidates(t *testing.T) {
	known := map[string]struct{}{
		"handlerA": {}, "handlerB": {}, "handlerC": {}, "handlerD": {}, "handlerE": {},
	}
	got, _ := Suggest("handlerX", known)
	if len(got) > suggestMaxCandidates {
		t.Errorf("got %d suggestions, want at most %d", len(got), suggestMaxCandidates)
	}
}

// TestSuggest_ShortNameFloor exercises withinFloor specifically — cases that
// pass suggestMaxDist and are rejected (or kept) only because of the relative
// floor. A case that maxDist already rejects proves nothing about the floor.
func TestSuggest_ShortNameFloor(t *testing.T) {
	// Distance 2 on a 3-char name: within maxDist, but two edits have changed
	// most of the name, so it is a different name rather than a typo of this one.
	if got, ok := Suggest("abc", map[string]struct{}{"xyc": {}}); ok {
		t.Errorf("distance-2 match on a 3-char name should be rejected, got %q", got)
	}
	// Distance 1 on the same 3-char name: a genuine single-edit slip, kept.
	if got, ok := Suggest("abc", map[string]struct{}{"abd": {}}); !ok || got[0] != "abd" {
		t.Errorf("abc -> abd = %q,%v; want abd kept", got, ok)
	}
	// Distance 2 on a 4-char name is back above the floor and stays.
	if got, ok := Suggest("abcd", map[string]struct{}{"abxy": {}}); !ok || got[0] != "abxy" {
		t.Errorf("abcd -> abxy = %q,%v; want kept (floor only bites below 4 chars)", got, ok)
	}
}
