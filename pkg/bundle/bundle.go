package bundle

import "rcodegen/pkg/jev"

type Bundle struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Inputs      []Input `json:"inputs,omitempty"`
	Steps       []Step  `json:"steps"`
	SourcePath  string  `json:"-"` // Path to bundle file (not serialized)
}

type Input struct {
	Name        string `json:"name"`
	Required    bool   `json:"required"`
	Description string `json:"description,omitempty"`
	Default     string `json:"default,omitempty"`
}

type Step struct {
	Name string `json:"name"`

	// Tool execution
	Tool   string `json:"tool,omitempty"` // CLI tools plus ollama/lmstudio API namespaces
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
	Task   string `json:"task,omitempty"`

	// Parallel execution
	Parallel []Step `json:"parallel,omitempty"`

	// Merge outputs
	Merge *MergeDef `json:"merge,omitempty"`

	// Vote/ensemble
	Vote *VoteDef `json:"vote,omitempty"`

	// Decision gate
	Decide *DecideDef `json:"decide,omitempty"`

	// Conditional
	If   string `json:"if,omitempty"`
	Then *Step  `json:"then,omitempty"`
	Else *Step  `json:"else,omitempty"`

	// Output
	Save string `json:"save,omitempty"`
}

type MergeDef struct {
	Inputs   []string `json:"inputs"`
	Strategy string   `json:"strategy"` // concat, union, dedupe
}

type VoteDef struct {
	Inputs   []string `json:"inputs"`
	Strategy string   `json:"strategy"` // majority, unanimous, ranked
}

// DecideDef configures a Jev decision step.
type DecideDef struct {
	State     string                  `json:"state"`
	Questions map[string]jev.Question `json:"questions"`
	Model     string                  `json:"model,omitempty"`
}

// ToolLabel names the execution surface for displays, events, and cost grouping.
func (s Step) ToolLabel() string {
	switch {
	case s.Tool != "":
		return s.Tool
	case s.Decide != nil:
		return "jev"
	case len(s.Parallel) > 0:
		return "parallel"
	default:
		return ""
	}
}

// ModelHint returns the explicit model requested by the step, wherever it lives.
func (s Step) ModelHint() string {
	if s.Decide != nil {
		return s.Decide.Model
	}
	return s.Model
}
