package lock

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// ANSI color codes
const (
	Dim   = "\033[2m"
	Green = "\033[32m"
	Cyan  = "\033[36m"
	Reset = "\033[0m"
)

// Lock timing constants
const (
	lockTimeout      = 5 * time.Minute // Maximum time to wait for lock
	lockPollInterval = 5 * time.Second // How often to check if lock is available
	maxIdentifierLen = 100             // Maximum length for lock identifier
)

// sanitizeIdentifier cleans the identifier for safe use in file
func sanitizeIdentifier(id string) string {
	if id == "" {
		return "unknown"
	}
	// Remove path separators and control characters
	result := strings.Map(func(r rune) rune {
		if r < 32 || r == '/' || r == '\\' {
			return '_'
		}
		return r
	}, id)
	// Limit length
	if len(result) > maxIdentifierLen {
		result = result[:maxIdentifierLen]
	}
	return result
}

// FileLock represents a file-based lock
type FileLock struct {
	file *os.File
	path string
}

// getLockDir returns the secure lock directory path (~/.rcodegen/locks/)
func getLockDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not get user home directory: %w", err)
	}
	return filepath.Join(home, ".rcodegen", "locks"), nil
}

// Acquire acquires a file lock, waiting if necessary
// identifier is used to identify who holds the lock (e.g., codebase name)
func Acquire(identifier string, useLock bool) (*FileLock, error) {
	if !useLock {
		return nil, nil
	}

	lockDir, err := getLockDir()
	if err != nil {
		return nil, err
	}

	// Create lock directory with secure permissions (owner only)
	if err := os.MkdirAll(lockDir, 0700); err != nil {
		return nil, fmt.Errorf("could not create lock directory %s: %w", lockDir, err)
	}

	lockPath := filepath.Join(lockDir, "rcodegen.lock")
	lockInfoPath := filepath.Join(lockDir, "rcodegen.lock.info")

	// Sanitize identifier for safe use
	identifier = sanitizeIdentifier(identifier)

	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("could not open lock file %s: %w", lockPath, err)
	}

	// Try non-blocking lock first
	err = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		// Lock is held, wait for it
		holder := "unknown"
		if data, err := os.ReadFile(lockInfoPath); err == nil {
			holder = strings.TrimSpace(string(data))
		}

		startWait := time.Now()
		fmt.Printf("%sWaiting for %s%s%s%s to finish...%s\n", Dim, Cyan, holder, Reset, Dim, Reset)

		for {
			// Check for timeout
			if time.Since(startWait) > lockTimeout {
				lockFile.Close()
				return nil, fmt.Errorf("timed out waiting for lock after %v", lockTimeout)
			}
			time.Sleep(lockPollInterval)
			err = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
			if err == nil {
				break
			}
			elapsed := int(time.Since(startWait).Seconds())
			if data, err := os.ReadFile(lockInfoPath); err == nil {
				holder = strings.TrimSpace(string(data))
			}
			fmt.Printf("%s  Still waiting for %s%s%s%s... %ds%s\n", Dim, Cyan, holder, Reset, Dim, elapsed, Reset)
		}
		elapsed := int(time.Since(startWait).Seconds())
		fmt.Printf("\r%sLock acquired%s %s(waited %ds for %s)%s     \n", Green, Reset, Dim, elapsed, holder, Reset)
	} else {
		fmt.Printf("%sLock acquired%s\n", Dim, Reset)
	}

	// Write our info so others know who has the lock
	if err := os.WriteFile(lockInfoPath, []byte(identifier), 0600); err != nil {
		fmt.Fprintf(os.Stderr, "%sWarning: could not write lock info: %v%s\n", Dim, err, Reset)
	}

	return &FileLock{file: lockFile, path: lockPath}, nil
}

// Release releases the file lock
func (l *FileLock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}

	unlockErr := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	if unlockErr != nil {
		return fmt.Errorf("failed to unlock: %w", unlockErr)
	}
	return closeErr
}

// GetIdentifier returns a reasonable identifier for the current process
// based on the working directory
func GetIdentifier(workDir string) string {
	codebaseName := filepath.Base(workDir)
	if codebaseName == "" || codebaseName == "." {
		if cwd, err := os.Getwd(); err == nil {
			codebaseName = filepath.Base(cwd)
		} else {
			codebaseName = "unknown"
		}
	}
	return codebaseName
}
