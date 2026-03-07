package main

import (
	"rcodegen/pkg/runner"
	"rcodegen/pkg/tools/gemini"

	chassis "github.com/ai8future/chassis-go/v6"
)

func main() {
	chassis.RequireMajor(6)
	tool := gemini.New()
	r := runner.NewRunner(tool)
	r.RunAndExit()
}
