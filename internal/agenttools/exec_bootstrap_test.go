package agenttools

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestExecBootstrapResultPayload(t *testing.T) {
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun not installed")
	}

	tmp := t.TempDir()
	codePath := filepath.Join(tmp, "task.ts")
	resultPath := filepath.Join(tmp, "result.json")

	code := `globalThis.result = { has_state: typeof (globalThis as any).state !== "undefined", count: 1 };`
	if err := os.WriteFile(codePath, []byte(code), 0o600); err != nil {
		t.Fatalf("write code: %v", err)
	}

	cwd, _ := os.Getwd()
	repoRoot := filepath.Clean(filepath.Join(cwd, "..", ".."))
	cmd := exec.Command("bun", "exec/bootstrap.ts", "--code-file", codePath, "--result-path", resultPath)
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bootstrap failed: %v\n%s", err, string(out))
	}
	raw, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	payload, ok := result["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result payload map, got %T", result["result"])
	}
	if payload["has_state"] != false {
		t.Fatalf("expected has_state=false, got %v", payload["has_state"])
	}
	if payload["count"] != float64(1) {
		t.Fatalf("expected count=1, got %v", payload["count"])
	}
}
