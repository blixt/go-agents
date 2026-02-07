package execworker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/flitsinc/go-agents/internal/goagents"
)

type Task struct {
	ID      string                 `json:"id"`
	Type    string                 `json:"type"`
	Status  string                 `json:"status"`
	Payload map[string]any         `json:"payload"`
	Meta    map[string]interface{} `json:"metadata"`
}

type Worker struct {
	APIURL        string
	BootstrapPath string
	HTTP          *http.Client
}

func (w *Worker) client() *http.Client {
	if w.HTTP != nil {
		return w.HTTP
	}
	return http.DefaultClient
}

func (w *Worker) PollOnce(ctx context.Context) ([]Task, error) {
	url := w.APIURL + "/api/tasks/queue?type=exec&limit=1"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := w.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("queue status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var tasks []Task
	if err := json.NewDecoder(resp.Body).Decode(&tasks); err != nil {
		return nil, err
	}
	for _, task := range tasks {
		if err := w.RunTask(ctx, task); err != nil {
			return tasks, err
		}
	}
	return tasks, nil
}

func (w *Worker) RunTask(ctx context.Context, task Task) error {
	payload := task.Payload
	code, _ := payload["code"].(string)
	if code == "" {
		return w.sendFail(ctx, task.ID, "exec task missing code")
	}

	tmpDir, err := os.MkdirTemp("", "go-agents-execd-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	home, err := goagents.EnsureHome()
	if err != nil {
		return err
	}

	nodeModules := filepath.Join(tmpDir, "node_modules")
	if err := os.MkdirAll(nodeModules, 0o755); err != nil {
		return err
	}
	toolsSource := filepath.Join(home, "tools")
	coreSource := filepath.Join(home, "core")
	_ = os.Symlink(toolsSource, filepath.Join(nodeModules, "tools"))
	_ = os.Symlink(coreSource, filepath.Join(nodeModules, "core"))

	codePath := filepath.Join(tmpDir, "task.ts")
	if err := os.WriteFile(codePath, []byte(code), 0o600); err != nil {
		return err
	}

	resultPath := filepath.Join(tmpDir, "result.json")
	bootstrap := w.BootstrapPath
	if bootstrap == "" {
		bootstrap = filepath.Join("exec", "bootstrap.ts")
	}
	if !filepath.IsAbs(bootstrap) {
		if abs, err := filepath.Abs(bootstrap); err == nil {
			bootstrap = abs
		}
	}

	_ = w.sendUpdate(ctx, task.ID, "start", map[string]any{})

	bunPath, err := exec.LookPath("bun")
	if err != nil {
		bunPath = "bun"
	}
	args := []string{bunPath, bootstrap, "--code-file", codePath, "--result-path", resultPath}

	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Dir = home
	cmd.Env = append(os.Environ(), fmt.Sprintf("GO_AGENTS_HOME=%s", home))
	if err := cmd.Run(); err != nil {
		_ = w.sendUpdate(ctx, task.ID, "exit", map[string]any{"exit_code": 1})
		return w.sendFail(ctx, task.ID, err.Error())
	}
	_ = w.sendUpdate(ctx, task.ID, "exit", map[string]any{"exit_code": 0})

	resultData, _ := os.ReadFile(resultPath)
	result := decodeExecResult(resultData)
	return w.sendComplete(ctx, task.ID, result)
}

func decodeExecResult(data []byte) map[string]any {
	if len(data) == 0 {
		return map[string]any{}
	}

	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return map[string]any{}
	}

	switch typed := raw.(type) {
	case map[string]any:
		if len(typed) == 1 {
			if inner, ok := typed["result"]; ok {
				if innerMap, ok := inner.(map[string]any); ok && innerMap != nil {
					return innerMap
				}
				return map[string]any{"result": inner}
			}
		}
		if typed == nil {
			return map[string]any{}
		}
		return typed
	case nil:
		return map[string]any{}
	default:
		return map[string]any{"result": typed}
	}
}

func (w *Worker) sendUpdate(ctx context.Context, taskID, kind string, payload map[string]any) error {
	body := map[string]any{"kind": kind, "payload": payload}
	return w.postJSON(ctx, fmt.Sprintf("/api/tasks/%s/updates", taskID), body)
}

func (w *Worker) sendComplete(ctx context.Context, taskID string, result map[string]any) error {
	return w.postJSON(ctx, fmt.Sprintf("/api/tasks/%s/complete", taskID), map[string]any{"result": result})
}

func (w *Worker) sendFail(ctx context.Context, taskID, errMsg string) error {
	return w.postJSON(ctx, fmt.Sprintf("/api/tasks/%s/fail", taskID), map[string]any{"error": errMsg})
}

func (w *Worker) postJSON(ctx context.Context, path string, payload map[string]any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.APIURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

func WaitForTask(ctx context.Context, fn func() (bool, error)) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		done, err := fn()
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
