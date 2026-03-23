package main

import (
	"fmt"
	"os"

	"rcodegen/pkg/runner"
	"rcodegen/pkg/tools/claude"

	chassis "github.com/ai8future/chassis-go/v10"
	"github.com/ai8future/chassis-go/v10/logz"
	"github.com/ai8future/chassis-go/v10/registry"
)

func main() {
	chassis.RequireMajor(10)
	logger := logz.New("info")
	if err := registry.InitCLI(chassis.Version); err != nil {
		logger.Error("registry init failed", "error", err)
		os.Exit(1)
	}
	tool := claude.New()
	r := runner.NewRunner(tool)
	result := r.Run()
	if result.Error != nil {
		fmt.Fprintln(os.Stderr, result.Error)
	}
	registry.ShutdownCLI(result.ExitCode)
	os.Exit(result.ExitCode)
}
