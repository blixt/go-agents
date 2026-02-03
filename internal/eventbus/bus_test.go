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
	_, err = bus.Push(ctx, EventInput{Stream: "signals", ScopeType: "agent", ScopeID: "agent-1", Body: "agent"})
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
