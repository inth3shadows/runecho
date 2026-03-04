package pipeline

import (
	"errors"
	"fmt"
)

var validModels = map[string]bool{
	"haiku":  true,
	"sonnet": true,
	"opus":   true,
}

// Validate checks that a Pipeline is well-formed.
func Validate(p *Pipeline) error {
	if p.Name == "" {
		return errors.New("pipeline: name is required")
	}
	if len(p.Stages) == 0 {
		return errors.New("pipeline: at least one stage is required")
	}
	for i, s := range p.Stages {
		if s.ID == "" {
			return fmt.Errorf("pipeline: stage[%d]: id is required", i)
		}
		if !validModels[s.Model] {
			return fmt.Errorf("pipeline: stage %q: model %q is not valid (use haiku, sonnet, or opus)", s.ID, s.Model)
		}
	}
	return nil
}
