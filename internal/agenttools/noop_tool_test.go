package agenttools

import (
	"encoding/json"
	"testing"

	"github.com/flitsinc/go-llms/content"
	llmtools "github.com/flitsinc/go-llms/tools"
)

func TestNoopToolReturnsIdle(t *testing.T) {
	tool := NoopTool()
	raw, _ := json.Marshal(NoopParams{Comment: "waiting on updates"})
	result := tool.Run(llmtools.NopRunner, raw)
	if result.Error() != nil {
		t.Fatalf("unexpected error: %v", result.Error())
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
	if payload["status"] != "idle" {
		t.Fatalf("expected idle status, got %v", payload["status"])
	}
	if payload["comment"] != "waiting on updates" {
		t.Fatalf("expected comment to round-trip, got %v", payload["comment"])
	}
}
