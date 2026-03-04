package pipeline

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Load reads .ai/pipelines/default.yaml from root.
// Falls back to DefaultPipeline() if the file is missing.
func Load(root string) (*Pipeline, error) {
	return LoadNamed(root, "default")
}

// LoadNamed reads .ai/pipelines/{name}.yaml from root.
// Falls back to DefaultPipeline() if name == "default" and the file is missing.
func LoadNamed(root, name string) (*Pipeline, error) {
	path := filepath.Join(root, ".ai", "pipelines", name+".yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) && name == "default" {
			return DefaultPipeline(), nil
		}
		return nil, fmt.Errorf("pipeline: load %q: %w", path, err)
	}
	var p Pipeline
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("pipeline: parse %q: %w", path, err)
	}
	if err := Validate(&p); err != nil {
		return nil, err
	}
	return &p, nil
}

// DefaultPipeline returns the standard haiku→opus→sonnet pipeline.
// This is the programmatic equivalent of the default.yaml file and matches
// the hardcoded routeText[RoutePipeline] in the governor.
func DefaultPipeline() *Pipeline {
	return &Pipeline{
		Name:        "default",
		Description: "Standard haiku→opus→sonnet pipeline for multi-step implementation tasks",
		Stages: []Stage{
			{
				ID:          "explore",
				Model:       "haiku",
				Description: "Search codebase, read files, gather context. Launch in parallel where possible.",
			},
			{
				ID:          "reason",
				Model:       "opus",
				Description: "Feed exploration results into a single opus subagent for architecture/design decisions. Opus returns the plan and key decisions.",
			},
			{
				ID:          "implement",
				Model:       "sonnet",
				Description: "Write the code yourself based on Opus's design. You have the exploration results and the design in your context.",
			},
		},
	}
}
