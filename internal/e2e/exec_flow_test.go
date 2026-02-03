package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/flitsinc/go-agents/internal/api"
	"github.com/flitsinc/go-agents/internal/eventbus"
	"github.com/flitsinc/go-agents/internal/state"
	"github.com/flitsinc/go-agents/internal/tasks"
	"github.com/flitsinc/go-agents/internal/testutil"
)

type taskResponse struct {
	ID      string         `json:"id"`
	Type    string         `json:"type"`
	Status  string         `json:"status"`
	Payload map[string]any `json:"payload"`
	Result  map[string]any `json:"result"`
}

func TestExecFlowEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun not installed")
	}

	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	store := state.NewStore(db)
	server := &api.Server{Tasks: mgr, Bus: bus, Store: store}
	client := testutil.NewInProcessClient(server.Handler())

	code := "globalThis.state.count = (globalThis.state.count || 0) + 1; globalThis.result = { count: globalThis.state.count };"
	createResp := doJSON(t, client, "POST", "/api/tasks", map[string]any{
		"type":    "exec",
		"payload": map[string]any{"code": code, "id": "session-1"},
	})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create status: %d", createResp.StatusCode)
	}
	var created taskResponse
	decodeJSON(t, createResp, &created)
	if created.ID == "" {
		t.Fatalf("expected task id")
	}

	queueResp := doJSON(t, client, "GET", "/api/tasks/queue?type=exec&limit=1", nil)
	if queueResp.StatusCode != http.StatusOK {
		t.Fatalf("queue status: %d", queueResp.StatusCode)
	}
	var queued []taskResponse
	decodeJSON(t, queueResp, &queued)
	if len(queued) != 1 {
		t.Fatalf("expected queued task")
	}

	tmp := t.TempDir()
	codePath := filepath.Join(tmp, "task.ts")
	resultPath := filepath.Join(tmp, "result.json")
	snapshotPath := filepath.Join(tmp, "snapshot.json")
	if err := os.WriteFile(codePath, []byte(code), 0o600); err != nil {
		t.Fatalf("write code: %v", err)
	}

	cwd, _ := os.Getwd()
	repoRoot := filepath.Clean(filepath.Join(cwd, "..", ".."))
	cmd := exec.Command("bun", "tools/exec/bootstrap.ts", "--code-file", codePath, "--snapshot-in", snapshotPath, "--snapshot-out", snapshotPath, "--result-path", resultPath)
	cmd.Dir = repoRoot
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bootstrap failed: %v\n%s", err, string(output))
	}

	resultRaw, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	var resultPayload map[string]any
	if err := json.Unmarshal(resultRaw, &resultPayload); err != nil {
		t.Fatalf("decode result: %v", err)
	}

	_ = doJSON(t, client, "POST", "/api/tasks/"+created.ID+"/updates", map[string]any{"kind": "stdout", "payload": map[string]any{"text": "ok"}})
	completeResp := doJSON(t, client, "POST", "/api/tasks/"+created.ID+"/complete", map[string]any{"result": resultPayload})
	if completeResp.StatusCode != http.StatusOK {
		t.Fatalf("complete status: %d", completeResp.StatusCode)
	}

	finalResp := doJSON(t, client, "GET", "/api/tasks/"+created.ID, nil)
	if finalResp.StatusCode != http.StatusOK {
		t.Fatalf("get status: %d", finalResp.StatusCode)
	}
	var final taskResponse
	decodeJSON(t, finalResp, &final)
	if final.Status != string(tasks.StatusCompleted) {
		t.Fatalf("expected completed status")
	}
}

func doJSON(t *testing.T, client *http.Client, method, path string, payload any) *http.Response {
	t.Helper()
	var body *bytes.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		body = bytes.NewReader(data)
	} else {
		body = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, "http://in-process"+path, body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}

func decodeJSON(t *testing.T, resp *http.Response, dest any) {
	t.Helper()
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(dest); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}
