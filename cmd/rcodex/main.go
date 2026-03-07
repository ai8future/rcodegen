package main

import (
	"os"

	"rcodegen/pkg/runner"
	"rcodegen/pkg/tools/codex"

	chassis "github.com/ai8future/chassis-go/v8"
	"github.com/ai8future/chassis-go/v8/logz"
	"github.com/ai8future/chassis-go/v8/registry"
)

func main() {
	chassis.RequireMajor(8)
	logger := logz.New("info")
	if err := registry.InitCLI(chassis.Version); err != nil {
		logger.Error("registry init failed", "error", err)
		os.Exit(1)
	}
	tool := codex.New()
	r := runner.NewRunner(tool)
	r.RunAndExit()
	registry.ShutdownCLI(0)
}
