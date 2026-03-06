package pipeline

// Pipeline is a declarative definition of a multi-step model routing pipeline.
type Pipeline struct {
	Name        string  `yaml:"name"`
	Description string  `yaml:"description,omitempty"`
	Stages      []Stage `yaml:"stages"`
}

// Stage is one phase of a pipeline.
type Stage struct {
	ID          string `yaml:"id"`
	Model       string `yaml:"model"`             // haiku | sonnet | opus
	TokenBudget int    `yaml:"token_budget,omitempty"`
	Scope       string `yaml:"scope,omitempty"`   // glob (informational in M5)
	Verify      string `yaml:"verify,omitempty"`  // shell cmd (informational in M5)
	Description string `yaml:"description,omitempty"`
}

