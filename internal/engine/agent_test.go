package engine

import (
	"context"
	"encoding/json"
	"errors"
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

type fakeProvider struct{}

type fakeStream struct{}

func (p *fakeProvider) Company() string              { return "fake" }
func (p *fakeProvider) Model() string                { return "fake" }
func (p *fakeProvider) SetDebugger(_ llms.Debugger)  {}
func (p *fakeProvider) SetHTTPClient(_ *http.Client) {}
func (p *fakeProvider) Generate(_ context.Context, _ content.Content, _ []llms.Message, _ *llmtools.Toolbox, _ *llmtools.ValueSchema) llms.ProviderStream {
	return &fakeStream{}
}

func (s *fakeStream) Err() error { return nil }
func (s *fakeStream) Message() llms.Message {
	return llms.Message{Role: "assistant", Content: content.FromText("ok")}
}
func (s *fakeStream) Text() string             { return "ok" }
func (s *fakeStream) Image() (string, string)  { return "", "" }
func (s *fakeStream) Thought() content.Thought { return content.Thought{} }
func (s *fakeStream) ToolCall() llms.ToolCall  { return llms.ToolCall{} }
func (s *fakeStream) Usage() llms.Usage        { return llms.Usage{} }
func (s *fakeStream) Iter() func(func(llms.StreamStatus) bool) {
	return func(yield func(llms.StreamStatus) bool) {
		yield(llms.StreamStatusText)
	}
}

type blockingProvider struct {
	started chan struct{}
}

func (p *blockingProvider) Company() string              { return "blocking" }
func (p *blockingProvider) Model() string                { return "blocking" }
func (p *blockingProvider) SetDebugger(_ llms.Debugger)  {}
func (p *blockingProvider) SetHTTPClient(_ *http.Client) {}
func (p *blockingProvider) Generate(ctx context.Context, _ content.Content, _ []llms.Message, _ *llmtools.Toolbox, _ *llmtools.ValueSchema) llms.ProviderStream {
	return &blockingStream{ctx: ctx, started: p.started}
}

type blockingStream struct {
	ctx     context.Context
	started chan struct{}
}

func (s *blockingStream) Err() error { return nil }
func (s *blockingStream) Message() llms.Message {
	return llms.Message{Role: "assistant", Content: content.FromText("")}
}
func (s *blockingStream) Text() string             { return "" }
func (s *blockingStream) Image() (string, string)  { return "", "" }
func (s *blockingStream) Thought() content.Thought { return content.Thought{} }
func (s *blockingStream) ToolCall() llms.ToolCall  { return llms.ToolCall{} }
func (s *blockingStream) Usage() llms.Usage        { return llms.Usage{} }
func (s *blockingStream) Iter() func(func(llms.StreamStatus) bool) {
	return func(yield func(llms.StreamStatus) bool) {
		select {
		case <-s.started:
		default:
			close(s.started)
		}
		<-s.ctx.Done()
	}
}

func TestRuntimeRunOnceStoresSession(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	client := &ai.Client{LLM: llms.New(&fakeProvider{})}
	rt := NewRuntime(bus, mgr, client)

	session, err := rt.RunOnce(context.Background(), "operator", "hello")
	if err != nil {
		t.Fatalf("run once: %v", err)
	}
	if session.LastOutput != "ok" {
		t.Fatalf("expected output")
	}

	stored, ok := rt.GetSession("operator")
	if !ok {
		t.Fatalf("expected session")
	}
	if stored.Prompt == "" {
		t.Fatalf("expected prompt")
	}

	if _, err := json.Marshal(stored); err != nil {
		t.Fatalf("session not json serializable: %v", err)
	}
}

func TestRuntimeInterruptCancelsLLM(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	started := make(chan struct{})
	client := &ai.Client{LLM: llms.New(&blockingProvider{started: started})}
	rt := NewRuntime(bus, mgr, client)

	ctx := context.Background()
	done := make(chan error, 1)
	var session Session
	go func() {
		var err error
		session, err = rt.RunOnce(ctx, "operator", "interrupt me")
		done <- err
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatalf("LLM did not start")
	}

	if _, err := bus.Push(ctx, eventbus.EventInput{
		Stream:  "signals",
		Subject: "interrupt",
		Body:    "interrupt",
		Metadata: map[string]any{
			"priority": "interrupt",
		},
	}); err != nil {
		t.Fatalf("push interrupt: %v", err)
	}

	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled error, got %v", err)
	}
	if session.LastError == "" {
		t.Fatalf("expected session error")
	}

	if session.RootTaskID == "" || session.LLMTaskID == "" {
		t.Fatalf("expected task ids")
	}
	root, err := mgr.Get(ctx, session.RootTaskID)
	if err != nil {
		t.Fatalf("get root task: %v", err)
	}
	if root.Status != tasks.StatusRunning {
		t.Fatalf("expected root running, got %s", root.Status)
	}
	llmTask, err := mgr.Get(ctx, session.LLMTaskID)
	if err != nil {
		t.Fatalf("get llm task: %v", err)
	}
	if llmTask.Status != tasks.StatusCancelled {
		t.Fatalf("expected llm cancelled, got %s", llmTask.Status)
	}
}
