package pipeline_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inth3shadows/runecho/internal/pipeline"
	"github.com/inth3shadows/runecho/internal/schema"
)

// --- Validate ---

func TestValidate_OK(t *testing.T) {
	p := pipeline.DefaultPipeline()
	if err := pipeline.Validate(p); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestValidate_EmptyName(t *testing.T) {
	p := pipeline.DefaultPipeline()
	p.Name = ""
	if err := pipeline.Validate(p); err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestValidate_NoStages(t *testing.T) {
	p := &pipeline.Pipeline{Name: "test", Stages: nil}
	if err := pipeline.Validate(p); err == nil {
		t.Fatal("expected error for empty stages")
	}
}

func TestValidate_InvalidModel(t *testing.T) {
	p := &pipeline.Pipeline{
		Name: "test",
		Stages: []pipeline.Stage{
			{ID: "step1", Model: "gpt-4"},
		},
	}
	if err := pipeline.Validate(p); err == nil {
		t.Fatal("expected error for invalid model")
	}
}

func TestValidate_MissingStageID(t *testing.T) {
	p := &pipeline.Pipeline{
		Name: "test",
		Stages: []pipeline.Stage{
			{ID: "", Model: "haiku"},
		},
	}
	if err := pipeline.Validate(p); err == nil {
		t.Fatal("expected error for missing stage id")
	}
}

// --- Load ---

func TestLoad_DefaultFallback(t *testing.T) {
	// When no default.yaml exists, Load returns DefaultPipeline.
	dir := t.TempDir()
	p, err := pipeline.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if p.Name != "default" {
		t.Errorf("expected name=default, got %q", p.Name)
	}
	if len(p.Stages) != 3 {
		t.Errorf("expected 3 stages, got %d", len(p.Stages))
	}
}

func TestLoadNamed_FromFile(t *testing.T) {
	dir := t.TempDir()
	yamlContent := `name: custom
stages:
  - id: build
    model: haiku
    description: "Run build"
  - id: review
    model: opus
    description: "Review output"
`
	pipDir := filepath.Join(dir, ".ai", "pipelines")
	if err := os.MkdirAll(pipDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pipDir, "custom.yaml"), []byte(yamlContent), 0o644); err != nil {
		t.Fatal(err)
	}

	p, err := pipeline.LoadNamed(dir, "custom")
	if err != nil {
		t.Fatalf("LoadNamed: %v", err)
	}
	if p.Name != "custom" {
		t.Errorf("expected name=custom, got %q", p.Name)
	}
	if len(p.Stages) != 2 {
		t.Errorf("expected 2 stages, got %d", len(p.Stages))
	}
}

func TestLoadNamed_MissingNonDefault(t *testing.T) {
	dir := t.TempDir()
	_, err := pipeline.LoadNamed(dir, "nonexistent")
	if err == nil {
		t.Fatal("expected error for missing non-default pipeline")
	}
}

// --- RenderText ---

func TestRenderText_DefaultEquivalence(t *testing.T) {
	p := pipeline.DefaultPipeline()
	text := pipeline.RenderText(p)

	// Must contain the header.
	if !strings.Contains(text, "MODEL ROUTER — MULTI-STEP PIPELINE:") {
		t.Error("missing header")
	}
	// Must contain all three stage IDs.
	for _, id := range []string{"EXPLORE", "REASON", "IMPLEMENT"} {
		if !strings.Contains(text, id) {
			t.Errorf("missing stage %q in rendered text", id)
		}
	}
	// Must contain model labels.
	for _, label := range []string{"haiku subagents", "opus subagent", "you, Sonnet"} {
		if !strings.Contains(text, label) {
			t.Errorf("missing model label %q in rendered text", label)
		}
	}
	// Must contain the quality trailer.
	if !strings.Contains(text, "maximizes quality") {
		t.Error("missing quality trailer")
	}
}

func TestRenderText_CustomPipeline(t *testing.T) {
	p := &pipeline.Pipeline{
		Name: "mini",
		Stages: []pipeline.Stage{
			{ID: "read", Model: "haiku", Description: "Read the files."},
			{ID: "write", Model: "sonnet", Description: "Write the output."},
		},
	}
	text := pipeline.RenderText(p)
	if strings.Contains(text, "EXPLORE") {
		t.Error("custom pipeline should not contain EXPLORE")
	}
	if !strings.Contains(text, "READ") {
		t.Error("missing READ in custom pipeline text")
	}
	if !strings.Contains(text, "WRITE") {
		t.Error("missing WRITE in custom pipeline text")
	}
}

// --- AppendEnvelope (idempotency) ---

func makeEnvelope(sessionID string) schema.Envelope {
	return schema.Envelope{
		SessionID: sessionID,
		Pipeline:  "default",
		Timestamp: "2026-01-01T00:00:00Z",
		Status:    "complete",
		Faults:    []string{},
		Stages:    []schema.StageResult{},
	}
}

func TestAppendEnvelope_WritesRecord(t *testing.T) {
	dir := t.TempDir()
	env := makeEnvelope("sess-001")

	if err := pipeline.AppendEnvelope(dir, env); err != nil {
		t.Fatalf("AppendEnvelope: %v", err)
	}

	envelopes, err := pipeline.ReadEnvelopes(dir)
	if err != nil {
		t.Fatalf("ReadEnvelopes: %v", err)
	}
	if len(envelopes) != 1 {
		t.Fatalf("expected 1 envelope, got %d", len(envelopes))
	}
	if envelopes[0].SessionID != "sess-001" {
		t.Errorf("wrong session_id: %q", envelopes[0].SessionID)
	}
}

func TestAppendEnvelope_Idempotent(t *testing.T) {
	dir := t.TempDir()
	env := makeEnvelope("sess-dup")

	if err := pipeline.AppendEnvelope(dir, env); err != nil {
		t.Fatalf("first AppendEnvelope: %v", err)
	}
	// Second call should be a no-op.
	if err := pipeline.AppendEnvelope(dir, env); err != nil {
		t.Fatalf("second AppendEnvelope: %v", err)
	}

	envelopes, err := pipeline.ReadEnvelopes(dir)
	if err != nil {
		t.Fatalf("ReadEnvelopes: %v", err)
	}
	if len(envelopes) != 1 {
		t.Errorf("idempotency failed: expected 1 envelope, got %d", len(envelopes))
	}
}

func TestReadEnvelopes_Empty(t *testing.T) {
	dir := t.TempDir()
	envelopes, err := pipeline.ReadEnvelopes(dir)
	if err != nil {
		t.Fatalf("ReadEnvelopes on empty dir: %v", err)
	}
	if len(envelopes) != 0 {
		t.Errorf("expected 0 envelopes, got %d", len(envelopes))
	}
}

// --- FaultsForSession ---

func TestFaultsForSession(t *testing.T) {
	dir := t.TempDir()
	aiDir := filepath.Join(dir, ".ai")
	if err := os.MkdirAll(aiDir, 0o755); err != nil {
		t.Fatal(err)
	}

	lines := []string{
		`{"signal":"IR_DRIFT","session_id":"sess-abc","value":1,"context":"drift","workspace":".","ts":"2026-01-01T00:00:00Z"}`,
		`{"signal":"TURN_FATIGUE","session_id":"sess-abc","value":35,"context":"stop","workspace":".","ts":"2026-01-01T00:01:00Z"}`,
		`{"signal":"COST_FATIGUE","session_id":"sess-other","value":1,"context":"x","workspace":".","ts":"2026-01-01T00:02:00Z"}`,
		// Duplicate signal for same session — should deduplicate.
		`{"signal":"IR_DRIFT","session_id":"sess-abc","value":2,"context":"drift2","workspace":".","ts":"2026-01-01T00:03:00Z"}`,
	}

	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(aiDir, "faults.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	signals := pipeline.FaultsForSession(dir, "sess-abc")
	if len(signals) != 2 {
		t.Errorf("expected 2 unique signals for sess-abc, got %d: %v", len(signals), signals)
	}
	// Should not include signals from other sessions.
	for _, s := range signals {
		if s == "COST_FATIGUE" {
			t.Error("included signal from different session")
		}
	}
}

func TestFaultsForSession_NoFile(t *testing.T) {
	dir := t.TempDir()
	signals := pipeline.FaultsForSession(dir, "sess-x")
	if signals != nil {
		t.Errorf("expected nil, got %v", signals)
	}
}
