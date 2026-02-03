package engine

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/flitsinc/go-agents/internal/ai"
	"github.com/flitsinc/go-agents/internal/eventbus"
	"github.com/flitsinc/go-agents/internal/tasks"
	"github.com/flitsinc/go-agents/internal/testutil"
	"github.com/flitsinc/go-llms/content"
	"github.com/flitsinc/go-llms/llms"
	llmtools "github.com/flitsinc/go-llms/tools"
)

type loopProvider struct{}

type loopStream struct{}

func (p *loopProvider) Company() string              { return "fake" }
func (p *loopProvider) Model() string                { return "fake" }
func (p *loopProvider) SetDebugger(_ llms.Debugger)  {}
func (p *loopProvider) SetHTTPClient(_ *http.Client) {}
func (p *loopProvider) Generate(_ context.Context, _ content.Content, _ []llms.Message, _ *llmtools.Toolbox, _ *llmtools.ValueSchema) llms.ProviderStream {
	return &loopStream{}
}

func (s *loopStream) Err() error { return nil }
func (s *loopStream) Message() llms.Message {
	return llms.Message{Role: "assistant", Content: content.FromText("loop")}
}
func (s *loopStream) Text() string             { return "loop" }
func (s *loopStream) Image() (string, string)  { return "", "" }
func (s *loopStream) Thought() content.Thought { return content.Thought{} }
func (s *loopStream) ToolCall() llms.ToolCall  { return llms.ToolCall{} }
func (s *loopStream) Usage() llms.Usage        { return llms.Usage{} }
func (s *loopStream) Iter() func(func(llms.StreamStatus) bool) {
	return func(yield func(llms.StreamStatus) bool) {
		yield(llms.StreamStatusText)
	}
}

func TestRuntimeRunLoop(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	client := &ai.Client{LLM: llms.New(&loopProvider{})}
	rt := NewRuntime(bus, mgr, client)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = rt.Run(ctx, "operator")
	}()

	_, _ = bus.Push(context.Background(), eventbus.EventInput{
		Stream:    "messages",
		ScopeType: "agent",
		ScopeID:   "operator",
		Body:      "hello",
		Metadata: map[string]any{
			"source": "human",
			"target": "operator",
		},
	})

	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for session")
		default:
			if session, ok := rt.GetSession("operator"); ok && session.LastOutput == "loop" {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}
