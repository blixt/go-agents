package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/flitsinc/go-llms/content"
	"github.com/flitsinc/go-llms/llms"
	llmtools "github.com/flitsinc/go-llms/tools"
)

type fakeProvider struct {
	calls int
}

type fakeStream struct {
	statuses []llms.StreamStatus
	toolCall llms.ToolCall
	message  llms.Message
}

func (p *fakeProvider) Company() string              { return "fake" }
func (p *fakeProvider) Model() string                { return "fake" }
func (p *fakeProvider) SetDebugger(d llms.Debugger)  {}
func (p *fakeProvider) SetHTTPClient(_ *http.Client) {}

func (p *fakeProvider) Generate(_ context.Context, _ content.Content, _ []llms.Message, _ *llmtools.Toolbox, _ *llmtools.ValueSchema) llms.ProviderStream {
	p.calls++
	if p.calls == 1 {
		toolCall := llms.ToolCall{ID: "call-1", Name: "external_tool", Arguments: json.RawMessage(`{"value": 1}`)}
		return &fakeStream{
			statuses: []llms.StreamStatus{llms.StreamStatusToolCallBegin, llms.StreamStatusToolCallDelta, llms.StreamStatusToolCallReady},
			toolCall: toolCall,
			message:  llms.Message{Role: "assistant", ToolCalls: []llms.ToolCall{toolCall}},
		}
	}
	return &fakeStream{
		statuses: []llms.StreamStatus{llms.StreamStatusText},
		message:  llms.Message{Role: "assistant", Content: content.FromText("done")},
	}
}

func (s *fakeStream) Err() error               { return nil }
func (s *fakeStream) Message() llms.Message    { return s.message }
func (s *fakeStream) Text() string             { return "done" }
func (s *fakeStream) Image() (string, string)  { return "", "" }
func (s *fakeStream) Thought() content.Thought { return content.Thought{} }
func (s *fakeStream) ToolCall() llms.ToolCall  { return s.toolCall }
func (s *fakeStream) Usage() llms.Usage        { return llms.Usage{} }
func (s *fakeStream) Iter() func(func(llms.StreamStatus) bool) {
	return func(yield func(llms.StreamStatus) bool) {
		for _, status := range s.statuses {
			if !yield(status) {
				return
			}
		}
	}
}

func TestAddExternalToolsDispatch(t *testing.T) {
	provider := &fakeProvider{}
	client := &Client{LLM: llms.New(provider)}

	schemas := []llmtools.FunctionSchema{{Name: "external_tool", Description: "test", Parameters: llmtools.ValueSchema{Type: "object"}}}
	called := false
	AddExternalTools(client, schemas, func(_ context.Context, name string, params json.RawMessage) (any, error) {
		called = true
		if name != "external_tool" {
			t.Fatalf("unexpected tool name")
		}
		return map[string]any{"ok": true}, nil
	})

	updates := client.LLM.Chat("run tool")
	for range updates {
	}
	if err := client.LLM.Err(); err != nil {
		t.Fatalf("chat error: %v", err)
	}
	if !called {
		t.Fatalf("expected external handler to be called")
	}
}
