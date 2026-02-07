package engine

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
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

type captureProvider struct {
	mu        sync.Mutex
	lastInput string
}

type captureStream struct{}

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

func (p *captureProvider) Company() string              { return "capture" }
func (p *captureProvider) Model() string                { return "capture" }
func (p *captureProvider) SetDebugger(_ llms.Debugger)  {}
func (p *captureProvider) SetHTTPClient(_ *http.Client) {}
func (p *captureProvider) Generate(_ context.Context, _ content.Content, messages []llms.Message, _ *llmtools.Toolbox, _ *llmtools.ValueSchema) llms.ProviderStream {
	last := ""
	if len(messages) > 0 {
		last = messageText(messages[len(messages)-1])
	}
	p.mu.Lock()
	p.lastInput = last
	p.mu.Unlock()
	return &captureStream{}
}

func (p *captureProvider) LastInput() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastInput
}

func (s *captureStream) Err() error { return nil }
func (s *captureStream) Message() llms.Message {
	return llms.Message{Role: "assistant", Content: content.FromText("ok")}
}
func (s *captureStream) Text() string             { return "ok" }
func (s *captureStream) Image() (string, string)  { return "", "" }
func (s *captureStream) Thought() content.Thought { return content.Thought{} }
func (s *captureStream) ToolCall() llms.ToolCall  { return llms.ToolCall{} }
func (s *captureStream) Usage() llms.Usage        { return llms.Usage{} }
func (s *captureStream) Iter() func(func(llms.StreamStatus) bool) {
	return func(yield func(llms.StreamStatus) bool) {
		yield(llms.StreamStatusText)
	}
}

