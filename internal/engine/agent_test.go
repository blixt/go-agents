package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

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
