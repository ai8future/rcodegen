package bundle

import (
	"fmt"
	"strings"

	"rcodegen/pkg/jev"
)

var reservedResultKeys = map[string]bool{
	"stdout":             true,
	"stderr":             true,
	"model":              true,
	"cost_usd":           true,
	"total_cost_usd":     true,
	"input_tokens":       true,
	"output_tokens":      true,
	"cache_read_tokens":  true,
	"cache_write_tokens": true,
	"output_length":      true,
	"output_truncated":   true,
	"state_truncated":    true,
	"steps":              true,
	"completed":          true,
	"merged":             true,
	"decision":           true,
	"votes":              true,
}

// Validate checks bundle contracts that would otherwise fail after work began.
func (b *Bundle) Validate() error {
	if b == nil {
		return nil
	}
	for i := range b.Steps {
		if err := validateStep(&b.Steps[i], "", false); err != nil {
			return err
		}
	}
	return nil
}

func validateStep(s *Step, parent string, inParallel bool) error {
	label := s.Name
	if parent != "" {
		if label == "" {
			label = parent
		} else {
			label = parent + "/" + label
		}
	}
	if s.Decide != nil {
		if strings.TrimSpace(s.Name) == "" {
			return fmt.Errorf("decide step: name is required")
		}
		if s.Tool != "" || len(s.Parallel) > 0 || s.Merge != nil || s.Vote != nil {
			return fmt.Errorf("step %s: cannot combine decide with tool, parallel, merge, or vote", label)
		}
		if s.Then != nil || s.Else != nil {
			return fmt.Errorf("step %s: cannot combine decide with then/else; put the decide step inside a branch", label)
		}
		if inParallel {
			return fmt.Errorf("step %s: decide steps cannot run inside a parallel group", label)
		}
		if strings.TrimSpace(s.Decide.State) == "" {
			return fmt.Errorf("step %s: decide.state is required", label)
		}
		if err := jev.ValidateQuestions(s.Decide.Questions); err != nil {
			return fmt.Errorf("step %s: decide.%v", label, err)
		}
		for name := range s.Decide.Questions {
			if reservedResultKeys[name] {
				return fmt.Errorf("step %s: question name %q is a reserved result key", label, name)
			}
			if strings.HasSuffix(name, "_confidence") {
				return fmt.Errorf("step %s: question name %q collides with derived _confidence keys", label, name)
			}
			if strings.HasSuffix(name, "_probabilities") {
				return fmt.Errorf("step %s: question name %q collides with derived _probabilities keys", label, name)
			}
		}
	}
	for i := range s.Parallel {
		if err := validateStep(&s.Parallel[i], label, true); err != nil {
			return err
		}
	}
	if s.Then != nil {
		if err := validateStep(s.Then, label, inParallel); err != nil {
			return err
		}
	}
	if s.Else != nil {
		if err := validateStep(s.Else, label, inParallel); err != nil {
			return err
		}
	}
	return nil
}
