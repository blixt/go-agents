package goagents

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureHomeSeedsTemplateWhenDirAlreadyExists(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)

	home := filepath.Join(root, ".go-agents")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}

	got, err := EnsureHome()
	if err != nil {
		t.Fatalf("ensure home: %v", err)
	}
	if got != home {
		t.Fatalf("expected home %q, got %q", home, got)
	}

	if _, err := os.Stat(filepath.Join(home, "PROMPT.ts")); err != nil {
		t.Fatalf("expected PROMPT.ts to be seeded: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ManagedHarnessPromptFile)); err != nil {
		t.Fatalf("expected %s to be seeded: %v", ManagedHarnessPromptFile, err)
	}
	if _, err := os.Stat(filepath.Join(home, "MEMORY.md")); err != nil {
		t.Fatalf("expected MEMORY.md to be seeded: %v", err)
	}
}

func TestEnsureHomeDoesNotOverwriteExistingFiles(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)

	home := filepath.Join(root, ".go-agents")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}

	customPrompt := "console.log('custom prompt');\n"
	promptPath := filepath.Join(home, "PROMPT.ts")
	if err := os.WriteFile(promptPath, []byte(customPrompt), 0o644); err != nil {
		t.Fatalf("write custom prompt: %v", err)
	}

	if _, err := EnsureHome(); err != nil {
		t.Fatalf("ensure home: %v", err)
	}

	gotPrompt, err := os.ReadFile(promptPath)
	if err != nil {
		t.Fatalf("read prompt: %v", err)
	}
	if string(gotPrompt) != customPrompt {
		t.Fatalf("expected custom prompt to remain unchanged")
	}

	if _, err := os.Stat(filepath.Join(home, "MEMORY.md")); err != nil {
		t.Fatalf("expected missing template files to be seeded: %v", err)
	}
}

func TestEnsureHomeOverwritesManagedHarnessPrompt(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)

	home := filepath.Join(root, ".go-agents")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}

	custom := "console.log('stale managed prompt');\n"
	managedPath := filepath.Join(home, ManagedHarnessPromptFile)
	if err := os.WriteFile(managedPath, []byte(custom), 0o644); err != nil {
		t.Fatalf("write stale managed prompt: %v", err)
	}

	if _, err := EnsureHome(); err != nil {
		t.Fatalf("ensure home: %v", err)
	}

	got, err := os.ReadFile(managedPath)
	if err != nil {
		t.Fatalf("read managed prompt: %v", err)
	}
	if string(got) == custom {
		t.Fatalf("expected managed prompt to be refreshed from template")
	}
	if len(got) == 0 {
		t.Fatalf("expected managed prompt to be non-empty")
	}
}
