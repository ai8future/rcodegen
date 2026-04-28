// Package colors provides shared ANSI color codes for terminal output.
// This package consolidates color definitions to avoid duplication across packages.
package colors

// ANSI color codes for terminal output
const (
	Reset   = "\033[0m"
	Bold    = "\033[1m"
	Dim     = "\033[2m"
	Red     = "\033[31m"
	Green   = "\033[32m"
	Yellow  = "\033[33m"
	Blue    = "\033[34m"
	Magenta = "\033[35m"
	Cyan    = "\033[36m"
	White   = "\033[37m"

	BrightMagenta = "\033[95m"
)
