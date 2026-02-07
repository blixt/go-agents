package agenttools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/flitsinc/go-agents/internal/eventbus"
	"github.com/flitsinc/go-agents/internal/tasks"
	"github.com/flitsinc/go-agents/internal/testutil"
	"github.com/flitsinc/go-llms/content"
	"github.com/flitsinc/go-llms/llms"
	llmtools "github.com/flitsinc/go-llms/tools"
)

func waitSecondsPtr(v int) *int {
	return &v
}

func TestExecToolSpawnsTask(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	tool := ExecTool(mgr)

	tc := llms.ToolCall{ID: "call-1", Name: "exec"}
	ctx := context.WithValue(context.Background(), llms.ToolCallContextKey, tc)
	runner := llmtools.NewRunner(ctx, nil, func(status string) {})

	params := ExecParams{Code: "globalThis.result = 1", ID: "session-1", WaitSeconds: waitSecondsPtr(0)}
	raw, _ := json.Marshal(params)
	result := tool.Run(runner, raw)
	if result.Error() != nil {
		t.Fatalf("tool error: %v", result.Error())
	}

	items := result.Content()
	if len(items) == 0 {
		t.Fatalf("expected content")
	}

	var payload map[string]any
	if jsonItem, ok := items[0].(*content.JSON); ok {
		_ = json.Unmarshal(jsonItem.Data, &payload)
	} else {
		for _, item := range items {
			if jsonItem, ok := item.(*content.JSON); ok {
				_ = json.Unmarshal(jsonItem.Data, &payload)
				break
			}
		}
	}

	taskID, _ := payload["task_id"].(string)
	if taskID == "" {
		t.Fatalf("expected task_id")
	}

	task, err := mgr.Get(context.Background(), taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if task.Payload["id"] != "session-1" {
		t.Fatalf("expected session id in payload")
	}
}

func TestExecToolValidatesCode(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	mgr := tasks.NewManager(db, nil)
	tool := ExecTool(mgr)
	runner := llmtools.NopRunner

	params := ExecParams{Code: ""}
	raw, _ := json.Marshal(params)
	result := tool.Run(runner, raw)
	if result.Error() == nil {
		t.Fatalf("expected error for empty code")
	}
}

func TestExecToolWaitsForCompletion(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	tool := ExecTool(mgr)

	tc := llms.ToolCall{ID: "call-2", Name: "exec"}
	ctx := context.WithValue(context.Background(), llms.ToolCallContextKey, tc)
	runner := llmtools.NewRunner(ctx, nil, func(status string) {})

	go func() {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			items, _ := mgr.List(context.Background(), tasks.ListFilter{
				Type:  "exec",
				Limit: 5,
			})
			if len(items) > 0 {
				time.Sleep(50 * time.Millisecond)
				_ = mgr.Complete(context.Background(), items[0].ID, map[string]any{"ok": true})
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	params := ExecParams{Code: "globalThis.result = 1", WaitSeconds: waitSecondsPtr(2)}
	raw, _ := json.Marshal(params)
	result := tool.Run(runner, raw)
	if result.Error() != nil {
		t.Fatalf("tool error: %v", result.Error())
	}

	var payload map[string]any
	for _, item := range result.Content() {
		if jsonItem, ok := item.(*content.JSON); ok {
			_ = json.Unmarshal(jsonItem.Data, &payload)
			break
		}
	}
	if payload == nil {
		t.Fatalf("expected JSON payload")
	}
	if payload["status"] != "completed" {
		t.Fatalf("expected completed status, got %v", payload["status"])
	}
	resultMap, _ := payload["result"].(map[string]any)
	if resultMap == nil || resultMap["ok"] != true {
		t.Fatalf("expected result ok=true, got %v", payload["result"])
	}
}

func TestExecToolRequiresWaitSeconds(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	tool := ExecTool(mgr)

	runner := llmtools.NopRunner
	params := ExecParams{Code: "globalThis.result = 1"}
	raw, _ := json.Marshal(params)
	result := tool.Run(runner, raw)
	if result.Error() == nil {
		t.Fatalf("expected error when wait_seconds is missing")
	}
}

func TestExecToolWaitZeroReturnsBackgroundPending(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	tool := ExecTool(mgr)

	tc := llms.ToolCall{ID: "call-3", Name: "exec"}
	ctx := context.WithValue(context.Background(), llms.ToolCallContextKey, tc)
	runner := llmtools.NewRunner(ctx, nil, func(status string) {})

	params := ExecParams{Code: "globalThis.result = 1", WaitSeconds: waitSecondsPtr(0)}
	raw, _ := json.Marshal(params)
	result := tool.Run(runner, raw)
	if result.Error() != nil {
		t.Fatalf("tool error: %v", result.Error())
	}

	var payload map[string]any
	for _, item := range result.Content() {
		if jsonItem, ok := item.(*content.JSON); ok {
			_ = json.Unmarshal(jsonItem.Data, &payload)
			break
		}
	}
	if payload == nil {
		t.Fatalf("expected JSON payload")
	}
	if payload["task_id"] == "" {
		t.Fatalf("expected task_id in payload")
	}
	if payload["pending"] != true {
		t.Fatalf("expected pending=true, got %v", payload["pending"])
	}
	if payload["background"] != true {
		t.Fatalf("expected background=true, got %v", payload["background"])
	}
}

func TestExecToolWakeIncludesWakeEventID(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	tool := ExecTool(mgr)

	tc := llms.ToolCall{ID: "call-4", Name: "exec"}
	ctx := context.WithValue(context.Background(), llms.ToolCallContextKey, tc)
	runner := llmtools.NewRunner(ctx, nil, func(status string) {})

	wakeEventID := make(chan string, 1)
	go func() {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			items, _ := mgr.List(context.Background(), tasks.ListFilter{
				Type:  "exec",
				Owner: "agent",
				Limit: 5,
			})
			if len(items) == 0 {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			evt, err := bus.Push(context.Background(), eventbus.EventInput{
				Stream:    "messages",
				ScopeType: "agent",
				ScopeID:   "agent",
				Subject:   "Message from external",
				Body:      "wake now",
				Metadata: map[string]any{
					"priority": "wake",
				},
			})
			if err == nil {
				wakeEventID <- evt.ID
			}
			return
		}
	}()

	params := ExecParams{Code: "globalThis.result = 1", WaitSeconds: waitSecondsPtr(2)}
	raw, _ := json.Marshal(params)
	result := tool.Run(runner, raw)
	if result.Error() != nil {
		t.Fatalf("tool error: %v", result.Error())
	}

	var payload map[string]any
	for _, item := range result.Content() {
		if jsonItem, ok := item.(*content.JSON); ok {
			_ = json.Unmarshal(jsonItem.Data, &payload)
			break
		}
	}
	if payload == nil {
		t.Fatalf("expected JSON payload")
	}

	var expectedWakeID string
	select {
	case expectedWakeID = <-wakeEventID:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for wake event id")
	}

	gotWakeID, _ := payload["wake_event_id"].(string)
	if gotWakeID == "" {
		t.Fatalf("expected wake_event_id in payload")
	}
	if gotWakeID != expectedWakeID {
		t.Fatalf("expected wake_event_id %q, got %q", expectedWakeID, gotWakeID)
	}
	if payload["background"] != true {
		t.Fatalf("expected background=true, got %v", payload["background"])
	}
	if payload["pending"] != true {
		t.Fatalf("expected pending=true, got %v", payload["pending"])
	}
}

func TestExecToolCanSuppressKnownWakeEvent(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	tool := ExecTool(mgr)

	evt, err := bus.Push(context.Background(), eventbus.EventInput{
		Stream:    "messages",
		ScopeType: "agent",
		ScopeID:   "agent",
		Subject:   "Message from external",
		Body:      "wake now",
		Metadata: map[string]any{
			"priority": "wake",
		},
	})
	if err != nil {
		t.Fatalf("push wake event: %v", err)
	}

	tc := llms.ToolCall{ID: "call-5", Name: "exec"}
	ctx := context.WithValue(context.Background(), llms.ToolCallContextKey, tc)
	ctx = tasks.WithIgnoredWakeEventIDs(ctx, []string{evt.ID})
	runner := llmtools.NewRunner(ctx, nil, func(status string) {})

	params := ExecParams{Code: "globalThis.result = 1", WaitSeconds: waitSecondsPtr(1)}
	raw, _ := json.Marshal(params)
	result := tool.Run(runner, raw)
	if result.Error() != nil {
		t.Fatalf("tool error: %v", result.Error())
	}

	var payload map[string]any
	for _, item := range result.Content() {
		if jsonItem, ok := item.(*content.JSON); ok {
			_ = json.Unmarshal(jsonItem.Data, &payload)
			break
		}
	}
	if payload == nil {
		t.Fatalf("expected JSON payload")
	}
	awaitError, _ := payload["await_error"].(string)
	if awaitError == "" {
		t.Fatalf("expected await_error in payload")
	}
	if awaitError == "awoken by wake event" {
		t.Fatalf("expected suppressed wake event to not awaken await, got %v", awaitError)
	}
	if _, ok := payload["wake_event_id"]; ok {
		t.Fatalf("expected no wake_event_id when wake was suppressed, got %v", payload["wake_event_id"])
	}
}
