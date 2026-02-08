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
			Stream:    "messages",
			ScopeType: "task",
			ScopeID:   "agent-a",
			Subject:   "Message from external",
			Body:      "wake now",
			Metadata: map[string]any{
				"priority": "wake",
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
	result := tool.Run(llmtools.NopRunner, raw)
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
