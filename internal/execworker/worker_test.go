package execworker

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"reflect"
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

func TestDecodeExecResult(t *testing.T) {
	t.Run("unwrap map result", func(t *testing.T) {
		data := []byte(`{"result":{"ok":true,"count":1}}`)
		got := decodeExecResult(data)
		want := map[string]any{"ok": true, "count": float64(1)}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("unexpected decoded result: got=%v want=%v", got, want)
		}
	})

	t.Run("wrap primitive result", func(t *testing.T) {
		data := []byte(`{"result":"done"}`)
		got := decodeExecResult(data)
		want := map[string]any{"result": "done"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("unexpected decoded result: got=%v want=%v", got, want)
		}
	})

	t.Run("pass through map payload", func(t *testing.T) {
		source := map[string]any{"ok": true}
		data, err := json.Marshal(source)
		if err != nil {
			t.Fatalf("marshal source: %v", err)
		}
		got := decodeExecResult(data)
		if !reflect.DeepEqual(got, source) {
			t.Fatalf("unexpected decoded result: got=%v want=%v", got, source)
		}
	})
}
