package contract_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/inth3shadows/runecho/internal/contract"
	"gopkg.in/yaml.v3"
)

// roundTripYAML writes a Contract to a temp file and parses it back.
func roundTripYAML(t *testing.T, c *contract.Contract) *contract.Contract {
	t.Helper()
	data, err := contract.Marshal(c)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "CONTRACT.yaml")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := contract.Parse(path)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return got
}

func TestRoundTrip_ListScope(t *testing.T) {
	orig := &contract.Contract{
		Title:       "Task #1: Add auth",
		Scope:       []string{"internal/auth/**", "cmd/auth/**"},
		Verify:      "go test ./internal/auth/...",
		Assumptions: []string{"JWT is acceptable"},
		NonGoals:    []string{"OAuth integration"},
		Success:     []string{"All auth tests pass"},
	}
	got := roundTripYAML(t, orig)

	if got.Title != orig.Title {
		t.Errorf("Title: got %q want %q", got.Title, orig.Title)
	}
	if len(got.Scope) != 2 {
		t.Fatalf("Scope len: got %d want 2", len(got.Scope))
	}
	if got.Scope[0] != "internal/auth/**" || got.Scope[1] != "cmd/auth/**" {
		t.Errorf("Scope: got %v", got.Scope)
	}
	if got.Verify != orig.Verify {
		t.Errorf("Verify: got %q want %q", got.Verify, orig.Verify)
	}
	if len(got.Assumptions) != 1 || got.Assumptions[0] != "JWT is acceptable" {
		t.Errorf("Assumptions: got %v", got.Assumptions)
	}
	if len(got.NonGoals) != 1 || got.NonGoals[0] != "OAuth integration" {
		t.Errorf("NonGoals: got %v", got.NonGoals)
	}
}

func TestFromTask_SingleScope(t *testing.T) {
	c := contract.FromTask("2", "Fix parser", "internal/parser/**", "go test ./internal/parser/...")
	if c.Title != "Task #2: Fix parser" {
		t.Errorf("Title: %q", c.Title)
	}
	if len(c.Scope) != 1 || c.Scope[0] != "internal/parser/**" {
		t.Errorf("Scope: %v", c.Scope)
	}
}

func TestFromTask_CSVScope(t *testing.T) {
	c := contract.FromTask("3", "Refactor", "internal/context/**, cmd/context/**", "go test ./...")
	if len(c.Scope) != 2 {
		t.Fatalf("Scope len: got %d want 2", len(c.Scope))
	}
	if c.Scope[0] != "internal/context/**" || c.Scope[1] != "cmd/context/**" {
		t.Errorf("Scope: %v", c.Scope)
	}
}

func TestFromTask_NewlineScope(t *testing.T) {
	c := contract.FromTask("4", "Big task", "internal/ir/**\ncmd/ir/**", "go build ./...")
	if len(c.Scope) != 2 {
		t.Fatalf("Scope len: got %d want 2, scope=%v", len(c.Scope), c.Scope)
	}
}

func TestValidate_MissingTitle(t *testing.T) {
	c := &contract.Contract{
		Scope:  []string{"internal/**"},
		Verify: "go test ./...",
	}
	errs := contract.Validate(c)
	if len(errs) == 0 {
		t.Fatal("expected validation error for missing title")
	}
	if errs[0].Field != "title" {
		t.Errorf("expected field=title, got %q", errs[0].Field)
	}
}

func TestValidate_InvalidGlob(t *testing.T) {
	c := &contract.Contract{
		Title: "Bad glob",
		Scope: []string{"internal/[invalid"},
	}
	errs := contract.Validate(c)
	if len(errs) == 0 {
		t.Fatal("expected validation error for invalid glob")
	}
	found := false
	for _, e := range errs {
		if e.Field == "scope[0]" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected scope[0] error, got: %v", errs)
	}
}

func TestValidate_ValidMinimal(t *testing.T) {
	c := &contract.Contract{
		Title:  "Minimal",
		Verify: "go test ./...",
	}
	errs := contract.Validate(c)
	if errs != nil {
		t.Errorf("expected nil errors, got: %v", errs)
	}
}

func TestValidate_NoScopeNoVerify(t *testing.T) {
	c := &contract.Contract{Title: "Empty contract"}
	errs := contract.Validate(c)
	if len(errs) == 0 {
		t.Fatal("expected validation error for missing scope and verify")
	}
}

func TestParse_MissingFile(t *testing.T) {
	got, err := contract.Parse("/nonexistent/path/CONTRACT.yaml")
	if err != nil {
		t.Fatalf("expected nil error for missing file, got: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil contract for missing file, got: %+v", got)
	}
}

func TestParse_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CONTRACT.yaml")
	if err := os.WriteFile(path, []byte(":\tinvalid:yaml:[\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := contract.Parse(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

// TestMarshal_OmitsEmptyOptionals ensures optional fields don't appear when empty.
func TestMarshal_OmitsEmptyOptionals(t *testing.T) {
	c := &contract.Contract{
		Title:  "Minimal",
		Scope:  []string{"internal/**"},
		Verify: "go test ./...",
	}
	data, err := contract.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]interface{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"assumptions", "non_goals", "success"} {
		if _, ok := raw[key]; ok {
			t.Errorf("expected %q to be omitted, but it was present in output", key)
		}
	}
}
