package contract

import (
	"fmt"
	"path/filepath"
)

// ValidationError describes a single contract validation failure.
type ValidationError struct {
	Field   string
	Message string
}

func (e ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

// Validate checks a Contract for required fields and valid glob patterns.
// Returns nil when the contract is valid.
func Validate(c *Contract) []ValidationError {
	var errs []ValidationError

	if c.Title == "" {
		errs = append(errs, ValidationError{Field: "title", Message: "required"})
	}

	if len(c.Scope) == 0 && c.Verify == "" {
		errs = append(errs, ValidationError{
			Field:   "scope/verify",
			Message: "at least one of scope or verify must be set",
		})
	}

	for i, glob := range c.Scope {
		if err := validateGlob(glob); err != nil {
			errs = append(errs, ValidationError{
				Field:   fmt.Sprintf("scope[%d]", i),
				Message: fmt.Sprintf("invalid glob %q: %v", glob, err),
			})
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return errs
}

// validateGlob does a dry-run of filepath.Match to catch syntax errors.
func validateGlob(pattern string) error {
	_, err := filepath.Match(pattern, "probe")
	return err
}
