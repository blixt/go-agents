package engine

import (
	"testing"
	"time"

	"github.com/flitsinc/go-agents/internal/eventbus"
)

func TestHistoryEntryFromEventPreservesContentWhitespace(t *testing.T) {
	evt := eventbus.Event{
		ID:        "evt-1",
		Stream:    "history",
		ScopeType: "task",
		ScopeID:   "agent-1",
		CreatedAt: time.Now().UTC(),
		Payload: map[string]any{
			"agent_id":   "agent-1",
			"generation": 1,
			"type":       "reasoning",
			"role":       "assistant",
			"content":    " Ishould use execto gather data.",
		},
	}

	entry, ok := HistoryEntryFromEvent(evt)
	if !ok {
		t.Fatalf("expected parseable history entry")
	}
	if entry.Content != " Ishould use execto gather data." {
		t.Fatalf("expected content whitespace preserved, got %q", entry.Content)
	}
}
