package tasks_test

import (
	"context"
	"testing"
	"time"

	"github.com/flitsinc/go-agents/internal/eventbus"
	"github.com/flitsinc/go-agents/internal/tasks"
	"github.com/flitsinc/go-agents/internal/testutil"
)

func TestKillParentCancelsChild(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	ctx := context.Background()

	parent, err := mgr.Spawn(ctx, tasks.Spec{Type: "parent", Mode: "async"})
	if err != nil {
		t.Fatalf("spawn parent: %v", err)
	}
	child, err := mgr.Spawn(ctx, tasks.Spec{Type: "child", ParentID: parent.ID, Mode: "async"})
	if err != nil {
		t.Fatalf("spawn child: %v", err)
	}

	if err := mgr.Kill(ctx, parent.ID, "shutdown"); err != nil {
		t.Fatalf("kill parent: %v", err)
	}

	updatedParent, err := mgr.Get(ctx, parent.ID)
	if err != nil {
		t.Fatalf("get parent: %v", err)
	}
	if updatedParent.Status != tasks.StatusCancelled {
		t.Fatalf("expected parent cancelled, got %s", updatedParent.Status)
	}

	updatedChild, err := mgr.Get(ctx, child.ID)
	if err != nil {
		t.Fatalf("get child: %v", err)
	}
	if updatedChild.Status != tasks.StatusCancelled {
		t.Fatalf("expected child cancelled, got %s", updatedChild.Status)
	}

	updates, err := mgr.ListUpdates(ctx, child.ID, 10)
	if err != nil {
		t.Fatalf("list updates: %v", err)
	}
	if len(updates) == 0 || updates[len(updates)-1].Kind != "killed" {
		t.Fatalf("expected child killed update, got %+v", updates)
	}
}

func TestAwaitTimeoutBecomesAsync(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	ctx := context.Background()

	task, err := mgr.Spawn(ctx, tasks.Spec{Type: "sync", Mode: "sync"})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}

	_, err = mgr.Await(ctx, task.ID, 50*time.Millisecond)
	if !tasks.IsAwaitTimeout(err) {
		t.Fatalf("expected timeout error, got %v", err)
	}
	current, err := mgr.Get(ctx, task.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if current.Status == tasks.StatusCompleted {
		t.Fatalf("expected task still running/queued")
	}

	if err := mgr.Complete(ctx, task.ID, map[string]any{"ok": true}); err != nil {
		t.Fatalf("complete: %v", err)
	}

	result, err := mgr.Await(ctx, task.ID, 2*time.Second)
	if err != nil {
		t.Fatalf("await: %v", err)
	}
	if result.Status != tasks.StatusCompleted {
		t.Fatalf("expected completed task, got %s", result.Status)
	}
}

func TestSendInputAndReadOutput(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	ctx := context.Background()

	task, err := mgr.Spawn(ctx, tasks.Spec{Type: "io"})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}

	if err := mgr.Send(ctx, task.ID, map[string]any{"line": "ping"}); err != nil {
		t.Fatalf("send: %v", err)
	}

	updates, err := mgr.ListUpdates(ctx, task.ID, 10)
	if err != nil {
		t.Fatalf("list updates: %v", err)
	}
	if len(updates) == 0 || updates[len(updates)-1].Kind != "input" {
		t.Fatalf("expected input update, got %+v", updates)
	}

	if _, err := bus.Push(ctx, eventbus.EventInput{
		Stream:  "task_output",
		Subject: "stdout",
		Body:    "stdout",
		Metadata: map[string]any{
			"task_id": task.ID,
			"kind":    "stdout",
		},
		Payload: map[string]any{"text": "pong"},
	}); err != nil {
		t.Fatalf("push output: %v", err)
	}

	list, err := bus.List(ctx, "task_output", eventbus.ListOptions{Limit: 5})
	if err != nil {
		t.Fatalf("list output: %v", err)
	}
	if len(list) == 0 {
		t.Fatalf("expected task_output events")
	}
	events, err := bus.Read(ctx, "task_output", []string{list[0].ID}, "")
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if len(events) == 0 || events[0].Payload == nil {
		t.Fatalf("expected output payload")
	}
}

