package context

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/inth3shadows/runecho/internal/schema"
)

// VerifyFailureProvider injects a TEST_FAILURE_ADVISORY block when the most
// recent verify run for the current workspace failed. This lets the next
// session immediately focus on reproducing the failure rather than
// re-diagnosing from scratch.
type VerifyFailureProvider struct{}

func (p *VerifyFailureProvider) Name() string { return "verify_failure" }

// Provide scans .ai/results.jsonl from the end, finds the most recent
// VerifyEntry with Passed=false, and formats it as a TEST_FAILURE_ADVISORY
// block. Returns an empty Result if no failing entry exists or the file is
// absent.
func (p *VerifyFailureProvider) Provide(req Request) (Result, error) {
	path := filepath.Join(req.Workspace, ".ai", "results.jsonl")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Result{Name: p.Name()}, nil
		}
		return Result{Name: p.Name()}, err
	}
	defer f.Close()

	// Collect all lines so we can scan from the end.
	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); line != "" {
			lines = append(lines, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return Result{Name: p.Name()}, err
	}

	// Walk backwards to find the most recent failing entry.
	var latest *schema.VerifyEntry
	for i := len(lines) - 1; i >= 0; i-- {
		var e schema.VerifyEntry
		if json.Unmarshal([]byte(lines[i]), &e) != nil {
			continue
		}
		if !e.Passed {
			latest = &e
			break
		}
	}

	if latest == nil {
		return Result{Name: p.Name()}, nil
	}

	// Choose the most informative output: prefer Stderr, fall back to Output.
	detail := latest.Stderr
	if detail == "" {
		detail = latest.Output
	}
	if detail == "" {
		detail = "(no output captured)"
	}

	content := fmt.Sprintf(`## TEST_FAILURE_ADVISORY
**Task:** %s
**Command:** %s
**Exit code:** %d
**Stderr:**
`+"```"+`
%s
`+"```"+`

Haiku advisory: generate a minimal failing test case or reproduction steps targeting this specific failure before attempting the fix.`,
		latest.TaskID,
		latest.Cmd,
		latest.ExitCode,
		detail,
	)

	return Result{
		Name:    p.Name(),
		Content: content,
		Tokens:  estimateTokens(content),
	}, nil
}
