package goagents

import (
	"io/fs"
	"os"
	"path/filepath"

	goagents_template "github.com/flitsinc/go-agents/template"
)

// ManagedHarnessPromptFile is the runtime-managed prompt fragment with harness API contract.
const ManagedHarnessPromptFile = "PROMPT_HARNESS_API.ts"

func copyTemplate(dest string, overwrite bool) error {
	sub, err := fs.Sub(goagents_template.FS, ".")
	if err != nil {
		return err
	}
	return fs.WalkDir(sub, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == "." {
			return nil
		}
		if path == "embed.go" {
			return nil
		}
		target := filepath.Join(dest, path)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := fs.ReadFile(sub, path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if !overwrite {
			if _, err := os.Stat(target); err == nil {
				return nil
			} else if !os.IsNotExist(err) {
				return err
			}
		}
		return os.WriteFile(target, data, 0o644)
	})
}

func syncManagedHarnessPrompt(destRoot string) error {
	data, err := fs.ReadFile(goagents_template.FS, ManagedHarnessPromptFile)
	if err != nil {
		return err
	}
	target := filepath.Join(destRoot, ManagedHarnessPromptFile)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return os.WriteFile(target, data, 0o644)
}
