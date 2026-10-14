// Package agenthome resolves the user-owned configuration location, independently
// of configuration parsing and prompt resource loading.
package agenthome

import (
	"os"
	"path/filepath"
)

func Dir() (string, error) {
	if dir := os.Getenv("CLAW_HOME"); dir != "" {
		return filepath.Abs(dir)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claw"), nil
}
