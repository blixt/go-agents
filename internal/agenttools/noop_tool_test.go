package agenttools

import (
	"encoding/json"
	"testing"

	llmtools "github.com/flitsinc/go-llms/tools"
)

func TestNoopToolReturnsIdle(t *testing.T) {
	tool := NoopTool()
	raw, _ := json.Marshal(NoopParams{Comment: "waiting on updates"})
	result := tool.Run(llmtools.NopRunner, raw)
	if result.Error() != nil {
		t.Fatalf("unexpected error: %v", result.Error())
	}

	payload := decodeToolPayload(t, result)
	if payload["status"] != "idle" {
		t.Fatalf("expected idle status, got %v", payload["status"])
	}
	if payload["comment"] != "waiting on updates" {
		t.Fatalf("expected comment to round-trip, got %v", payload["comment"])
	}
}
