// Package jev is a minimal client for TypeSafe's Jev decision model
// (System One API). It validates questions, scrubs and caps state, performs
// the upstream POST with retries, and prices the call.
package jev

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Question types accepted by the System One API.
const (
	TypeNoul   = "noul"
	TypeChoice = "choice"
	TypeScore  = "score"
)

const (
	maxQuestions      = 32
	maxChoiceOptions  = 255
	minChoiceOptions  = 2
	minScoreLevels    = 2
	maxScoreLevels    = 10
	maxInstructionLen = 4096

	// MaxQuestionBytes bounds one question's instructions plus criteria, and
	// MaxQuestionsTotalBytes bounds all of them together.
	MaxQuestionBytes       = 16 << 10
	MaxQuestionsTotalBytes = 48 << 10
)

var questionNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

// ErrInvalidResponse marks an upstream reply that parsed but does not answer
// the questions asked. Callers treat it as a failed decision, never as data.
var ErrInvalidResponse = errors.New("jev: invalid response")

// Question is one typed question. Criteria is passed upstream verbatim:
// choice expects an object of option name to description, score expects an
// array of level descriptions, and noul accepts optional true/false text.
type Question struct {
	Type         string          `json:"type"`
	Instructions string          `json:"instructions"`
	Criteria     json.RawMessage `json:"criteria,omitempty"`
}

// Request is the upstream request body.
type Request struct {
	Model     string              `json:"model"`
	State     string              `json:"state"`
	Questions map[string]Question `json:"questions"`
}

// ProbabilityMap rejects null entries instead of silently decoding them as zero.
type ProbabilityMap map[string]float64

func (p *ProbabilityMap) UnmarshalJSON(data []byte) error {
	var raw map[string]*float64
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw == nil {
		*p = nil
		return nil
	}
	values := make(ProbabilityMap, len(raw))
	for key, value := range raw {
		if value == nil {
			return fmt.Errorf("probability %q must not be null", key)
		}
		values[key] = *value
	}
	*p = values
	return nil
}

// Answer is one upstream answer. Only the fields for the question's type are populated.
type Answer struct {
	Type          string            `json:"type"`
	Noul          *float64          `json:"noul,omitempty"`
	Choice        string            `json:"choice,omitempty"`
	Score         json.RawMessage   `json:"score,omitempty"`
	Legend        map[string]string `json:"legend,omitempty"`
	Probabilities ProbabilityMap    `json:"probabilities,omitempty"`
	Confidence    *float64          `json:"confidence,omitempty"`
}

// Usage is the upstream token accounting. Output tokens are free.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Response is the upstream response body.
type Response struct {
	Model   string            `json:"model"`
	Answers map[string]Answer `json:"answers"`
	Usage   Usage             `json:"usage"`
}

// Value renders the primary answer as a plain string for bundle conditions.
func (a Answer) Value() string {
	switch a.Type {
	case TypeNoul:
		if a.Noul == nil {
			return ""
		}
		return strconv.FormatFloat(*a.Noul, 'f', 4, 64)
	case TypeChoice:
		return a.Choice
	case TypeScore:
		v, ok := a.scoreValue()
		if !ok {
			return ""
		}
		return strconv.FormatFloat(v, 'f', 4, 64)
	default:
		return ""
	}
}

func (a Answer) scoreValue() (float64, bool) {
	var v *float64
	if len(a.Score) == 0 || json.Unmarshal(a.Score, &v) != nil || v == nil {
		return 0, false
	}
	return *v, true
}

// QuestionBytes is the size StateBudget and ValidateQuestions account for.
func QuestionBytes(q Question) int {
	return len(q.Instructions) + len(q.Criteria)
}

// ValidateQuestions enforces the upstream schema locally so a bad bundle or
// request fails before any tokens are spent.
func ValidateQuestions(qs map[string]Question) error {
	if len(qs) == 0 {
		return fmt.Errorf("questions: at least one question is required")
	}
	if len(qs) > maxQuestions {
		return fmt.Errorf("questions: at most %d questions per call", maxQuestions)
	}
	total := 0
	for name, q := range qs {
		if !questionNamePattern.MatchString(name) {
			return fmt.Errorf("question name %q: must match %s", name, questionNamePattern)
		}
		if strings.TrimSpace(q.Instructions) == "" {
			return fmt.Errorf("question %q: instructions are required", name)
		}
		if len(q.Instructions) > maxInstructionLen {
			return fmt.Errorf("question %q: instructions exceed %d bytes", name, maxInstructionLen)
		}
		n := QuestionBytes(q)
		if n > MaxQuestionBytes {
			return fmt.Errorf("question %q: instructions plus criteria are %d bytes; the limit is %d", name, n, MaxQuestionBytes)
		}
		total += n
		switch q.Type {
		case TypeNoul:
			if len(q.Criteria) > 0 {
				var c map[string]string
				if err := json.Unmarshal(q.Criteria, &c); err != nil {
					return fmt.Errorf("question %q: noul criteria must be an object of true/false to text", name)
				}
				for k := range c {
					if k != "true" && k != "false" {
						return fmt.Errorf("question %q: noul criteria keys must be \"true\" or \"false\"", name)
					}
				}
			}
		case TypeChoice:
			var c map[string]string
			if len(q.Criteria) == 0 || json.Unmarshal(q.Criteria, &c) != nil {
				return fmt.Errorf("question %q: choice criteria must be an object of option to description", name)
			}
			if len(c) < minChoiceOptions || len(c) > maxChoiceOptions {
				return fmt.Errorf("question %q: choice needs %d to %d options, got %d", name, minChoiceOptions, maxChoiceOptions, len(c))
			}
		case TypeScore:
			var levels []string
			if len(q.Criteria) == 0 || json.Unmarshal(q.Criteria, &levels) != nil {
				return fmt.Errorf("question %q: score criteria must be an array of level descriptions", name)
			}
			if len(levels) < minScoreLevels || len(levels) > maxScoreLevels {
				return fmt.Errorf("question %q: score needs %d to %d levels, got %d", name, minScoreLevels, maxScoreLevels, len(levels))
			}
		default:
			return fmt.Errorf("question %q: type must be noul, choice, or score", name)
		}
	}
	if total > MaxQuestionsTotalBytes {
		return fmt.Errorf("questions: %d bytes across all questions; the limit is %d", total, MaxQuestionsTotalBytes)
	}
	return nil
}

