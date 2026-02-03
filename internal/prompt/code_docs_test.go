package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollectCodeDocs(t *testing.T) {
	dir := t.TempDir()
	codeDir := filepath.Join(dir, "code")
	if err := os.MkdirAll(codeDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	source := `// Run a shell command
export async function exec(command: string, options?: ExecOptions) {}

// Alias for exec
export const run = (command: string) => exec(command)
`
	if err := os.WriteFile(filepath.Join(codeDir, "shell.ts"), []byte(source), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	docs, err := CollectCodeDocs(codeDir)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if !strings.Contains(docs, "shell.ts") {
		t.Fatalf("expected file in docs: %s", docs)
	}
	if !strings.Contains(docs, "exec(command: string") {
		t.Fatalf("expected exec signature: %s", docs)
	}
	if !strings.Contains(docs, "run(command: string") {
		t.Fatalf("expected run signature: %s", docs)
	}
}
