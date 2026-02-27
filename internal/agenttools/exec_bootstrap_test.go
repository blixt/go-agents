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

	runBootstrap(t, codePath, resultPath, "", "")
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

func TestExecBootstrapBindsPriorResults(t *testing.T) {
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun not installed")
	}

	tmp := t.TempDir()
	bindingsPath := filepath.Join(tmp, "bindings.bin")
	firstCodePath := filepath.Join(tmp, "first.ts")
	firstResultPath := filepath.Join(tmp, "first.json")
	secondCodePath := filepath.Join(tmp, "second.ts")
	secondResultPath := filepath.Join(tmp, "second.json")

	firstCode := `globalThis.result = { value: 41 };`
	if err := os.WriteFile(firstCodePath, []byte(firstCode), 0o600); err != nil {
		t.Fatalf("write first code: %v", err)
	}
	runBootstrap(t, firstCodePath, firstResultPath, bindingsPath, "")

	secondCode := `globalThis.result = {
  from_result1: (globalThis as any).$result1?.value ?? null,
  from_last: (globalThis as any).$last?.value ?? null,
};`
	if err := os.WriteFile(secondCodePath, []byte(secondCode), 0o600); err != nil {
		t.Fatalf("write second code: %v", err)
	}
	runBootstrap(t, secondCodePath, secondResultPath, bindingsPath, "")

	raw, err := os.ReadFile(secondResultPath)
	if err != nil {
		t.Fatalf("read second result: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode second result: %v", err)
	}
	payload, ok := result["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected second result payload map, got %T", result["result"])
	}
	if payload["from_result1"] != float64(41) {
		t.Fatalf("expected from_result1=41, got %v", payload["from_result1"])
	}
	if payload["from_last"] != float64(41) {
		t.Fatalf("expected from_last=41, got %v", payload["from_last"])
	}
}

func TestExecBootstrapSendToUserMessages(t *testing.T) {
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun not installed")
	}

	tmp := t.TempDir()
	codePath := filepath.Join(tmp, "task.ts")
	resultPath := filepath.Join(tmp, "result.json")
	userMessagesPath := filepath.Join(tmp, "user_messages.json")

	code := `sendToUser("hello user")
sendToUser("  ")
globalThis.result = { ok: true }`
	if err := os.WriteFile(codePath, []byte(code), 0o600); err != nil {
		t.Fatalf("write code: %v", err)
	}

	runBootstrap(t, codePath, resultPath, "", userMessagesPath)
	raw, err := os.ReadFile(userMessagesPath)
	if err != nil {
		t.Fatalf("read user messages: %v", err)
	}
	var parsed struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("decode user messages: %v", err)
	}
	if len(parsed.Messages) != 1 {
		t.Fatalf("expected 1 user message, got %d", len(parsed.Messages))
	}
	if parsed.Messages[0]["text"] != "hello user" {
		t.Fatalf("unexpected user message: %#v", parsed.Messages[0])
	}
}

func runBootstrap(t *testing.T, codePath, resultPath, bindingsPath, userMessagesPath string) {
	t.Helper()
	cwd, _ := os.Getwd()
	repoRoot := filepath.Clean(filepath.Join(cwd, "..", ".."))
	args := []string{"exec/bootstrap.ts", "--code-file", codePath, "--result-path", resultPath}
	if bindingsPath != "" {
		args = append(args, "--bindings-path", bindingsPath)
	}
	if userMessagesPath != "" {
		args = append(args, "--user-messages-path", userMessagesPath)
	}
	cmd := exec.Command("bun", args...)
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bootstrap failed: %v\n%s", err, string(out))
	}
}
