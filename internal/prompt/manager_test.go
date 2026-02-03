package prompt

import (
	"context"
	"strings"
	"testing"

	"github.com/flitsinc/go-agents/internal/eventbus"
	"github.com/flitsinc/go-agents/internal/testutil"
)

type fakeCompactor struct {
	called bool
}

func (f *fakeCompactor) Summarize(_ context.Context, input string) (string, error) {
	f.called = true
	return "summary: " + input, nil
}

func TestManagerBuildPromptAndCompaction(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		_, _ = bus.Push(ctx, eventbus.EventInput{Stream: "errors", Body: "err"})
	}

	compactor := &fakeCompactor{}
	mgr := &Manager{
		Policy:     StreamPolicy{UpdateStreams: []string{"errors"}, Reader: "tester", Ack: true},
		Compactor:  compactor,
		MaxUpdates: 1,
		CacheHint:  "short",
	}

	prompt, text, err := mgr.BuildSystemPrompt(ctx, bus)
	if err != nil {
		t.Fatalf("build prompt: %v", err)
	}
	if !compactor.called {
		t.Fatalf("expected compactor called")
	}
	if !strings.Contains(text, "Summary:") {
		t.Fatalf("expected summary in prompt")
	}
	if len(prompt) == 0 {
		t.Fatalf("expected content prompt")
	}
}

func TestStreamUpdatesRenderAndAck(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	ctx := context.Background()

	evt, _ := bus.Push(ctx, eventbus.EventInput{Stream: "signals", Subject: "ping", Body: "ping"})

	updates, err := CollectUpdates(ctx, bus, StreamPolicy{UpdateStreams: []string{"signals"}, Reader: "tester", Ack: true})
	if err != nil {
		t.Fatalf("collect updates: %v", err)
	}
	if len(updates.Events) != 1 {
		t.Fatalf("expected 1 event")
	}

	text := RenderUpdates(updates)
	if !strings.Contains(text, "ping") {
		t.Fatalf("expected ping in render")
	}

	readBack, _ := bus.Read(ctx, "signals", []string{evt.ID}, "tester")
	if len(readBack) == 0 || !readBack[0].Read {
		t.Fatalf("expected acked event")
	}
}
