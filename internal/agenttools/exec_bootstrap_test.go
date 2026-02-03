package agenttools

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestExecBootstrapSnapshot(t *testing.T) {
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun not installed")
	}

	tmp := t.TempDir()
	codePath := filepath.Join(tmp, "task.ts")
	snapshotPath := filepath.Join(tmp, "snapshot.json")
	resultPath := filepath.Join(tmp, "result.json")

	code := `globalThis.state.count = (globalThis.state.count || 0) + 1; globalThis.result = { count: globalThis.state.count };`
	if err := os.WriteFile(codePath, []byte(code), 0o600); err != nil {
		t.Fatalf("write code: %v", err)
	}

	cwd, _ := os.Getwd()
	repoRoot := filepath.Clean(filepath.Join(cwd, "..", ".."))

	run := func() {
		cmd := exec.Command("bun", "exec/bootstrap.ts", "--code-file", codePath, "--snapshot-in", snapshotPath, "--snapshot-out", snapshotPath, "--result-path", resultPath)
		cmd.Dir = repoRoot
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bootstrap failed: %v\n%s", err, string(out))
		}
	}

	run()
	raw, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	state, ok := result["state"].(map[string]any)
	if !ok || state["count"].(float64) != 1 {
		t.Fatalf("expected count 1")
	}

	run()
	raw, _ = os.ReadFile(resultPath)
	_ = json.Unmarshal(raw, &result)
	state, ok = result["state"].(map[string]any)
	if !ok || state["count"].(float64) != 2 {
		t.Fatalf("expected count 2")
	}
}
