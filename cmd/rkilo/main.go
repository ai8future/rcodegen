package main

import (
	"fmt"
	"os"

	rcodegenpkg "rcodegen"
	"rcodegen/pkg/runner"
	"rcodegen/pkg/tools/kilocode"

	chassis "github.com/ai8future/chassis-go/v11"
	"github.com/ai8future/chassis-go/v11/logz"
	"github.com/ai8future/chassis-go/v11/registry"
)

func main() {
	chassis.SetAppVersion(rcodegenpkg.AppVersion)
	chassis.RequireMajor(11)
	logger := logz.New("info")
	if err := registry.InitCLI(chassis.Version); err != nil {
		logger.Error("registry init failed", "error", err)
		os.Exit(1)
	}
	tool := kilocode.New()
	r := runner.NewRunner(tool)
	result := r.Run()
	if result.Error != nil {
		fmt.Fprintln(os.Stderr, result.Error)
	}
	registry.ShutdownCLI(result.ExitCode)
	os.Exit(result.ExitCode)
}
