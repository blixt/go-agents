package eventbus

import (
	"context"
	"testing"
	"time"

	"github.com/flitsinc/go-agents/internal/testutil"
)

func TestBusPushListReadAck(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := NewBus(db)
	ctx := context.Background()

	first, err := bus.Push(ctx, EventInput{Stream: "errors", Subject: "First", Body: "first"})
	if err != nil {
		t.Fatalf("push first: %v", err)
	}
	_, err = bus.Push(ctx, EventInput{Stream: "errors", Subject: "Second", Body: "second"})
	if err != nil {
		t.Fatalf("push second: %v", err)
	}

	items, err := bus.List(ctx, "errors", ListOptions{Order: "fifo"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 events, got %d", len(items))
	}
	if items[0].ID != first.ID {
		t.Fatalf("expected fifo order")
	}

	events, err := bus.Read(ctx, "errors", []string{first.ID}, "tester")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(events) != 1 || events[0].ID != first.ID {
		t.Fatalf("expected event")
	}
	if events[0].Read {
		t.Fatalf("expected unread before ack")
	}

	if err := bus.Ack(ctx, "errors", []string{first.ID}, "tester"); err != nil {
		t.Fatalf("ack: %v", err)
	}
	readBack, err := bus.Read(ctx, "errors", []string{first.ID}, "tester")
	if err != nil {
		t.Fatalf("read after ack: %v", err)
	}
	if !readBack[0].Read {
		t.Fatalf("expected read after ack")
	}
}

func TestBusScopesAndSubscribe(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := NewBus(db)
	ctx := context.Background()

	_, err := bus.Push(ctx, EventInput{Stream: "signals", ScopeType: "global", ScopeID: "*", Body: "global"})
	if err != nil {
		t.Fatalf("push global: %v", err)
	}
	_, err = bus.Push(ctx, EventInput{Stream: "signals", ScopeType: "task", ScopeID: "agent-1", Body: "agent"})
	if err != nil {
		t.Fatalf("push agent: %v", err)
	}

	items, err := bus.List(ctx, "signals", ListOptions{Reader: "agent-1"})
	if err != nil {
		t.Fatalf("list scoped: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected both global and agent events")
	}

	subCtx, cancel := context.WithCancel(ctx)
	sub := bus.Subscribe(subCtx, []string{"signals"})

	go func() {
		defer cancel()
		_, _ = bus.Push(ctx, EventInput{Stream: "signals", Body: "ping"})
	}()

	select {
	case evt := <-sub:
		if evt.Body != "ping" {
			t.Fatalf("unexpected body: %s", evt.Body)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for subscription event")
	}
}

func TestBusPushWithSourceIDPreReads(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := NewBus(db)
	ctx := context.Background()

	evt, err := bus.Push(ctx, EventInput{
		Stream:   "signals",
		Body:     "self-event",
		SourceID: "agent-1",
	})
	if err != nil {
		t.Fatalf("push with source_id: %v", err)
	}
	if len(evt.ReadBy) != 1 || evt.ReadBy[0] != "agent-1" {
		t.Fatalf("expected returned event ReadBy=[agent-1], got %v", evt.ReadBy)
	}

	// List as agent-1 — should be pre-read.
	summaries, err := bus.List(ctx, "signals", ListOptions{Reader: "agent-1"})
	if err != nil {
		t.Fatalf("list as agent-1: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(summaries))
	}
	if !summaries[0].Read {
		t.Fatalf("expected summary.Read=true for agent-1")
	}

	// List as agent-2 — should be unread.
	summaries2, err := bus.List(ctx, "signals", ListOptions{Reader: "agent-2"})
	if err != nil {
		t.Fatalf("list as agent-2: %v", err)
	}
	if len(summaries2) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(summaries2))
	}
	if summaries2[0].Read {
		t.Fatalf("expected summary.Read=false for agent-2")
	}

	// Read as agent-1 — should be pre-read.
	events, err := bus.Read(ctx, "signals", []string{evt.ID}, "agent-1")
	if err != nil {
		t.Fatalf("read as agent-1: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if !events[0].Read {
		t.Fatalf("expected evt.Read=true for agent-1")
	}

	// Read as agent-2 — should be unread.
	events2, err := bus.Read(ctx, "signals", []string{evt.ID}, "agent-2")
	if err != nil {
		t.Fatalf("read as agent-2: %v", err)
	}
	if len(events2) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events2))
	}
	if events2[0].Read {
		t.Fatalf("expected evt.Read=false for agent-2")
	}
}
