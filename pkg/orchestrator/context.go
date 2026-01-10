package orchestrator

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"rcodegen/pkg/envelope"
)

type Context struct {
	Inputs      map[string]string
	StepResults map[string]*envelope.Envelope
	Variables   map[string]string
}

func NewContext(inputs map[string]string) *Context {
	return &Context{
		Inputs:      inputs,
		StepResults: make(map[string]*envelope.Envelope),
		Variables:   make(map[string]string),
	}
}

var varPattern = regexp.MustCompile(`\$\{([^}]+)\}`)

func (c *Context) Resolve(s string) string {
	return varPattern.ReplaceAllStringFunc(s, func(match string) string {
		ref := match[2 : len(match)-1] // Strip ${ and }
		parts := strings.Split(ref, ".")

		switch parts[0] {
		case "inputs":
			if len(parts) >= 2 {
				if v, ok := c.Inputs[parts[1]]; ok {
					return v
				}
			}
		case "steps":
			if len(parts) >= 3 {
				stepName := parts[1]
				if env, ok := c.StepResults[stepName]; ok {
					switch parts[2] {
					case "output_ref":
						return env.OutputRef
					case "status":
						return string(env.Status)
					case "result":
						if len(parts) == 3 {
							if b, err := json.Marshal(env.Result); err == nil {
								return string(b)
							}
						} else if len(parts) >= 4 {
							if v, ok := env.Result[parts[3]]; ok {
								return fmt.Sprintf("%v", v)
							}
						}
					}
				}
			}
		}
		return match // Leave unresolved
	})
}

func (c *Context) SetResult(name string, env *envelope.Envelope) {
	c.StepResults[name] = env
}
