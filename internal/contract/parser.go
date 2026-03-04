package contract

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Parse reads a CONTRACT.yaml file and returns the parsed Contract.
// Returns nil, nil if the file does not exist (absence is not an error).
func Parse(path string) (*Contract, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("contract: read %s: %w", path, err)
	}

	var c Contract
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("contract: parse %s: %w", path, err)
	}
	return &c, nil
}

// Marshal serializes a Contract to YAML bytes.
func Marshal(c *Contract) ([]byte, error) {
	return yaml.Marshal(c)
}

// FromTask builds a Contract from task fields (backward-compat path).
// scope may be a single glob or comma/newline-separated list.
// Title is set to "Task #<id>: <title>".
func FromTask(id, title, scope, verify string) *Contract {
	c := &Contract{
		Title:  fmt.Sprintf("Task #%s: %s", id, title),
		Verify: verify,
	}
	if scope != "" {
		for _, s := range splitScope(scope) {
			if s != "" {
				c.Scope = append(c.Scope, s)
			}
		}
	}
	return c
}

// splitScope splits a scope string on comma or newline, trimming whitespace.
func splitScope(s string) []string {
	s = strings.ReplaceAll(s, "\n", ",")
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
