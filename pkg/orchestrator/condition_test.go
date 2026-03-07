package orchestrator

import (
	"context"
	"testing"

	"rcodegen/pkg/envelope"
)

func TestEvaluateCondition_Empty(t *testing.T) {
	ctx := NewContext(context.Background(), nil)
	if !EvaluateCondition("", ctx) {
		t.Error("empty condition should return true")
	}
}

func TestEvaluateCondition_WithInputs(t *testing.T) {
	ctx := NewContext(context.Background(), map[string]string{
		"status": "ready",
		"count":  "10",
	})

	tests := []struct {
		name      string
		condition string
		expected  bool
	}{
		{"input equals string", "${inputs.status} == 'ready'", true},
		{"input not equals", "${inputs.status} == 'pending'", false},
		{"input numeric gt", "${inputs.count} > 5", true},
		{"input numeric lt", "${inputs.count} < 5", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := EvaluateCondition(tc.condition, ctx)
			if result != tc.expected {
				t.Errorf("EvaluateCondition(%q) = %v, want %v", tc.condition, result, tc.expected)
			}
		})
	}
}

func TestEvaluateCondition_WithStepResults(t *testing.T) {
	ctx := NewContext(context.Background(), nil)
	ctx.SetResult("analyze", &envelope.Envelope{
		Status: envelope.StatusSuccess,
	})
	ctx.SetResult("build", &envelope.Envelope{
		Status: envelope.StatusFailure,
	})

	tests := []struct {
		name      string
		condition string
		expected  bool
	}{
		{"step success", "${steps.analyze.status} == 'success'", true},
		{"step failure check", "${steps.build.status} == 'failure'", true},
		{"step not success", "${steps.build.status} == 'success'", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := EvaluateCondition(tc.condition, ctx)
			if result != tc.expected {
				t.Errorf("EvaluateCondition(%q) = %v, want %v", tc.condition, result, tc.expected)
			}
		})
	}
}

func TestEvaluate_BooleanLiterals(t *testing.T) {
	tests := []struct {
		expr     string
		expected bool
	}{
		{"true", true},
		{"false", false},
		{"TRUE", false}, // Case sensitive
		{"True", false}, // Case sensitive
		{"", false},     // Empty string is false
	}

	for _, tc := range tests {
		t.Run(tc.expr, func(t *testing.T) {
			result := evaluate(tc.expr)
			if result != tc.expected {
				t.Errorf("evaluate(%q) = %v, want %v", tc.expr, result, tc.expected)
			}
		})
	}
}

func TestEvaluate_Comparisons(t *testing.T) {
	tests := []struct {
		name     string
		expr     string
		expected bool
	}{
		// Equality
		{"quoted eq", "'foo' == 'foo'", true},
		{"quoted neq", "'foo' == 'bar'", false},
		{"unquoted eq", "foo == foo", true},
		{"not equals true", "'foo' != 'bar'", true},
		{"not equals false", "'foo' != 'foo'", false},

		// Numeric comparisons
		{"gt true", "10 > 5", true},
		{"gt false", "5 > 10", false},
		{"lt true", "5 < 10", true},
		{"lt false", "10 < 5", false},
		{"gte equal", "10 >= 10", true},
		{"gte greater", "11 >= 10", true},
		{"gte less", "9 >= 10", false},
		{"lte equal", "10 <= 10", true},
		{"lte less", "9 <= 10", true},
		{"lte greater", "11 <= 10", false},

		// Contains
		{"contains true", "'hello world' contains 'world'", true},
		{"contains false", "'hello world' contains 'foo'", false},

		// Non-numeric comparisons return false
		{"non-numeric gt", "'abc' > 'def'", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := evaluate(tc.expr)
			if result != tc.expected {
				t.Errorf("evaluate(%q) = %v, want %v", tc.expr, result, tc.expected)
			}
		})
	}
}

func TestEvaluate_LogicalOperators(t *testing.T) {
	tests := []struct {
		name     string
		expr     string
		expected bool
	}{
		// AND
		{"and tt", "true AND true", true},
		{"and tf", "true AND false", false},
		{"and ft", "false AND true", false},
		{"and ff", "false AND false", false},

		// OR
		{"or tt", "true OR true", true},
		{"or tf", "true OR false", true},
		{"or ft", "false OR true", true},
		{"or ff", "false OR false", false},

		// Combined with comparisons
		{"and with gt", "10 > 5 AND 20 > 10", true},
		{"and with mixed", "10 > 5 AND 5 > 10", false},
		{"or with gt", "10 > 5 OR 5 > 10", true},
		{"or both false", "5 > 10 OR 3 > 10", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := evaluate(tc.expr)
			if result != tc.expected {
				t.Errorf("evaluate(%q) = %v, want %v", tc.expr, result, tc.expected)
			}
		})
	}
}

func TestEvaluate_OperatorPrecedence(t *testing.T) {
	// AND should bind tighter than OR
	// "A OR B AND C" should be evaluated as "A OR (B AND C)"
	tests := []struct {
		name     string
		expr     string
		expected bool
	}{
		// true OR (false AND false) = true OR false = true
		{"or-and true", "true OR false AND false", true},
		// false OR (true AND true) = false OR true = true
		{"or-and mixed", "false OR true AND true", true},
		// (false AND false) OR true = false OR true = true
		{"and-or", "false AND false OR true", true},
		// false OR (false AND true) = false OR false = false
		{"or-and false", "false OR false AND true", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := evaluate(tc.expr)
			if result != tc.expected {
				t.Errorf("evaluate(%q) = %v, want %v", tc.expr, result, tc.expected)
			}
		})
	}
}

func TestCompare(t *testing.T) {
	tests := []struct {
		left, op, right string
		expected        bool
	}{
		{"hello", "==", "hello", true},
		{"hello", "==", "world", false},
		{"hello", "!=", "world", true},
		{"'quoted'", "==", "quoted", true}, // Quotes stripped
		{"10", ">", "5", true},
		{"abc", ">", "def", false}, // Non-numeric
		{"test string", " contains ", "string", true},
	}

	for _, tc := range tests {
		t.Run(tc.left+tc.op+tc.right, func(t *testing.T) {
			result := compare(tc.left, tc.op, tc.right)
			if result != tc.expected {
				t.Errorf("compare(%q, %q, %q) = %v, want %v", tc.left, tc.op, tc.right, result, tc.expected)
			}
		})
	}
}
