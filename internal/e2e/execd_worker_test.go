package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type queuedExecTask struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Status   string         `json:"status"`
	Owner    string         `json:"owner,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
	Payload  map[string]any `json:"payload,omitempty"`
}

type execTaskUpdate struct {
	Kind    string         `json:"kind"`
	Payload map[string]any `json:"payload"`
}

type mockExecAPI struct {
	mu               sync.Mutex
	queue            []queuedExecTask
	updates          map[string][]execTaskUpdate
	completions      map[string]map[string]any
	fails            map[string]string
	assistantOutputs map[string][]map[string]any
}

func newMockExecAPI(queue []queuedExecTask) *mockExecAPI {
	copied := append([]queuedExecTask(nil), queue...)
	return &mockExecAPI{
		queue:            copied,
		updates:          map[string][]execTaskUpdate{},
		completions:      map[string]map[string]any{},
		fails:            map[string]string{},
		assistantOutputs: map[string][]map[string]any{},
	}
}

func (m *mockExecAPI) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/api/tasks/queue" {
		m.handleQueue(w, r)
		return
	}

	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 4 || parts[0] != "api" || parts[1] != "tasks" {
		http.NotFound(w, r)
		return
	}
	taskID := parts[2]
	action := parts[3]

	switch action {
	case "updates":
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode([]map[string]any{})
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Kind    string         `json:"kind"`
			Payload map[string]any `json:"payload"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		m.mu.Lock()
		m.updates[taskID] = append(m.updates[taskID], execTaskUpdate{Kind: body.Kind, Payload: body.Payload})
		m.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		return
	case "complete":
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		m.mu.Lock()
		m.completions[taskID] = body
		m.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		return
	case "fail":
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Error string `json:"error"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		m.mu.Lock()
		m.fails[taskID] = body.Error
		m.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		return
	case "assistant_output":
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		m.mu.Lock()
		m.assistantOutputs[taskID] = append(m.assistantOutputs[taskID], body)
		m.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		return
	default:
		http.NotFound(w, r)
	}
}

func (m *mockExecAPI) handleQueue(w http.ResponseWriter, r *http.Request) {
	limit := 1
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	m.mu.Lock()
	if limit > len(m.queue) {
		limit = len(m.queue)
	}
	claimed := append([]queuedExecTask(nil), m.queue[:limit]...)
	m.queue = append([]queuedExecTask(nil), m.queue[limit:]...)
	m.mu.Unlock()
	_ = json.NewEncoder(w).Encode(claimed)
}

func (m *mockExecAPI) hasCompletion(taskID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.completions[taskID]
	return ok
}

func (m *mockExecAPI) completion(taskID string) (map[string]any, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	got, ok := m.completions[taskID]
	if !ok {
		return nil, false
	}
	return cloneAnyMap(got), true
}

func (m *mockExecAPI) fail(taskID string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	err, ok := m.fails[taskID]
	return err, ok
}

func (m *mockExecAPI) updatesByKind(taskID, kind string) []execTaskUpdate {
	m.mu.Lock()
	defer m.mu.Unlock()
	src := m.updates[taskID]
	out := make([]execTaskUpdate, 0, len(src))
	for _, upd := range src {
		if upd.Kind != kind {
			continue
		}
		out = append(out, execTaskUpdate{
			Kind:    upd.Kind,
			Payload: cloneAnyMap(upd.Payload),
		})
	}
	return out
}

func (m *mockExecAPI) assistantOutput(taskID string) []map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	src := m.assistantOutputs[taskID]
	out := make([]map[string]any, 0, len(src))
	for _, item := range src {
		out = append(out, cloneAnyMap(item))
	}
	return out
}

func cloneAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

type runningExecd struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
	stdout *bytes.Buffer
	stderr *bytes.Buffer
}

func startExecd(t *testing.T, apiURL, home string) *runningExecd {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(
		ctx,
		"bun",
		"exec/execd.ts",
		"--api-url",
		apiURL,
		"--parallel",
		"1",
		"--poll-ms",
		"10",
	)
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(), "GO_AGENTS_HOME="+home)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("start execd: %v", err)
	}
	return &runningExecd{
		cmd:    cmd,
		cancel: cancel,
		stdout: &stdout,
		stderr: &stderr,
	}
}

func (r *runningExecd) stop() {
	if r == nil {
		return
	}
	r.cancel()
	if r.cmd.Process != nil {
		_ = r.cmd.Process.Kill()
	}
	_ = r.cmd.Wait()
}

func waitForCondition(t *testing.T, timeout time.Duration, description string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if fn() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", description)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestExecdBindsPriorResultsByOwner(t *testing.T) {
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun not installed")
	}

	api := newMockExecAPI([]queuedExecTask{
		{
			ID:      "exec-bind-1",
			Type:    "exec",
			Status:  "queued",
			Owner:   "agent-a",
			Payload: map[string]any{"code": "globalThis.result = { value: 41 }"},
		},
		{
			ID:     "exec-bind-2",
			Type:   "exec",
			Status: "queued",
			Owner:  "agent-a",
			Payload: map[string]any{
				"code": `globalThis.result = {
  from_result1: (globalThis as any).$result1?.value ?? null,
  from_last: (globalThis as any).$last?.value ?? null,
}`,
			},
		},
	})
	server := httptest.NewServer(http.HandlerFunc(api.handle))
	defer server.Close()

	home := filepath.Join(t.TempDir(), "go-agents-home")
	proc := startExecd(t, server.URL, home)
	defer proc.stop()

	waitForCondition(t, 15*time.Second, "second completion", func() bool {
		return api.hasCompletion("exec-bind-2")
	})
	if failErr, failed := api.fail("exec-bind-2"); failed {
		t.Fatalf("exec-bind-2 failed: %s\nstdout=%s\nstderr=%s", failErr, proc.stdout.String(), proc.stderr.String())
	}

	completion, ok := api.completion("exec-bind-2")
	if !ok {
		t.Fatalf("missing completion for exec-bind-2")
	}
	result, _ := completion["result"].(map[string]any)
	if result == nil {
		t.Fatalf("missing result payload: %#v", completion)
	}
	if result["from_result1"] != float64(41) {
		t.Fatalf("expected from_result1=41, got %v", result["from_result1"])
	}
	if result["from_last"] != float64(41) {
		t.Fatalf("expected from_last=41, got %v", result["from_last"])
	}
}

func TestExecdSpillsOversizedStdoutToFile(t *testing.T) {
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun not installed")
	}

	payloadLen := 200000
	api := newMockExecAPI([]queuedExecTask{
		{
			ID:     "exec-stdout-overflow",
			Type:   "exec",
			Status: "queued",
			Owner:  "agent-a",
			Payload: map[string]any{
				"code": fmt.Sprintf("console.log('x'.repeat(%d)); globalThis.result = { ok: true }", payloadLen),
			},
		},
	})
	server := httptest.NewServer(http.HandlerFunc(api.handle))
	defer server.Close()

	home := filepath.Join(t.TempDir(), "go-agents-home")
	proc := startExecd(t, server.URL, home)
	defer proc.stop()

	waitForCondition(t, 15*time.Second, "stdout overflow completion", func() bool {
		return api.hasCompletion("exec-stdout-overflow")
	})
	if failErr, failed := api.fail("exec-stdout-overflow"); failed {
		t.Fatalf("exec-stdout-overflow failed: %s\nstdout=%s\nstderr=%s", failErr, proc.stdout.String(), proc.stderr.String())
	}

	updates := api.updatesByKind("exec-stdout-overflow", "stdout")
	if len(updates) == 0 {
		t.Fatalf("expected stdout updates")
	}
	var overflowPayload map[string]any
	inlineBytes := 0
	for _, upd := range updates {
		if text, _ := upd.Payload["text"].(string); text != "" {
			if overflow, _ := upd.Payload["overflow_to_file"].(bool); !overflow {
				inlineBytes += len(text)
			}
		}
		if overflow, _ := upd.Payload["overflow_to_file"].(bool); overflow {
			overflowPayload = upd.Payload
		}
	}
	if overflowPayload == nil {
		t.Fatalf("expected overflow notice in stdout updates: %#v", updates)
	}
	if inlineBytes > 65536 {
		t.Fatalf("expected inline bytes <= 65536, got %d", inlineBytes)
	}
	outputFile, _ := overflowPayload["output_file"].(string)
	if strings.TrimSpace(outputFile) == "" {
		t.Fatalf("missing output_file in overflow payload: %#v", overflowPayload)
	}
	outputFileAbs, _ := overflowPayload["output_file_abs"].(string)
	if strings.TrimSpace(outputFileAbs) == "" {
		outputFileAbs = filepath.Join(home, outputFile)
	}
	raw, err := os.ReadFile(outputFileAbs)
	if err != nil {
		t.Fatalf("read spilled stdout file %s: %v", outputFileAbs, err)
	}
	if len(raw) < payloadLen {
		t.Fatalf("expected spilled stdout length >= %d, got %d", payloadLen, len(raw))
	}
}

func TestExecdSpillsOversizedResultToFile(t *testing.T) {
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun not installed")
	}

	payloadLen := 150000
	api := newMockExecAPI([]queuedExecTask{
		{
			ID:     "exec-result-overflow",
			Type:   "exec",
			Status: "queued",
			Owner:  "agent-a",
			Payload: map[string]any{
				"code": fmt.Sprintf("globalThis.result = { blob: 'x'.repeat(%d) }", payloadLen),
			},
		},
	})
	server := httptest.NewServer(http.HandlerFunc(api.handle))
	defer server.Close()

	home := filepath.Join(t.TempDir(), "go-agents-home")
	proc := startExecd(t, server.URL, home)
	defer proc.stop()

	waitForCondition(t, 15*time.Second, "result overflow completion", func() bool {
		return api.hasCompletion("exec-result-overflow")
	})
	if failErr, failed := api.fail("exec-result-overflow"); failed {
		t.Fatalf("exec-result-overflow failed: %s\nstdout=%s\nstderr=%s", failErr, proc.stdout.String(), proc.stderr.String())
	}

	completion, ok := api.completion("exec-result-overflow")
	if !ok {
		t.Fatalf("missing completion for exec-result-overflow")
	}
	result, _ := completion["result"].(map[string]any)
	if result == nil {
		t.Fatalf("missing result payload: %#v", completion)
	}
	if overflow, _ := result["result_too_large"].(bool); !overflow {
		t.Fatalf("expected result_too_large=true, got %#v", result)
	}
	resultFile, _ := result["result_file"].(string)
	if strings.TrimSpace(resultFile) == "" {
		t.Fatalf("missing result_file in completion payload: %#v", result)
	}
	resultFileAbs, _ := result["result_file_abs"].(string)
	if strings.TrimSpace(resultFileAbs) == "" {
		resultFileAbs = filepath.Join(home, resultFile)
	}
	raw, err := os.ReadFile(resultFileAbs)
	if err != nil {
		t.Fatalf("read spilled result file %s: %v", resultFileAbs, err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("decode spilled result file: %v", err)
	}
	inner, _ := parsed["result"].(map[string]any)
	if inner == nil {
		t.Fatalf("expected wrapped result map, got %#v", parsed)
	}
	blob, _ := inner["blob"].(string)
	if len(blob) != payloadLen {
		t.Fatalf("expected blob len=%d, got %d", payloadLen, len(blob))
	}
}

func TestExecdSendToUserRoutesAssistantOutput(t *testing.T) {
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun not installed")
	}

	api := newMockExecAPI([]queuedExecTask{
		{
			ID:     "exec-send-to-user",
			Type:   "exec",
			Status: "queued",
			Owner:  "agent-a",
			Payload: map[string]any{
				"code": `sendToUser("hello from sendToUser"); globalThis.result = { ok: true }`,
			},
		},
	})
	server := httptest.NewServer(http.HandlerFunc(api.handle))
	defer server.Close()

	home := filepath.Join(t.TempDir(), "go-agents-home")
	proc := startExecd(t, server.URL, home)
	defer proc.stop()

	waitForCondition(t, 15*time.Second, "sendToUser completion", func() bool {
		return api.hasCompletion("exec-send-to-user")
	})
	if failErr, failed := api.fail("exec-send-to-user"); failed {
		t.Fatalf("exec-send-to-user failed: %s\nstdout=%s\nstderr=%s", failErr, proc.stdout.String(), proc.stderr.String())
	}

	waitForCondition(t, 15*time.Second, "assistant output", func() bool {
		return len(api.assistantOutput("agent-a")) > 0
	})
	outputs := api.assistantOutput("agent-a")
	if len(outputs) == 0 {
		t.Fatalf("expected assistant output for owner")
	}
	found := false
	for _, payload := range outputs {
		if strings.TrimSpace(fmt.Sprintf("%v", payload["text"])) == "hello from sendToUser" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected sendToUser text in assistant output payloads, got %#v", outputs)
	}
}
