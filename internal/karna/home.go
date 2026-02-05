package karna

import (
	"fmt"
	"os"
	"path/filepath"
)

func HomeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".karna"), nil
}

// EnsureHome creates ~/.karna and seeds it with the embedded template if it does not exist.
// If ~/.karna already exists, it is left untouched.
func EnsureHome() (string, error) {
	home, err := HomeDir()
	if err != nil {
		return "", err
	}
	info, err := os.Stat(home)
	if err == nil {
		if !info.IsDir() {
			return "", fmt.Errorf("%s exists and is not a directory", home)
		}
		return home, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return "", err
	}
	if err := copyTemplate(home); err != nil {
		return "", err
	}
	return home, nil
}
