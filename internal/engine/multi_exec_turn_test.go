package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/flitsinc/go-agents/internal/agenttools"
	"github.com/flitsinc/go-agents/internal/ai"
	"github.com/flitsinc/go-agents/internal/eventbus"
	"github.com/flitsinc/go-agents/internal/tasks"
	"github.com/flitsinc/go-agents/internal/testutil"
	"github.com/flitsinc/go-llms/content"
	"github.com/flitsinc/go-llms/llms"
	llmtools "github.com/flitsinc/go-llms/tools"
)

type multiExecTurnProvider struct {
	mu             sync.Mutex
	toolCalls      []llms.ToolCall
	finalText      string
	callCount      int
	secondCallSeen chan time.Time
}

func newMultiExecTurnProvider(toolCalls []llms.ToolCall, finalText string) *multiExecTurnProvider {
	return &multiExecTurnProvider{
		toolCalls:      append([]llms.ToolCall{}, toolCalls...),
		finalText:      finalText,
		secondCallSeen: make(chan time.Time, 1),
	}
}

func (p *multiExecTurnProvider) Company() string              { return "test" }
func (p *multiExecTurnProvider) Model() string                { return "test" }
func (p *multiExecTurnProvider) SetDebugger(_ llms.Debugger)  {}
func (p *multiExecTurnProvider) SetHTTPClient(_ *http.Client) {}
func (p *multiExecTurnProvider) Generate(_ context.Context, _ content.Content, _ []llms.Message, _ *llmtools.Toolbox, _ *llmtools.ValueSchema) llms.ProviderStream {
	p.mu.Lock()
	p.callCount++
	call := p.callCount
	p.mu.Unlock()

	if call == 1 {
		return newToolCallsOnlyStream(p.toolCalls)
	}
	if call == 2 {
		select {
		case p.secondCallSeen <- time.Now().UTC():
		default:
		}
	}
	return newTextOnlyStream(p.finalText)
}

func (p *multiExecTurnProvider) WaitForSecondCall(timeout time.Duration) (time.Time, bool) {
	select {
	case ts := <-p.secondCallSeen:
		return ts, true
	case <-time.After(timeout):
		return time.Time{}, false
	}
}

func (p *multiExecTurnProvider) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.callCount
}

type toolCallsOnlyStream struct {
	toolCalls []llms.ToolCall
	current   int
}

func newToolCallsOnlyStream(toolCalls []llms.ToolCall) *toolCallsOnlyStream {
	return &toolCallsOnlyStream{
		toolCalls: append([]llms.ToolCall{}, toolCalls...),
		current:   -1,
	}
}

func (s *toolCallsOnlyStream) Err() error { return nil }
func (s *toolCallsOnlyStream) Message() llms.Message {
	return llms.Message{
		Role:      "assistant",
		Content:   content.FromText(""),
		ToolCalls: append([]llms.ToolCall{}, s.toolCalls...),
	}
}
func (s *toolCallsOnlyStream) Text() string             { return "" }
func (s *toolCallsOnlyStream) Image() (string, string)  { return "", "" }
func (s *toolCallsOnlyStream) Thought() content.Thought { return content.Thought{} }
func (s *toolCallsOnlyStream) ToolCall() llms.ToolCall {
	if s.current < 0 || s.current >= len(s.toolCalls) {
		return llms.ToolCall{}
	}
	return s.toolCalls[s.current]
}
func (s *toolCallsOnlyStream) Usage() llms.Usage { return llms.Usage{} }
func (s *toolCallsOnlyStream) Iter() func(func(llms.StreamStatus) bool) {
	return func(yield func(llms.StreamStatus) bool) {
		for i := range s.toolCalls {
			s.current = i
			if !yield(llms.StreamStatusToolCallBegin) {
				return
			}
			if !yield(llms.StreamStatusToolCallReady) {
				return
			}
		}
	}
}

type textOnlyStream struct {
	text string
}

func newTextOnlyStream(text string) *textOnlyStream {
	return &textOnlyStream{text: text}
}

