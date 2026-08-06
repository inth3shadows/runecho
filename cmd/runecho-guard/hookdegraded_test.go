// hookdegraded_test.go — the three arms of answerDegradedStore.
//
// This branch was already exercised end-to-end through runHookMode, but only its
// SIDE EFFECTS were: the JSON on the wire and the decision-log reason. Its
// ask/defer classification — the bool — was asserted nowhere, so the extraction's
// doc comment claimed an assertability that did not exist. These pin it directly.
//
// The three arms answer three different degraded states and must stay
// distinguishable: schema-newer is loud regardless of strict mode, an unenrolled
// tree is silent by design, and a store that opened but has no usable snapshot is
// silent unless strict. Collapsing any two would be invisible to a test that only
// checked "the guard did not ask".
package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/inth3shadows/runecho/internal/guard"
)

func TestAnswerDegradedStore_DeferArms(t *testing.T) {
	cases := []struct {
		name       string
		res        lookupResult
		strict     bool
		wantReason string
		wantRepo   string
		// wantContext is a substring the additionalContext must carry, or ""
		// when the arm must stay silent.
		wantContext string
	}{
		{
			name:        "schema-newer is loud even without strict",
			res:         lookupResult{Warn: "store was written by a newer runecho", RepoName: "r"},
			wantReason:  "schema-newer",
			wantRepo:    "r",
			wantContext: "newer runecho",
		},
		{
			name:       "unenrolled tree is silent by design",
			res:        lookupResult{NoRepo: true},
			wantReason: "no-repo",
		},
		{
			name:       "degraded store is silent unless strict",
			res:        lookupResult{RepoName: "r"},
			wantReason: "store-degraded",
			wantRepo:   "r",
		},
		{
			name:        "degraded store under strict says validation is off",
			res:         lookupResult{RepoName: "r"},
			strict:      true,
			wantReason:  "store-degraded",
			wantRepo:    "r",
			wantContext: "validation",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("RUNECHO_HOME", t.TempDir())
			if tc.strict {
				t.Setenv("RUNECHO_GUARD_STRICT", "1")
			} else {
				t.Setenv("RUNECHO_GUARD_STRICT", "")
			}
			var out bytes.Buffer
			asked := answerDegradedStore(&out, tc.res,
				hookEdit{ToolName: "Edit", NewString: "x = compute()\n"},
				"/tmp/whatever/a.py", guard.LangPython, "")

			if asked {
				t.Errorf("asked = true; no contract and no call-shape finding, so this arm must defer")
			}
			rec := readLastDecisionLog(t)
			if rec == nil {
				t.Fatal("no decision record written")
			}
			if got := rec["reason"]; got != tc.wantReason {
				t.Errorf("logged reason = %v, want %q", got, tc.wantReason)
			}
			if got := rec["decision"]; got != "defer" {
				t.Errorf("logged decision = %v, want \"defer\"", got)
			}
			// The repo name distinguishes "not enrolled" from "enrolled but
			// degraded" in the log, which is what makes the two arms tellable
			// apart after the fact.
			if tc.wantRepo == "" {
				if _, ok := rec["repo"]; ok {
					t.Errorf("no-repo arm logged a repo name: %v", rec["repo"])
				}
			} else if got := rec["repo"]; got != tc.wantRepo {
				t.Errorf("logged repo = %v, want %q", got, tc.wantRepo)
			}

			ctx := additionalContextOf(t, out.String())
			if tc.wantContext == "" {
				if ctx != "" {
					t.Errorf("arm must stay silent but wrote context: %q", ctx)
				}
				return
			}
			if !strings.Contains(strings.ToLower(ctx), strings.ToLower(tc.wantContext)) {
				t.Errorf("additionalContext = %q, want it to mention %q", ctx, tc.wantContext)
			}
		})
	}
}

// additionalContextOf pulls the advisory out of a PreToolUse hook response, or
// "" when the guard wrote nothing.
func additionalContextOf(t *testing.T, raw string) string {
	t.Helper()
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var resp struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("hook response is not JSON: %q", raw)
	}
	return resp.HookSpecificOutput.AdditionalContext
}
