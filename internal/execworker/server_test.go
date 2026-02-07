package execworker

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/flitsinc/go-agents/internal/api"
	"github.com/flitsinc/go-agents/internal/eventbus"
	"github.com/flitsinc/go-agents/internal/tasks"
	"github.com/flitsinc/go-agents/internal/testutil"
)

func TestWorkerWebhookDispatch(t *testing.T) {
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun not installed")
	}

	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	server := &api.Server{Tasks: mgr, Bus: bus}
	client := testutil.NewInProcessClient(server.Handler())

	task, err := mgr.Spawn(context.Background(), tasks.Spec{
		Type:    "exec",
		Payload: map[string]any{"code": "globalThis.result = { ok: true };"},
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}

	worker := &Worker{
		APIURL:        "http://in-process",
		BootstrapPath: filepath.Join(repoRoot(t), "exec", "bootstrap.ts"),
		HTTP:          client,
	}

	body, _ := json.Marshal(Task{ID: task.ID, Type: "exec", Payload: map[string]any{"code": "globalThis.result = { ok: true };"}})
	req := httptest.NewRequest(http.MethodPost, "http://execd/dispatch", bytes.NewReader(body))
	w := httptest.NewRecorder()
	worker.Handler().ServeHTTP(w, req)
	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("dispatch status: %d", w.Result().StatusCode)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = WaitForTask(ctx, func() (bool, error) {
		task, err := mgr.Get(context.Background(), task.ID)
		if err != nil {
			return false, nil
		}
		return task.Status == tasks.StatusCompleted, nil
	})
	if err != nil {
		t.Fatalf("wait for task: %v", err)
	}
}
