package state_test

import (
	"context"
	"testing"

	"github.com/flitsinc/go-agents/internal/state"
	"github.com/flitsinc/go-agents/internal/testutil"
)

func TestStoreAgentsAndActions(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	store := state.NewStore(db)
	ctx := context.Background()

	agent, err := store.CreateAgent(ctx, "operator", "idle")
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	agents, err := store.ListAgents(ctx, 10)
	if err != nil {
		t.Fatalf("list agents: %v", err)
	}
	if len(agents) == 0 || agents[0].ID != agent.ID {
		t.Fatalf("expected agent in list")
	}

	action, err := store.CreateAction(ctx, agent.ID, "run", "queued", map[string]any{"kind": "exec"})
	if err != nil {
		t.Fatalf("create action: %v", err)
	}
	actions, err := store.ListActions(ctx, 10)
	if err != nil {
		t.Fatalf("list actions: %v", err)
	}
	if len(actions) == 0 || actions[0].ID != action.ID {
		t.Fatalf("expected action in list")
	}
}
