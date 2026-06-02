package guard

import (
	"testing"
	"time"
)

func TestTextToAddedLines(t *testing.T) {
	ls := TextToAddedLines("foo\nbar\nbaz")
	if len(ls) != 3 {
		t.Fatalf("len = %d, want 3", len(ls))
	}
	cases := []struct {
		no   int
		text string
	}{
		{1, "foo"},
		{2, "bar"},
		{3, "baz"},
	}
	for i, c := range cases {
		if ls[i].LineNo != c.no || ls[i].Text != c.text {
			t.Errorf("line[%d] = %+v, want {%d, %q}", i, ls[i], c.no, c.text)
		}
	}
}

func TestTextToAddedLines_Empty(t *testing.T) {
	ls := TextToAddedLines("")
	if len(ls) != 1 || ls[0].Text != "" {
		t.Errorf("empty string: got %v", ls)
	}
}

func TestParseMaxAge_Default(t *testing.T) {
	t.Setenv("RUNECHO_GUARD_MAX_AGE", "")
	d, err := ParseMaxAge()
	if err != nil {
		t.Fatal(err)
	}
	if d != 24*time.Hour {
		t.Errorf("default: got %v, want 24h", d)
	}
}

func TestParseMaxAge_Custom(t *testing.T) {
	t.Setenv("RUNECHO_GUARD_MAX_AGE", "48h")
	d, err := ParseMaxAge()
	if err != nil {
		t.Fatal(err)
	}
	if d != 48*time.Hour {
		t.Errorf("custom: got %v, want 48h", d)
	}
}

func TestParseMaxAge_Invalid(t *testing.T) {
	t.Setenv("RUNECHO_GUARD_MAX_AGE", "notaduration")
	_, err := ParseMaxAge()
	if err == nil {
		t.Error("expected error for invalid duration, got nil")
	}
}
