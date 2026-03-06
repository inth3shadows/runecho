package contract

// Contract is a machine-readable session contract scoped to a task or
// work unit. It can be authored by Claude or generated via `ai-task contract`.
// The canonical file path is <workspace>/.ai/CONTRACT.yaml.
type Contract struct {
	Title       string   `yaml:"title"`
	Scope       []string `yaml:"scope"`                  // glob patterns for allowed file paths
	Verify      string   `yaml:"verify"`                 // shell command to validate completion
	Assumptions []string `yaml:"assumptions,omitempty"`  // explicit assumptions
	NonGoals    []string `yaml:"non_goals,omitempty"`    // explicitly out of scope
	Success     []string `yaml:"success,omitempty"`      // measurable success criteria
}
