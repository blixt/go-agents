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

	summaries, err := bus.List(ctx, "signals", eventbus.ListOptions{Limit: 1, Reader: "runtime"})
	if err != nil {
		t.Fatalf("list signals: %v", err)
	}
	if len(summaries) == 0 || !summaries[0].Read {
		t.Fatalf("expected wake event to be acked")
	}
}

func TestAwaitKeepsAwaitedTerminalWakeUnread(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	ctx := context.Background()

	task, err := mgr.Spawn(ctx, tasks.Spec{
		Type:  "wake",
		Owner: "agent-a",
		Metadata: map[string]any{
			"notify_target": "agent-a",
		},
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if err := mgr.MarkRunning(ctx, task.ID); err != nil {
		t.Fatalf("mark running: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := mgr.Await(ctx, task.ID, 2*time.Second)
		done <- err
	}()

	time.Sleep(25 * time.Millisecond)
	if err := mgr.Complete(ctx, task.ID, map[string]any{"ok": true}); err != nil {
		t.Fatalf("complete: %v", err)
	}

	if err := <-done; err != nil {
		t.Fatalf("await: %v", err)
	}

	summaries, err := bus.List(ctx, "task_output", eventbus.ListOptions{
		Reader: "agent-a",
		Limit:  50,
		Order:  "fifo",
	})
	if err != nil {
		t.Fatalf("list task_output: %v", err)
	}
	ids := make([]string, 0, len(summaries))
	for _, summary := range summaries {
		ids = append(ids, summary.ID)
	}
	events, err := bus.Read(ctx, "task_output", ids, "agent-a")
	if err != nil {
		t.Fatalf("read task_output: %v", err)
	}

	foundCompleted := false
	for _, evt := range events {
		if evt.ID == "" {
			continue
		}
		if evt.Metadata == nil || evt.Metadata["task_id"] != task.ID || evt.Metadata["task_kind"] != "completed" {
			continue
		}
		foundCompleted = true
		if evt.Read {
			t.Fatalf("expected awaited terminal wake event to remain unread")
		}
	}
	if !foundCompleted {
		t.Fatalf("expected completed task_output event for task %s", task.ID)
	}
}

func TestAwaitAnyKeepsCompletedTaskWakeUnread(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	ctx := context.Background()

	taskA, err := mgr.Spawn(ctx, tasks.Spec{
		Type:  "wake",
		Owner: "agent-a",
		Metadata: map[string]any{
			"notify_target": "agent-a",
		},
	})
	if err != nil {
		t.Fatalf("spawn taskA: %v", err)
	}
	taskB, err := mgr.Spawn(ctx, tasks.Spec{
		Type:  "wake",
		Owner: "agent-a",
		Metadata: map[string]any{
			"notify_target": "agent-a",
		},
	})
	if err != nil {
		t.Fatalf("spawn taskB: %v", err)
	}
	if err := mgr.MarkRunning(ctx, taskA.ID); err != nil {
		t.Fatalf("mark taskA running: %v", err)
	}
	if err := mgr.MarkRunning(ctx, taskB.ID); err != nil {
		t.Fatalf("mark taskB running: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := mgr.AwaitAny(ctx, []string{taskA.ID, taskB.ID}, 2*time.Second)
		done <- err
	}()

	time.Sleep(25 * time.Millisecond)
	if err := mgr.Complete(ctx, taskA.ID, map[string]any{"ok": true}); err != nil {
		t.Fatalf("complete taskA: %v", err)
	}

	if err := <-done; err != nil {
		t.Fatalf("await any: %v", err)
	}

	summaries, err := bus.List(ctx, "task_output", eventbus.ListOptions{
		Reader: "agent-a",
		Limit:  50,
		Order:  "fifo",
	})
	if err != nil {
		t.Fatalf("list task_output: %v", err)
	}
	ids := make([]string, 0, len(summaries))
	for _, summary := range summaries {
		ids = append(ids, summary.ID)
	}
	events, err := bus.Read(ctx, "task_output", ids, "agent-a")
	if err != nil {
		t.Fatalf("read task_output: %v", err)
	}

	foundCompleted := false
	for _, evt := range events {
		if evt.Metadata == nil || evt.Metadata["task_id"] != taskA.ID || evt.Metadata["task_kind"] != "completed" {
			continue
		}
		foundCompleted = true
		if evt.Read {
			t.Fatalf("expected completed task wake event to remain unread after AwaitAny")
		}
	}
	if !foundCompleted {
		t.Fatalf("expected completed task_output event for task %s", taskA.ID)
	}
}

func TestAwaitSeesPreexistingWakeEvent(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	ctx := context.Background()

	task, err := mgr.Spawn(ctx, tasks.Spec{
		Type:  "wake",
		Owner: "agent-a",
		Metadata: map[string]any{
			"notify_target": "agent-a",
		},
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}

	evt, err := bus.Push(ctx, eventbus.EventInput{
		Stream:    "signals",
		ScopeType: "agent",
		ScopeID:   "agent-a",
		Subject:   "wake",
		Body:      "wake",
		Metadata: map[string]any{
			"priority": "wake",
		},
	})
	if err != nil {
		t.Fatalf("push wake: %v", err)
	}

	_, err = mgr.Await(ctx, task.ID, 2*time.Second)
	wakeErr, ok := tasks.AsWakeError(err)
	if !ok {
		t.Fatalf("expected wake error, got %v", err)
	}
	if wakeErr.Event.ID != evt.ID {
		t.Fatalf("expected preexisting wake event to be returned")
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

func TestAwaitAnySeesPreexistingWakeEvent(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	ctx := context.Background()

	taskA, err := mgr.Spawn(ctx, tasks.Spec{
		Type:  "parallel",
		Owner: "agent-a",
		Metadata: map[string]any{
			"notify_target": "agent-a",
		},
	})
	if err != nil {
		t.Fatalf("spawn A: %v", err)
	}
	taskB, err := mgr.Spawn(ctx, tasks.Spec{
		Type:  "parallel",
		Owner: "agent-a",
		Metadata: map[string]any{
			"notify_target": "agent-a",
		},
	})
	if err != nil {
		t.Fatalf("spawn B: %v", err)
	}

	evt, err := bus.Push(ctx, eventbus.EventInput{
		Stream:    "signals",
		ScopeType: "agent",
		ScopeID:   "agent-a",
		Subject:   "wake",
		Body:      "wake",
		Metadata: map[string]any{
			"priority": "wake",
		},
	})
	if err != nil {
		t.Fatalf("push wake: %v", err)
	}

	result, err := mgr.AwaitAny(ctx, []string{taskA.ID, taskB.ID}, 2*time.Second)
	if _, ok := tasks.AsWakeError(err); !ok {
		t.Fatalf("expected wake error, got %v", err)
	}
	if result.WakeEvent == nil || result.WakeEvent.ID != evt.ID {
		t.Fatalf("expected preexisting wake event in result")
	}
}

func TestAwaitWakesOnOtherTaskCompletion(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	ctx := context.Background()

	waited, err := mgr.Spawn(ctx, tasks.Spec{
		Type:  "waited",
		Owner: "agent-a",
		Metadata: map[string]any{
			"notify_target": "agent-a",
		},
	})
	if err != nil {
		t.Fatalf("spawn waited: %v", err)
	}
	other, err := mgr.Spawn(ctx, tasks.Spec{
		Type:  "other",
		Owner: "agent-a",
		Metadata: map[string]any{
			"notify_target": "agent-a",
		},
	})
	if err != nil {
		t.Fatalf("spawn other: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := mgr.Await(ctx, waited.ID, 2*time.Second)
		done <- err
	}()

	time.Sleep(50 * time.Millisecond)
	if err := mgr.Complete(ctx, other.ID, map[string]any{"ok": true}); err != nil {
		t.Fatalf("complete other: %v", err)
	}

	err = <-done
	wakeErr, ok := tasks.AsWakeError(err)
	if !ok {
		t.Fatalf("expected wake error from other task completion, got %v", err)
	}
	if wakeErr.Priority != "wake" {
		t.Fatalf("expected wake priority, got %q", wakeErr.Priority)
	}
	taskID, _ := wakeErr.Event.Metadata["task_id"].(string)
	if taskID != other.ID {
		t.Fatalf("expected wake from task %s, got %s", other.ID, taskID)
	}
}

func TestTaskCompletionEventsUseWakePriority(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	ctx := context.Background()

	task, err := mgr.Spawn(ctx, tasks.Spec{
		Type:  "complete",
		Owner: "agent-a",
		Metadata: map[string]any{
			"notify_target": "agent-a",
		},
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if err := mgr.Complete(ctx, task.ID, map[string]any{"ok": true}); err != nil {
		t.Fatalf("complete: %v", err)
	}

	list, err := bus.List(ctx, "task_output", eventbus.ListOptions{Limit: 20})
	if err == nil && len(list) == 0 {
		list, err = bus.List(ctx, "task_output", eventbus.ListOptions{Reader: "agent-a", Limit: 20})
	}
	if err != nil {
		t.Fatalf("list task_output: %v", err)
	}
	if len(list) == 0 {
		t.Fatalf("expected task_output events")
	}
	ids := make([]string, 0, len(list))
	for _, summary := range list {
		ids = append(ids, summary.ID)
	}
	events, err := bus.Read(ctx, "task_output", ids, "")
	if err != nil {
		t.Fatalf("read task_output: %v", err)
	}

	found := false
	for _, evt := range events {
		taskID, _ := evt.Metadata["task_id"].(string)
		kind, _ := evt.Metadata["task_kind"].(string)
		if taskID == task.ID && kind == "completed" {
			priority, _ := evt.Metadata["priority"].(string)
			if priority != "wake" {
				t.Fatalf("expected wake priority on completion, got %q", priority)
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected completed task_output event for %s", task.ID)
	}
}

func TestAwaitIgnoresForeignScopedWake(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	ctx := context.Background()

	task, err := mgr.Spawn(ctx, tasks.Spec{
		Type:  "wake",
		Owner: "agent-a",
		Metadata: map[string]any{
			"notify_target": "agent-a",
		},
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}

	type awaitResult struct {
		task tasks.Task
		err  error
	}
	done := make(chan awaitResult, 1)
	go func() {
		result, err := mgr.Await(ctx, task.ID, 2*time.Second)
		done <- awaitResult{task: result, err: err}
	}()

	time.Sleep(50 * time.Millisecond)
	_, err = bus.Push(ctx, eventbus.EventInput{
		Stream:    "signals",
		ScopeType: "agent",
		ScopeID:   "agent-b",
		Subject:   "wake",
		Body:      "wake",
		Metadata: map[string]any{
			"priority": "wake",
		},
	})
	if err != nil {
		t.Fatalf("push foreign wake: %v", err)
	}

	time.Sleep(100 * time.Millisecond)
	if err := mgr.Complete(ctx, task.ID, map[string]any{"ok": true}); err != nil {
		t.Fatalf("complete: %v", err)
	}

	result := <-done
	if result.err != nil {
		t.Fatalf("expected completion, got error: %v", result.err)
	}
	if result.task.Status != tasks.StatusCompleted {
		t.Fatalf("expected completed task, got %s", result.task.Status)
	}
}

func TestAwaitMaintainsSingleSubscription(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	ctx := context.Background()

	task, err := mgr.Spawn(ctx, tasks.Spec{
		Type:  "wait",
		Owner: "agent-a",
		Metadata: map[string]any{
			"notify_target": "agent-a",
		},
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}

	awaitCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := mgr.Await(awaitCtx, task.ID, 5*time.Second)
		done <- err
	}()

	deadline := time.Now().Add(2 * time.Second)
	for bus.SubscriberCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if bus.SubscriberCount() == 0 {
		t.Fatalf("expected await subscriber")
	}

	for i := 0; i < 5; i++ {
		_, err := bus.Push(ctx, eventbus.EventInput{
			Stream:    "signals",
			ScopeType: "agent",
			ScopeID:   "agent-b",
			Subject:   "wake",
			Body:      "wake",
			Metadata: map[string]any{
				"priority": "wake",
			},
		})
		if err != nil {
			t.Fatalf("push foreign wake: %v", err)
		}
	}

	time.Sleep(100 * time.Millisecond)
	if count := bus.SubscriberCount(); count != 1 {
		t.Fatalf("expected one subscriber, got %d", count)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("await did not stop after cancel")
	}
}

func TestAwaitIgnoresContextSuppressedWakeEvent(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	ctx := context.Background()

	task, err := mgr.Spawn(ctx, tasks.Spec{
		Type:  "exec",
		Owner: "agent-a",
		Metadata: map[string]any{
			"notify_target": "agent-a",
		},
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}

	evt, err := bus.Push(ctx, eventbus.EventInput{
		Stream:    "messages",
		ScopeType: "agent",
		ScopeID:   "agent-a",
		Subject:   "Message from external",
		Body:      "wake",
		Metadata: map[string]any{
			"priority": "wake",
		},
	})
	if err != nil {
		t.Fatalf("push wake: %v", err)
	}

	awaitCtx := tasks.WithIgnoredWakeEventIDs(ctx, []string{evt.ID})
	_, err = mgr.Await(awaitCtx, task.ID, 200*time.Millisecond)
	if !tasks.IsAwaitTimeout(err) {
		t.Fatalf("expected await timeout when wake event is suppressed, got %v", err)
	}

	events, readErr := bus.Read(ctx, "messages", []string{evt.ID}, "agent-a")
	if readErr != nil || len(events) == 0 {
		t.Fatalf("read wake event: %v", readErr)
	}
	if !events[0].Read {
		t.Fatalf("expected suppressed wake event to be acked")
	}
}
