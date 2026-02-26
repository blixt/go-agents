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
	llmtools "github.com/flitsinc/go-llms/tools"
)

func decodeToolJSONPayload(t *testing.T, result llmtools.Result) map[string]any {
	t.Helper()
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
	return payload
}

func TestAwaitTaskToolWakeIncludesWakeEventID(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	tool := AwaitTaskTool(mgr)

	task, err := mgr.Spawn(context.Background(), tasks.Spec{
		Type:  "exec",
		Owner: "agent-a",
	})
	if err != nil {
		t.Fatalf("spawn task: %v", err)
	}

	wakeEventID := make(chan string, 1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		evt, err := bus.Push(context.Background(), eventbus.EventInput{
			Stream:    "task_input",
			ScopeType: "task",
			ScopeID:   "agent-a",
			Subject:   "Message from external",
			Body:      "wake now",
			Metadata: map[string]any{
				"priority": "wake",
				"kind":     "message",
			},
		})
		if err == nil {
			wakeEventID <- evt.ID
		}
	}()

	waitSec := 2
	raw, _ := json.Marshal(AwaitTaskParams{
		TaskID:      task.ID,
		WaitSeconds: &waitSec,
	})
	payload := decodeToolJSONPayload(t, tool.Run(llmtools.NopRunner, raw))

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

func TestAwaitTaskToolAgentWaitsForFreshAssistantOutput(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	tool := AwaitTaskTool(mgr)

	task, err := mgr.Spawn(context.Background(), tasks.Spec{
		Type:  "agent",
		Owner: "agent-a",
	})
	if err != nil {
		t.Fatalf("spawn task: %v", err)
	}
	if err := mgr.MarkRunning(context.Background(), task.ID); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	if err := mgr.RecordUpdate(context.Background(), task.ID, "assistant_output", map[string]any{
		"text": "ECHO: alpha",
	}); err != nil {
		t.Fatalf("record baseline assistant_output: %v", err)
	}
	if err := mgr.Complete(context.Background(), task.ID, map[string]any{"output": "ECHO: alpha"}); err != nil {
		t.Fatalf("complete task: %v", err)
	}

	go func() {
		time.Sleep(120 * time.Millisecond)
		_ = mgr.RecordUpdate(context.Background(), task.ID, "assistant_output", map[string]any{
			"text": "ECHO: beta",
		})
	}()

	waitSec := 1
	raw, _ := json.Marshal(AwaitTaskParams{
		TaskID:      task.ID,
		WaitSeconds: &waitSec,
	})
	payload := decodeToolJSONPayload(t, tool.Run(llmtools.NopRunner, raw))
	if payload["status"] != "completed" {
		t.Fatalf("expected completed status, got %v", payload["status"])
	}
	resultMap, _ := payload["result"].(map[string]any)
	if resultMap == nil {
		t.Fatalf("expected result map, got %T", payload["result"])
	}
	if resultMap["output"] != "ECHO: beta" {
		t.Fatalf("expected fresh output ECHO: beta, got %v", resultMap["output"])
	}
	if _, hasAwaitErr := payload["await_error"]; hasAwaitErr {
		t.Fatalf("expected no await_error when fresh assistant output arrives, got %v", payload["await_error"])
	}
}

func TestAwaitTaskToolAgentTimesOutWithoutFreshOutput(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	tool := AwaitTaskTool(mgr)

	task, err := mgr.Spawn(context.Background(), tasks.Spec{
		Type:  "agent",
		Owner: "agent-a",
	})
	if err != nil {
		t.Fatalf("spawn task: %v", err)
	}
	if err := mgr.MarkRunning(context.Background(), task.ID); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	if err := mgr.RecordUpdate(context.Background(), task.ID, "assistant_output", map[string]any{
		"text": "ECHO: alpha",
	}); err != nil {
		t.Fatalf("record baseline assistant_output: %v", err)
	}
	if err := mgr.Complete(context.Background(), task.ID, map[string]any{"output": "ECHO: alpha"}); err != nil {
		t.Fatalf("complete task: %v", err)
	}

	waitSec := 1
	raw, _ := json.Marshal(AwaitTaskParams{
		TaskID:      task.ID,
		WaitSeconds: &waitSec,
	})
	payload := decodeToolJSONPayload(t, tool.Run(llmtools.NopRunner, raw))
	if payload["pending"] != true {
		t.Fatalf("expected pending=true when no fresh assistant output arrives, got %v", payload["pending"])
	}
	if payload["background"] != true {
		t.Fatalf("expected background=true when timed out, got %v", payload["background"])
	}
	if payload["await_error"] != tasks.ErrAwaitTimeout.Error() {
		t.Fatalf("expected await timeout, got %v", payload["await_error"])
	}
	if _, hasResult := payload["result"]; hasResult {
		t.Fatalf("did not expect stale result in timeout response, got %v", payload["result"])
	}
}