func messageText(msg llms.Message) string {
	var parts []string
	for _, item := range msg.Content {
		if txt, ok := item.(*content.Text); ok {
			parts = append(parts, txt.Text)
		}
	}
	return strings.Join(parts, "")
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

func TestRuntimeHistoryReconstructionExcludesLowPriority(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	cp := &captureProvider{}
	client := &ai.Client{LLM: llms.New(cp)}
	rt := NewRuntime(bus, mgr, client)

	ctx := context.Background()
	seedTask := func(priority, input, output string) {
		task, err := mgr.Spawn(ctx, tasks.Spec{
			Type:  "llm",
			Owner: "agent-a",
			Mode:  "sync",
			Metadata: map[string]any{
				"agent_id":      "agent-a",
				"input_target":  "agent-a",
				"notify_target": "agent-a",
				"source":        "external",
				"priority":      priority,
			},
		})
		if err != nil {
			t.Fatalf("spawn seed task: %v", err)
		}
		if err := mgr.MarkRunning(ctx, task.ID); err != nil {
			t.Fatalf("mark running seed task: %v", err)
		}
		if err := mgr.RecordUpdate(ctx, task.ID, "input", map[string]any{"message": input}); err != nil {
			t.Fatalf("record input seed task: %v", err)
		}
		if err := mgr.Complete(ctx, task.ID, map[string]any{"output": output}); err != nil {
			t.Fatalf("complete seed task: %v", err)
		}
	}

	seedTask("low", "low-input", "low-output")
	seedTask("wake", "wake-input", "wake-output")

	if _, err := rt.RunOnce(ctx, "agent-a", "current-message"); err != nil {
		t.Fatalf("run once: %v", err)
	}

	last := cp.LastInput()
	if !strings.Contains(last, "wake-input") || !strings.Contains(last, "wake-output") {
		t.Fatalf("expected wake-priority history in prompt input, got: %q", last)
	}
	if strings.Contains(last, "low-input") || strings.Contains(last, "low-output") {
		t.Fatalf("did not expect low-priority history in prompt input, got: %q", last)
	}
}

func TestRuntimeInjectsAndAcksContextUpdates(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	cp := &captureProvider{}
	client := &ai.Client{LLM: llms.New(cp)}
	rt := NewRuntime(bus, mgr, client)

	ctx := context.Background()
	evt, err := bus.Push(ctx, eventbus.EventInput{
		Stream:    "signals",
		ScopeType: "agent",
		ScopeID:   "agent-a",
		Subject:   "task_done",
		Body:      "task completed",
		Metadata: map[string]any{
			"priority": "wake",
			"task_id":  "task-123",
		},
		Payload: map[string]any{
			"result": "ok",
		},
	})
	if err != nil {
		t.Fatalf("push context event: %v", err)
	}

	if _, err := rt.RunOnce(ctx, "agent-a", "check status"); err != nil {
		t.Fatalf("run once: %v", err)
	}

	last := cp.LastInput()
	if !strings.Contains(last, "<context_updates") {
		t.Fatalf("expected context_updates xml in input, got: %q", last)
	}
	if !strings.Contains(last, "task_done") || !strings.Contains(last, "task completed") {
		t.Fatalf("expected event details in context_updates xml, got: %q", last)
	}

	historySummaries, err := bus.List(ctx, "history", eventbus.ListOptions{
		ScopeType: "agent",
		ScopeID:   "agent-a",
		Limit:     50,
		Order:     "lifo",
	})
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	historyIDs := make([]string, 0, len(historySummaries))
	for _, summary := range historySummaries {
		historyIDs = append(historyIDs, summary.ID)
	}
	historyEvents, err := bus.Read(ctx, "history", historyIDs, "")
	if err != nil {
		t.Fatalf("read history: %v", err)
	}
	foundContextEvent := false
	for _, evt := range historyEvents {
		entry, ok := HistoryEntryFromEvent(evt)
		if !ok {
			continue
		}
		if entry.Type == "context_event" {
			foundContextEvent = true
			break
		}
	}
	if !foundContextEvent {
		t.Fatalf("expected context_event entry in history")
	}

	summaries, err := bus.List(ctx, "signals", eventbus.ListOptions{
		Reader: "agent-a",
		Limit:  20,
		Order:  "fifo",
	})
	if err != nil {
		t.Fatalf("list signals: %v", err)
	}
	for _, summary := range summaries {
		if summary.ID == evt.ID {
			if !summary.Read {
				t.Fatalf("expected context update event to be acked")
			}
			return
		}
	}
	t.Fatalf("expected context update event to be listed")
}

func TestRuntimeInjectsTimeAndDateSystemUpdates(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	cp := &captureProvider{}
	client := &ai.Client{LLM: llms.New(cp)}
	rt := NewRuntime(bus, mgr, client)

	now := time.Now().UTC()
	rt.turnMu.Lock()
	rt.lastTurnStart["agent-a"] = now.Add(-25 * time.Hour)
	rt.turnMu.Unlock()

	if _, err := rt.RunOnce(context.Background(), "agent-a", "status"); err != nil {
		t.Fatalf("run once: %v", err)
	}

	last := cp.LastInput()
	if !strings.Contains(last, "kind=\"time_passed\"") {
		t.Fatalf("expected time_passed system update, got: %q", last)
	}
	if !strings.Contains(last, "kind=\"date_changed\"") {
		t.Fatalf("expected date_changed system update, got: %q", last)
	}
	if strings.Contains(last, "time_elapsed") {
		t.Fatalf("did not expect legacy time_elapsed event in prompt input: %q", last)
	}
}

func TestRuntimeSkipsLLMDebugSignalsInPromptContext(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	cp := &captureProvider{}
	client := &ai.Client{LLM: llms.New(cp)}
	rt := NewRuntime(bus, mgr, client)

	evt, err := bus.Push(context.Background(), eventbus.EventInput{
		Stream:    "signals",
		ScopeType: "agent",
		ScopeID:   "agent-a",
		Subject:   "llm_debug_event",
		Body:      "llm_debug_event",
		Metadata: map[string]any{
			"kind":     "llm_debug",
			"priority": "low",
		},
		Payload: map[string]any{
			"data": "debug payload",
		},
	})
	if err != nil {
		t.Fatalf("push debug signal: %v", err)
	}

	if _, err := rt.RunOnce(context.Background(), "agent-a", "check"); err != nil {
		t.Fatalf("run once: %v", err)
	}

	last := cp.LastInput()
	if strings.Contains(last, "llm_debug_event") || strings.Contains(last, "debug payload") {
		t.Fatalf("did not expect llm_debug signal in prompt context: %q", last)
	}

	summaries, err := bus.List(context.Background(), "signals", eventbus.ListOptions{
		Reader: "agent-a",
		Limit:  50,
		Order:  "fifo",
	})
	if err != nil {
		t.Fatalf("list signals: %v", err)
	}
	for _, summary := range summaries {
		if summary.ID != evt.ID {
			continue
		}
		if !summary.Read {
			t.Fatalf("expected llm_debug signal to be acked")
		}
		return
	}
	t.Fatalf("expected llm_debug signal summary")
}

func TestRuntimeAppendsAgentHistoryEntries(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	client := &ai.Client{LLM: llms.New(&fakeProvider{})}
	rt := NewRuntime(bus, mgr, client)

	if _, err := rt.RunOnce(context.Background(), "agent-a", "hello history"); err != nil {
		t.Fatalf("run once: %v", err)
	}

	summaries, err := bus.List(context.Background(), "history", eventbus.ListOptions{
		ScopeType: "agent",
		ScopeID:   "agent-a",
		Limit:     100,
		Order:     "lifo",
	})
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	if len(summaries) == 0 {
		t.Fatalf("expected history entries")
	}
	ids := make([]string, 0, len(summaries))
	for _, summary := range summaries {
		ids = append(ids, summary.ID)
	}
	events, err := bus.Read(context.Background(), "history", ids, "")
	if err != nil {
		t.Fatalf("read history: %v", err)
	}
	types := map[string]struct{}{}
	for _, evt := range events {
		entry, ok := HistoryEntryFromEvent(evt)
		if !ok {
			continue
		}
		types[entry.Type] = struct{}{}
	}
	for _, required := range []string{"tools_config", "system_prompt", "user_message", "assistant_message"} {
		if _, ok := types[required]; !ok {
			t.Fatalf("missing required history entry type %q", required)
		}
	}
}

func TestRuntimeWritesSystemPromptAndToolsConfigOncePerGeneration(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	client := &ai.Client{LLM: llms.New(&fakeProvider{})}
	rt := NewRuntime(bus, mgr, client)

	if _, err := rt.RunOnce(context.Background(), "agent-a", "hello"); err != nil {
		t.Fatalf("first run once: %v", err)
	}
	if _, err := rt.RunOnce(context.Background(), "agent-a", "hello again"); err != nil {
		t.Fatalf("second run once: %v", err)
	}

	summaries, err := bus.List(context.Background(), "history", eventbus.ListOptions{
		ScopeType: "agent",
		ScopeID:   "agent-a",
		Limit:     200,
		Order:     "lifo",
	})
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	ids := make([]string, 0, len(summaries))
	for _, summary := range summaries {
		ids = append(ids, summary.ID)
	}
	events, err := bus.Read(context.Background(), "history", ids, "")
	if err != nil {
		t.Fatalf("read history: %v", err)
	}

	currentGeneration := int64(1)
	for _, evt := range events {
		entry, ok := HistoryEntryFromEvent(evt)
		if !ok {
			continue
		}
		if entry.Generation > currentGeneration {
			currentGeneration = entry.Generation
		}
	}
	toolsConfigCount := 0
	systemPromptCount := 0
	for _, evt := range events {
		entry, ok := HistoryEntryFromEvent(evt)
		if !ok || entry.Generation != currentGeneration {
			continue
		}
		if entry.Type == "tools_config" {
			toolsConfigCount++
		}
		if entry.Type == "system_prompt" {
			systemPromptCount++
		}
	}
	if toolsConfigCount != 1 {
		t.Fatalf("expected 1 tools_config entry in generation %d, got %d", currentGeneration, toolsConfigCount)
	}
	if systemPromptCount != 1 {
		t.Fatalf("expected 1 system_prompt entry in generation %d, got %d", currentGeneration, systemPromptCount)
	}
}
