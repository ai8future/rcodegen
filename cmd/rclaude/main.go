package main

import (
	"rcodegen/pkg/runner"
	"rcodegen/pkg/tools/claude"

	chassis "github.com/ai8future/chassis-go/v6"
)

func main() {
	chassis.RequireMajor(6)
	tool := claude.New()
	r := runner.NewRunner(tool)
	r.RunAndExit()
}