func (s *textOnlyStream) Err() error { return nil }
func (s *textOnlyStream) Message() llms.Message {
	return llms.Message{Role: "assistant", Content: content.FromText(s.text)}
}
func (s *textOnlyStream) Text() string             { return s.text }
func (s *textOnlyStream) Image() (string, string)  { return "", "" }
func (s *textOnlyStream) Thought() content.Thought { return content.Thought{} }
func (s *textOnlyStream) ToolCall() llms.ToolCall  { return llms.ToolCall{} }
func (s *textOnlyStream) Usage() llms.Usage        { return llms.Usage{} }
func (s *textOnlyStream) Iter() func(func(llms.StreamStatus) bool) {
	return func(yield func(llms.StreamStatus) bool) {
		_ = yield(llms.StreamStatusText)
	}
}

func makeExecToolCall(t *testing.T, id string, waitSeconds int, code string) llms.ToolCall {
	t.Helper()
	args, err := json.Marshal(map[string]any{
		"code":         code,
		"wait_seconds": waitSeconds,
	})
	if err != nil {
		t.Fatalf("marshal exec args: %v", err)
	}
	return llms.ToolCall{
		ID:        id,
		Name:      "exec",
		Arguments: args,
	}
}

func waitForExecTasks(t *testing.T, mgr *tasks.Manager, count int, timeout time.Duration) []tasks.Task {
	t.Helper()
	deadline := time.After(timeout)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			items, _ := mgr.List(context.Background(), tasks.ListFilter{Type: "exec", Owner: "operator", Limit: 20})
			t.Fatalf("timed out waiting for %d exec tasks, got %d", count, len(items))
		case <-ticker.C:
			items, err := mgr.List(context.Background(), tasks.ListFilter{Type: "exec", Owner: "operator", Limit: 20})
			if err != nil {
				t.Fatalf("list exec tasks: %v", err)
			}
			if len(items) < count {
				continue
			}
			sort.Slice(items, func(i, j int) bool {
				if items[i].CreatedAt.Equal(items[j].CreatedAt) {
					return items[i].ID < items[j].ID
				}
				return items[i].CreatedAt.Before(items[j].CreatedAt)
			})
			return items
		}
	}
}

func hasTaskUpdateKind(t *testing.T, mgr *tasks.Manager, taskID, kind string) bool {
	t.Helper()
	updates, err := mgr.ListUpdates(context.Background(), taskID, 200)
	if err != nil {
		t.Fatalf("list updates for task %s: %v", taskID, err)
	}
	for _, upd := range updates {
		if upd.Kind == kind {
			return true
		}
	}
	return false
}

func taskUpdateKinds(t *testing.T, mgr *tasks.Manager, taskID string) []string {
	t.Helper()
	updates, err := mgr.ListUpdates(context.Background(), taskID, 200)
	if err != nil {
		t.Fatalf("list updates for task %s: %v", taskID, err)
	}
	kinds := make([]string, 0, len(updates))
	for _, upd := range updates {
		kinds = append(kinds, upd.Kind)
	}
	return kinds
}

func TestRunOnceMultiExecNextTurnOnAnyExecCompletion(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	provider := newMultiExecTurnProvider([]llms.ToolCall{
		makeExecToolCall(t, "exec-call-1", 0, "globalThis.result = { job: 'a' }"),
		makeExecToolCall(t, "exec-call-2", 20, "globalThis.result = { job: 'b' }"),
	}, "all done")
	client := &ai.Client{LLM: llms.New(provider, agenttools.ExecTool(mgr))}
	rt := NewRuntime(bus, mgr, client)
	createTestAgent(t, mgr, "operator")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := rt.RunOnce(ctx, "operator", "run two exec jobs")
		done <- err
	}()

	execTasks := waitForExecTasks(t, mgr, 2, 3*time.Second)
	triggerAt := time.Now().UTC()
	_ = mgr.MarkRunning(context.Background(), execTasks[0].ID)
	if err := mgr.Complete(context.Background(), execTasks[0].ID, map[string]any{"ok": true, "job": "a"}); err != nil {
		t.Fatalf("complete first exec task: %v", err)
	}

	secondCallAt, ok := provider.WaitForSecondCall(3 * time.Second)
	if !ok {
		t.Fatalf("timed out waiting for second model turn")
	}
	if delta := secondCallAt.Sub(triggerAt); delta > 2*time.Second {
		t.Fatalf("expected second turn quickly after first exec completion, delta=%s", delta)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run once: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for run to finish")
	}

	if provider.Calls() < 2 {
		t.Fatalf("expected at least two model calls, got %d", provider.Calls())
	}
}

