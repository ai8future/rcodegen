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
		return nil, fmt.Errorf("could not create lock directory: %w", err)
	}

	lockPath := filepath.Join(lockDir, "rcodegen.lock")
	lockInfoPath := filepath.Join(lockDir, "rcodegen.lock.info")

	// Use provided identifier or try to determine it
	if identifier == "" {
		identifier = "unknown"
	}

	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("could not open lock file: %w", err)
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
			time.Sleep(5 * time.Second)
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
	os.WriteFile(lockInfoPath, []byte(identifier), 0600)

	return &FileLock{file: lockFile, path: lockPath}, nil
}

// Release releases the file lock
func (l *FileLock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}

	syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	return l.file.Close()
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
