package execworker

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/flitsinc/go-agents/internal/api"
	"github.com/flitsinc/go-agents/internal/eventbus"
	"github.com/flitsinc/go-agents/internal/tasks"
	"github.com/flitsinc/go-agents/internal/testutil"
)

func TestWorkerPollOnce(t *testing.T) {
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun not installed")
	}

	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	server := &api.Server{Tasks: mgr, Bus: bus}
	client := testutil.NewInProcessClient(server.Handler())

	code := "globalThis.result = { ok: true };"
	task, err := mgr.Spawn(context.Background(), tasks.Spec{Type: "exec", Payload: map[string]any{"code": code}})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}

	worker := &Worker{
		APIURL:        "http://in-process",
		SnapshotDir:   filepath.Join(t.TempDir(), "snapshots"),
		BootstrapPath: filepath.Join(repoRoot(t), "exec", "bootstrap.ts"),
		HTTP:          client,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := worker.PollOnce(ctx); err != nil {
		t.Fatalf("poll once: %v", err)
	}

	updated, err := mgr.Get(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if updated.Status != tasks.StatusCompleted {
		t.Fatalf("expected completed, got %s", updated.Status)
	}
}