func TestRunOnceMultiExecNextTurnOnExecTimeout(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	provider := newMultiExecTurnProvider([]llms.ToolCall{
		makeExecToolCall(t, "exec-call-1", 0, "globalThis.result = { job: 'a' }"),
		makeExecToolCall(t, "exec-call-2", 1, "globalThis.result = { job: 'b' }"),
	}, "timed out and continued")
	client := &ai.Client{LLM: llms.New(provider, agenttools.ExecTool(mgr))}
	rt := NewRuntime(bus, mgr, client)
	createTestAgent(t, mgr, "operator")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := rt.RunOnce(ctx, "operator", "run two exec jobs")
		done <- err
	}()

	execTasks := waitForExecTasks(t, mgr, 2, 3*time.Second)
	triggerAt := time.Now().UTC()
	secondCallAt, ok := provider.WaitForSecondCall(4 * time.Second)
	if !ok {
		t.Fatalf("timed out waiting for second model turn")
	}
	if delta := secondCallAt.Sub(triggerAt); delta < 900*time.Millisecond || delta > 3500*time.Millisecond {
		t.Fatalf("expected second turn soon after timeout window, delta=%s", delta)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run once: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for run to finish")
	}

	secondTask, err := mgr.Get(context.Background(), execTasks[1].ID)
	if err != nil {
		t.Fatalf("get second exec task: %v", err)
	}
	if secondTask.Status == tasks.StatusCompleted {
		t.Fatalf("expected second exec task to still be non-terminal after timeout-driven return, updates=%v",
			taskUpdateKinds(t, mgr, execTasks[1].ID),
		)
	}
}

func TestRunOnceMultiExecNextTurnOnExternalWakeWhileWaiting(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	provider := newMultiExecTurnProvider([]llms.ToolCall{
		makeExecToolCall(t, "exec-call-1", 0, "globalThis.result = { job: 'a' }"),
		makeExecToolCall(t, "exec-call-2", 30, "globalThis.result = { job: 'b' }"),
	}, "woken and continued")
	client := &ai.Client{LLM: llms.New(provider, agenttools.ExecTool(mgr))}
	rt := NewRuntime(bus, mgr, client)
	createTestAgent(t, mgr, "operator")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := rt.RunOnce(ctx, "operator", "run two exec jobs")
		done <- err
	}()

	execTasks := waitForExecTasks(t, mgr, 2, 3*time.Second)
	triggerAt := time.Now().UTC()
	if _, err := bus.Push(context.Background(), eventbus.EventInput{
		Stream:    "signals",
		ScopeType: "task",
		ScopeID:   "operator",
		Subject:   "test wake",
		Body:      "wake now",
		Metadata: map[string]any{
			"priority": "wake",
			"kind":     "test_wake",
		},
	}); err != nil {
		t.Fatalf("push wake signal: %v", err)
	}

	secondCallAt, ok := provider.WaitForSecondCall(3 * time.Second)
	if !ok {
		t.Fatalf("timed out waiting for second model turn")
	}
	if delta := secondCallAt.Sub(triggerAt); delta > 2*time.Second {
		t.Fatalf("expected second turn quickly after wake event, delta=%s", delta)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run once: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for run to finish")
	}

	if hasTaskUpdateKind(t, mgr, execTasks[1].ID, "await_timeout") {
		t.Fatalf("did not expect await_timeout on second exec task when wake event interrupted waiting")
	}
}
