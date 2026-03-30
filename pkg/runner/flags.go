package runner

import (
	"fmt"
	"strings"
)

// FlagAliases defines a group of flag names that are aliases for the same option
type FlagAliases struct {
	Names    []string // e.g., ["-m", "--model"]
	TakesArg bool     // true if the flag takes an argument
}

// CheckDuplicateFlags scans args for duplicate flags with conflicting values
// Returns an error describing the conflict, or nil if no conflicts found
func CheckDuplicateFlags(args []string, flagGroups []FlagAliases) error {
	for _, group := range flagGroups {
		var values []string
		var flagsUsed []string

		for i := 0; i < len(args); i++ {
			arg := args[i]

			for _, flagName := range group.Names {
				if group.TakesArg {
					// Check for "-m value" or "--model value" format
					if arg == flagName && i+1 < len(args) {
						values = append(values, args[i+1])
						flagsUsed = append(flagsUsed, flagName)
						i++ // skip the value
						break
					}
					// Check for "-m=value" or "--model=value" format
					if strings.HasPrefix(arg, flagName+"=") {
						values = append(values, arg[len(flagName)+1:])
						flagsUsed = append(flagsUsed, flagName)
						break
					}
				} else {
					// Boolean flag
					if arg == flagName {
						values = append(values, "true")
						flagsUsed = append(flagsUsed, flagName)
						break
					}
				}
			}
		}

		// Check for conflicts
		if len(values) > 1 {
			// Check if all values are the same (not a conflict)
			allSame := true
			for _, v := range values[1:] {
				if v != values[0] {
					allSame = false
					break
				}
			}
			if !allSame {
				return fmt.Errorf("conflicting flags: %s specified multiple times with different values (%s)",
					strings.Join(flagsUsed, ", "), strings.Join(values, " vs "))
			}
		}
	}
	return nil
}

// ParseVarFlags extracts -x key=value flags from args and returns cleaned args and vars map
func ParseVarFlags(args []string) ([]string, map[string]string) {
	vars := make(map[string]string)
	var cleanedArgs []string

	i := 0
	for i < len(args) {
		arg := args[i]
		if arg == "-x" && i+1 < len(args) {
			// -x key=value format
			kv := args[i+1]
			if idx := strings.Index(kv, "="); idx > 0 {
				key := kv[:idx]
				value := kv[idx+1:]
				vars[key] = value
			}
			i += 2
		} else if strings.HasPrefix(arg, "-x=") {
			// -x=key=value format (less common but support it)
			kv := arg[3:]
			if idx := strings.Index(kv, "="); idx > 0 {
				key := kv[:idx]
				value := kv[idx+1:]
				vars[key] = value
			}
			i++
		} else {
			cleanedArgs = append(cleanedArgs, arg)
			i++
		}
	}

	return cleanedArgs, vars
}

// CommonFlagGroups returns the flag groups common to all tools
func CommonFlagGroups() []FlagAliases {
	return []FlagAliases{
		{Names: []string{"-c", "--code"}, TakesArg: true},
		{Names: []string{"-d", "--dir"}, TakesArg: true},
		{Names: []string{"-m", "--model"}, TakesArg: true},
		{Names: []string{"-j", "--json"}, TakesArg: false},
		{Names: []string{"-J", "--stats-json"}, TakesArg: false},
		{Names: []string{"--status-only"}, TakesArg: false},
		{Names: []string{"-l", "--lock"}, TakesArg: false},
		{Names: []string{"-D", "--delete-old"}, TakesArg: false},
		{Names: []string{"-R", "--require-review"}, TakesArg: false},
		{Names: []string{"-r", "--recursive"}, TakesArg: false},
		{Names: []string{"--levels"}, TakesArg: true},
		{Names: []string{"--list"}, TakesArg: true},
		{Names: []string{"-A", "--dir-all"}, TakesArg: true},
		{Names: []string{"-V", "--verbose"}, TakesArg: false},
		{Names: []string{"-v", "--version"}, TakesArg: false},
		{Names: []string{"-f", "--force"}, TakesArg: false},
	}
}

// reorderArgsForFlagParsing moves all flags to the front so Go's flag package
// can parse them correctly (it stops at the first non-flag argument)
func reorderArgsForFlagParsing(args []string, flagGroups []FlagAliases) []string {
	// Build a set of known flags for quick lookup
	knownFlags := make(map[string]bool)
	flagTakesArg := make(map[string]bool)
	for _, group := range flagGroups {
		for _, name := range group.Names {
			knownFlags[name] = true
			flagTakesArg[name] = group.TakesArg
		}
	}
	// Add common flags that might not be in flagGroups
	commonFlags := []string{"-h", "--help", "-t", "--tasks", "-n", "--dry-run", "-o", "--output", "-v", "--version"}
	for _, f := range commonFlags {
		knownFlags[f] = true
	}
	flagTakesArg["-o"] = true
	flagTakesArg["--output"] = true

	var flagArgs []string
	var positionalArgs []string

	i := 0
	for i < len(args) {
		arg := args[i]

		// Check if it's a flag
		if strings.HasPrefix(arg, "-") {
			// Check for --flag=value format
			if eqIdx := strings.Index(arg, "="); eqIdx > 0 {
				flagName := arg[:eqIdx]
				if knownFlags[flagName] {
					flagArgs = append(flagArgs, arg)
					i++
					continue
				}
			}

			// Check if it's a known flag
			if knownFlags[arg] {
				flagArgs = append(flagArgs, arg)
				// If it takes an argument, include the next arg too
				if flagTakesArg[arg] && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
					i++
					flagArgs = append(flagArgs, args[i])
				}
				i++
				continue
			}

			// Unknown flag - still treat as flag (might be tool-specific)
			flagArgs = append(flagArgs, arg)
			i++
			continue
		}

		// It's a positional argument
		positionalArgs = append(positionalArgs, arg)
		i++
	}

	// Return flags first, then positional args
	return append(flagArgs, positionalArgs...)
}
