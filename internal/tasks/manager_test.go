package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/flitsinc/go-agents/internal/eventbus"
	"github.com/flitsinc/go-agents/internal/testutil"
)

func TestManagerLifecycleAndEvents(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := NewManager(db, bus)
	ctx := context.Background()

	sub := bus.Subscribe(ctx, []string{"task_input", "task_output"})

	task, err := mgr.Spawn(ctx, Spec{
		Type:  "exec",
		Owner: "tester",
		Payload: map[string]any{
			"code": "console.log('hi')",
		},
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if task.Status != StatusQueued {
		t.Fatalf("expected queued status")
	}

	// Expect a task_input event for spawn.
	select {
	case evt := <-sub:
		if evt.Stream != "task_input" {
			t.Fatalf("expected task_input event")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for task_input event")
	}

	if err := mgr.Send(ctx, task.ID, map[string]any{"hello": "world"}); err != nil {
		t.Fatalf("send: %v", err)
	}

	if err := mgr.RecordUpdate(ctx, task.ID, "progress", map[string]any{"pct": 50}); err != nil {
		t.Fatalf("record update: %v", err)
	}

	receivedOutput := false
	for i := 0; i < 4; i++ {
		select {
		case evt := <-sub:
			if evt.Stream == "task_output" {
				receivedOutput = true
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timeout waiting for task_output event")
		}
		if receivedOutput {
			break
		}
	}
	if !receivedOutput {
		t.Fatalf("expected task_output event")
	}

	claimed, err := mgr.ClaimQueued(ctx, "exec", 1)
	if err != nil {
		t.Fatalf("claim queued: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("expected 1 claimed task")
	}

	go func() {
		_ = mgr.Complete(ctx, task.ID, map[string]any{"ok": true})
	}()

	final, err := mgr.Await(ctx, task.ID, 5*time.Second)
	if err != nil {
		t.Fatalf("await: %v", err)
	}
	if final.Status != StatusCompleted {
		t.Fatalf("expected completed status, got %s", final.Status)
	}
}

func TestManagerCancelKill(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := NewManager(db, bus)
	ctx := context.Background()

	task, err := mgr.Spawn(ctx, Spec{Type: "exec"})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}

	if err := mgr.Cancel(ctx, task.ID, "stop"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	cancelled, err := mgr.Get(ctx, task.ID)
	if err != nil {
		t.Fatalf("get cancelled: %v", err)
	}
	if cancelled.Status != StatusCancelled {
		t.Fatalf("expected cancelled")
	}

	task2, err := mgr.Spawn(ctx, Spec{Type: "exec"})
	if err != nil {
		t.Fatalf("spawn 2: %v", err)
	}
	if err := mgr.Kill(ctx, task2.ID, "kill"); err != nil {
		t.Fatalf("kill: %v", err)
	}
	killed, err := mgr.Get(ctx, task2.ID)
	if err != nil {
		t.Fatalf("get killed: %v", err)
	}
	if killed.Status != StatusCancelled {
		t.Fatalf("expected cancelled on kill")
	}

	updates, err := mgr.ListUpdates(ctx, task2.ID, 10)
	if err != nil {
		t.Fatalf("list updates: %v", err)
	}
	if len(updates) == 0 {
		t.Fatalf("expected updates")
	}

	// Ensure result JSON parseable if present.
	if killed.Result != nil {
		if _, err := json.Marshal(killed.Result); err != nil {
			t.Fatalf("result not json: %v", err)
		}
	}
}

func TestManagerRejectsInvalidTerminalTransition(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := NewManager(db, bus)
	ctx := context.Background()

	task, err := mgr.Spawn(ctx, Spec{Type: "exec"})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if err := mgr.Complete(ctx, task.ID, map[string]any{"ok": true}); err != nil {
		t.Fatalf("complete: %v", err)
	}

	err = mgr.Fail(ctx, task.ID, "late failure")
	if !errors.Is(err, ErrInvalidStatusTransition) {
		t.Fatalf("expected invalid transition error, got %v", err)
	}

	current, err := mgr.Get(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if current.Status != StatusCompleted {
		t.Fatalf("expected completed status, got %s", current.Status)
	}
}

func TestMarkRunningRejectsTerminalTask(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := NewManager(db, bus)
	ctx := context.Background()

	task, err := mgr.Spawn(ctx, Spec{Type: "exec"})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if err := mgr.Cancel(ctx, task.ID, "done"); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	err = mgr.MarkRunning(ctx, task.ID)
	if !errors.Is(err, ErrInvalidStatusTransition) {
		t.Fatalf("expected invalid transition error, got %v", err)
	}
}
