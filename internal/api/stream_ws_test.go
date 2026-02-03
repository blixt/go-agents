package api

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/flitsinc/go-agents/internal/eventbus"
	"github.com/flitsinc/go-agents/internal/testutil"
)

type fakeWSWriter struct {
	messages [][]byte
}

func (f *fakeWSWriter) Write(_ context.Context, _ websocket.MessageType, data []byte) error {
	f.messages = append(f.messages, data)
	return nil
}

func TestStreamEventsWriter(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	writer := &fakeWSWriter{}
	go func() {
		_ = streamEvents(ctx, bus, []string{"errors"}, writer)
	}()

	_, err := bus.Push(context.Background(), eventbus.EventInput{Stream: "errors", Body: "boom"})
	if err != nil {
		t.Fatalf("push: %v", err)
	}

	deadline := time.After(2 * time.Second)
	for {
		if len(writer.messages) > 0 {
			var evt eventbus.Event
			if err := json.Unmarshal(writer.messages[0], &evt); err != nil {
				t.Fatalf("decode ws payload: %v", err)
			}
			if evt.Body != "boom" {
				t.Fatalf("unexpected event body")
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for ws message")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}
