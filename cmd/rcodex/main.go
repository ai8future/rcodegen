package main

import (
	"rcodegen/pkg/runner"
	"rcodegen/pkg/tools/codex"

	chassis "github.com/ai8future/chassis-go/v6"
)

func main() {
	chassis.RequireMajor(6)
	tool := codex.New()
	r := runner.NewRunner(tool)
	r.RunAndExit()
}