// ValidateResponse checks that every requested question has an answer of the right type.
func ValidateResponse(qs map[string]Question, r *Response) error {
	if r == nil || len(r.Answers) == 0 {
		return fmt.Errorf("%w: no answers", ErrInvalidResponse)
	}
	for name, q := range qs {
		a, ok := r.Answers[name]
		if !ok {
			return fmt.Errorf("%w: no answer for question %q", ErrInvalidResponse, name)
		}
		if a.Type != q.Type {
			return fmt.Errorf("%w: question %q asked %s, answer type is %q", ErrInvalidResponse, name, q.Type, a.Type)
		}
		for opt, p := range a.Probabilities {
			if p < 0 || p > 1 {
				return fmt.Errorf("%w: question %q probability for %q is %v, expected 0 to 1", ErrInvalidResponse, name, opt, p)
			}
		}
		if a.Confidence != nil && (*a.Confidence < 0 || *a.Confidence > 1) {
			return fmt.Errorf("%w: question %q confidence %v is outside 0 to 1", ErrInvalidResponse, name, *a.Confidence)
		}
		switch q.Type {
		case TypeNoul:
			if a.Noul == nil {
				return fmt.Errorf("%w: question %q has no noul value", ErrInvalidResponse, name)
			}
			if *a.Noul < 0 || *a.Noul > 1 {
				return fmt.Errorf("%w: question %q noul %v is outside 0 to 1", ErrInvalidResponse, name, *a.Noul)
			}
		case TypeChoice:
			if a.Choice == "" {
				return fmt.Errorf("%w: question %q has an empty choice", ErrInvalidResponse, name)
			}
			options := choiceOptions(q)
			if !contains(options, a.Choice) {
				return fmt.Errorf("%w: question %q choice %q is not one of the supplied options", ErrInvalidResponse, name, a.Choice)
			}
			if err := requireDistribution(name, a, options); err != nil {
				return err
			}
		case TypeScore:
			v, ok := a.scoreValue()
			if !ok {
				return fmt.Errorf("%w: question %q score is missing or not numeric", ErrInvalidResponse, name)
			}
			levels := scoreLevelKeys(q)
			top := float64(len(levels) - 1)
			if v < 0 || v > top {
				return fmt.Errorf("%w: question %q score %v is outside 0 to %v", ErrInvalidResponse, name, v, top)
			}
			if err := requireDistribution(name, a, levels); err != nil {
				return err
			}
		}
	}
	return nil
}

func requireDistribution(name string, a Answer, expected []string) error {
	if len(a.Probabilities) == 0 {
		return fmt.Errorf("%w: question %q answer has no probabilities", ErrInvalidResponse, name)
	}
	if a.Confidence == nil {
		return fmt.Errorf("%w: question %q answer has no confidence", ErrInvalidResponse, name)
	}
	if len(a.Probabilities) != len(expected) {
		return fmt.Errorf("%w: question %q probabilities cover %d keys, expected %d", ErrInvalidResponse, name, len(a.Probabilities), len(expected))
	}
	sum := 0.0
	for _, key := range expected {
		p, ok := a.Probabilities[key]
		if !ok {
			return fmt.Errorf("%w: question %q probabilities are missing key %q", ErrInvalidResponse, name, key)
		}
		sum += p
	}
	if sum < 0.98 || sum > 1.02 {
		return fmt.Errorf("%w: question %q probabilities sum to %.3f, expected 1", ErrInvalidResponse, name, sum)
	}
	return nil
}

func choiceOptions(q Question) []string {
	var options map[string]string
	_ = json.Unmarshal(q.Criteria, &options)
	keys := make([]string, 0, len(options))
	for k := range options {
		keys = append(keys, k)
	}
	return keys
}

func scoreLevelKeys(q Question) []string {
	var levels []json.RawMessage
	_ = json.Unmarshal(q.Criteria, &levels)
	keys := make([]string, len(levels))
	for i := range levels {
		keys[i] = strconv.Itoa(i)
	}
	return keys
}

func contains(values []string, needle string) bool {
	for _, v := range values {
		if v == needle {
			return true
		}
	}
	return false
}
