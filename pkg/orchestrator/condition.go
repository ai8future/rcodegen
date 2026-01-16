package orchestrator

import (
	"strconv"
	"strings"
)

func EvaluateCondition(condition string, ctx *Context) bool {
	if condition == "" {
		return true
	}

	resolved := ctx.Resolve(condition)
	return evaluate(resolved)
}

func evaluate(expr string) bool {
	expr = strings.TrimSpace(expr)

	// Handle OR first (lower precedence - evaluated at top level)
	if idx := strings.Index(expr, " OR "); idx != -1 {
		return evaluate(expr[:idx]) || evaluate(expr[idx+4:])
	}
	// Handle AND (higher precedence - evaluated deeper in recursion)
	if idx := strings.Index(expr, " AND "); idx != -1 {
		return evaluate(expr[:idx]) && evaluate(expr[idx+5:])
	}

	// Handle comparisons
	ops := []string{">=", "<=", "!=", "==", ">", "<", " contains "}
	for _, op := range ops {
		if idx := strings.Index(expr, op); idx != -1 {
			left := strings.TrimSpace(expr[:idx])
			right := strings.TrimSpace(expr[idx+len(op):])
			return compare(left, op, right)
		}
	}

	// Boolean literal
	return expr == "true"
}

func compare(left, op, right string) bool {
	// Strip quotes from strings
	left = strings.Trim(left, "'\"")
	right = strings.Trim(right, "'\"")

	switch op {
	case "==":
		return left == right
	case "!=":
		return left != right
	case " contains ":
		return strings.Contains(left, right)
	case ">", "<", ">=", "<=":
		lf, lerr := strconv.ParseFloat(left, 64)
		rf, rerr := strconv.ParseFloat(right, 64)
		if lerr != nil || rerr != nil {
			return false
		}
		switch op {
		case ">":
			return lf > rf
		case "<":
			return lf < rf
		case ">=":
			return lf >= rf
		case "<=":
			return lf <= rf
		}
	}
	return false
}
