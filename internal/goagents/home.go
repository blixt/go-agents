package goagents

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// LoadDotEnv parses a .env file at the given path and returns all key-value pairs.
// Unlike config.loadDotEnv, this loads all variables (not just *_API_KEY).
// Returns an empty map if the file does not exist.
func LoadDotEnv(path string) (map[string]string, error) {
	vars := make(map[string]string)
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return vars, nil
		}
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 &&
			((value[0] == '"' && value[len(value)-1] == '"') ||
				(value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		}
		vars[key] = value
	}
	return vars, scanner.Err()
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
