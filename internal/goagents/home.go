package goagents

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func HomeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".go-agents"), nil
}

// EnsureHome creates ~/.go-agents and seeds it with the embedded template if it does not exist.
// If ~/.go-agents already exists, it is left untouched.
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

// EnsureToolDeps scans ~/.go-agents/tools/*/package.json and runs `bun install`
// in each directory that has a package.json but no node_modules/.
func EnsureToolDeps(home string) error {
	toolsDir := filepath.Join(home, "tools")
	entries, err := os.ReadDir(toolsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(toolsDir, entry.Name())
		pkg := filepath.Join(dir, "package.json")
		if _, err := os.Stat(pkg); err != nil {
			continue
		}
		nm := filepath.Join(dir, "node_modules")
		if _, err := os.Stat(nm); err == nil {
			continue
		}
		cmd := exec.Command("bun", "install")
		cmd.Dir = dir
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("bun install in %s: %w", dir, err)
		}
	}
	return nil
}
