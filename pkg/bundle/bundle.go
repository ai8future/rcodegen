package bundle

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
	Tool  string `json:"tool,omitempty"`  // claude, gemini, codex
	Model string `json:"model,omitempty"`
	Task  string `json:"task,omitempty"`

	// Parallel execution
	Parallel []Step `json:"parallel,omitempty"`

	// Merge outputs
	Merge *MergeDef `json:"merge,omitempty"`

	// Vote/ensemble
	Vote *VoteDef `json:"vote,omitempty"`

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
