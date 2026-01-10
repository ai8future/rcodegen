package main

import (
	"rcodegen/pkg/runner"
	"rcodegen/pkg/tools/gemini"
)

func main() {
	tool := gemini.New()
	r := runner.NewRunner(tool)
	r.Run()
}
