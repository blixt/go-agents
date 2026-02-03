package agenttools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flitsinc/go-agents/internal/eventbus"
	"github.com/flitsinc/go-agents/internal/tasks"
	"github.com/flitsinc/go-agents/internal/testutil"
	"github.com/flitsinc/go-llms/content"
	"github.com/flitsinc/go-llms/llms"
	llmtools "github.com/flitsinc/go-llms/tools"
)

func TestExecToolSpawnsTask(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	tool := ExecTool(mgr)

	tc := llms.ToolCall{ID: "call-1", Name: "exec"}
	ctx := context.WithValue(context.Background(), llms.ToolCallContextKey, tc)
	runner := llmtools.NewRunner(ctx, nil, func(status string) {})

	params := ExecParams{Code: "globalThis.result = 1", ID: "session-1"}
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