func TestAwaitWakeEvent(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	ctx := context.Background()

	task, err := mgr.Spawn(ctx, tasks.Spec{Type: "wake"})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := mgr.Await(ctx, task.ID, 2*time.Second)
		done <- err
	}()

	time.Sleep(50 * time.Millisecond)
	evt, err := bus.Push(ctx, eventbus.EventInput{
		Stream:  "signals",
		Subject: "wake",
		Body:    "wake",
		Metadata: map[string]any{
			"priority": "wake",
		},
	})
	if err != nil {
		t.Fatalf("push wake: %v", err)
	}

	err = <-done
	wakeErr, ok := tasks.AsWakeError(err)
	if !ok {
		t.Fatalf("expected wake error, got %v", err)
	}
	if wakeErr.Event.ID != evt.ID {
		t.Fatalf("expected wake event")
	}

	summaries, err := bus.List(ctx, "signals", eventbus.ListOptions{Limit: 1, Reader: "operator"})
	if err != nil {
		t.Fatalf("list signals: %v", err)
	}
	if len(summaries) == 0 || !summaries[0].Read {
		t.Fatalf("expected wake event to be acked")
	}
}

func TestAwaitInterruptEvent(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	ctx := context.Background()

	task, err := mgr.Spawn(ctx, tasks.Spec{Type: "interrupt"})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := mgr.Await(ctx, task.ID, 2*time.Second)
		done <- err
	}()

	time.Sleep(50 * time.Millisecond)
	_, err = bus.Push(ctx, eventbus.EventInput{
		Stream:  "signals",
		Subject: "interrupt",
		Body:    "interrupt",
		Metadata: map[string]any{
			"priority": "interrupt",
		},
	})
	if err != nil {
		t.Fatalf("push interrupt: %v", err)
	}

	err = <-done
	if !tasks.IsInterrupt(err) {
		t.Fatalf("expected interrupt error, got %v", err)
	}
}

func TestAwaitAnyParallelWake(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	ctx := context.Background()

	taskA, err := mgr.Spawn(ctx, tasks.Spec{Type: "parallel"})
	if err != nil {
		t.Fatalf("spawn A: %v", err)
	}
	taskB, err := mgr.Spawn(ctx, tasks.Spec{Type: "parallel"})
	if err != nil {
		t.Fatalf("spawn B: %v", err)
	}

	done := make(chan error, 1)
	var result tasks.AwaitAnyResult
	go func() {
		var err error
		result, err = mgr.AwaitAny(ctx, []string{taskA.ID, taskB.ID}, 2*time.Second)
		done <- err
	}()

	time.Sleep(50 * time.Millisecond)
	_, err = bus.Push(ctx, eventbus.EventInput{
		Stream:  "signals",
		Subject: "wake",
		Body:    "wake",
		Metadata: map[string]any{
			"priority": "wake",
		},
	})
	if err != nil {
		t.Fatalf("push wake: %v", err)
	}

	err = <-done
	if _, ok := tasks.AsWakeError(err); !ok {
		t.Fatalf("expected wake error, got %v", err)
	}
	if result.WakeEvent == nil {
		t.Fatalf("expected wake event in result")
	}
	if len(result.PendingIDs) != 2 {
		t.Fatalf("expected pending tasks, got %v", result.PendingIDs)
	}

	if err := mgr.Complete(ctx, taskA.ID, map[string]any{"ok": true}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	final, err := mgr.AwaitAny(ctx, []string{taskA.ID, taskB.ID}, 2*time.Second)
	if err != nil {
		t.Fatalf("await any: %v", err)
	}
	if final.TaskID != taskA.ID {
		t.Fatalf("expected completed task A, got %s", final.TaskID)
	}
}
