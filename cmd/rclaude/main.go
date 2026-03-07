package main

import (
	"log"

	"rcodegen/pkg/runner"
	"rcodegen/pkg/tools/claude"

	chassis "github.com/ai8future/chassis-go/v8"
	"github.com/ai8future/chassis-go/v8/registry"
)

func main() {
	chassis.RequireMajor(8)
	if err := registry.InitCLI(chassis.Version); err != nil {
		log.Fatalf("registry: %v", err)
	}
	tool := claude.New()
	r := runner.NewRunner(tool)
	r.RunAndExit()
	registry.ShutdownCLI(0)
}
