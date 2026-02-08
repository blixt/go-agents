package engine

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
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

var errLoopFailure = errors.New("loop failure")

type failingLoopProvider struct{}

type failingLoopStream struct{}

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

func (p *failingLoopProvider) Company() string              { return "fake" }
func (p *failingLoopProvider) Model() string                { return "fake" }
func (p *failingLoopProvider) SetDebugger(_ llms.Debugger)  {}
func (p *failingLoopProvider) SetHTTPClient(_ *http.Client) {}
func (p *failingLoopProvider) Generate(_ context.Context, _ content.Content, _ []llms.Message, _ *llmtools.Toolbox, _ *llmtools.ValueSchema) llms.ProviderStream {
	return &failingLoopStream{}
}

func (s *failingLoopStream) Err() error { return errLoopFailure }
func (s *failingLoopStream) Message() llms.Message {
	return llms.Message{Role: "assistant", Content: content.FromText("")}
}
func (s *failingLoopStream) Text() string             { return "" }
func (s *failingLoopStream) Image() (string, string)  { return "", "" }
func (s *failingLoopStream) Thought() content.Thought { return content.Thought{} }
func (s *failingLoopStream) ToolCall() llms.ToolCall  { return llms.ToolCall{} }
func (s *failingLoopStream) Usage() llms.Usage        { return llms.Usage{} }
func (s *failingLoopStream) Iter() func(func(llms.StreamStatus) bool) {
	return func(func(llms.StreamStatus) bool) {}
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
		Stream:    "task_input",
		ScopeType: "task",
		ScopeID:   "operator",
		Body:      "hello",
		Metadata: map[string]any{
			"source": "external",
			"target": "operator",
			"kind":   "message",
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

func TestRuntimeRunLoopReplaysUnreadMessages(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	client := &ai.Client{LLM: llms.New(&loopProvider{})}
	rt := NewRuntime(bus, mgr, client)

	evt, err := bus.Push(context.Background(), eventbus.EventInput{
		Stream:    "task_input",
		ScopeType: "task",
		ScopeID:   "operator",
		Body:      "queued-before-loop",
		Metadata: map[string]any{
			"source": "external",
			"target": "operator",
			"kind":   "message",
		},
	})
	if err != nil {
		t.Fatalf("push message: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = rt.Run(ctx, "operator")
	}()

	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for replayed message to run")
		default:
			session, ok := rt.GetSession("operator")
			if ok && session.LastOutput == "loop" {
				inputs := llmInputsFromHistory(t, bus, "operator")
				if len(inputs) == 0 {
					t.Fatalf("expected at least one llm_input history entry")
				}
				foundMessage := false
				for _, input := range inputs {
					if strings.Contains(input, "queued-before-loop") {
						foundMessage = true
						break
					}
				}
				if !foundMessage {
					t.Fatalf("expected replayed message in llm_input history, inputs=%v", inputs)
				}
				summaries, err := bus.List(context.Background(), "task_input", eventbus.ListOptions{
					Reader: "operator",
					Limit:  10,
					Order:  "fifo",
				})
				if err != nil {
					t.Fatalf("list task_input: %v", err)
				}
				for _, summary := range summaries {
					if summary.ID == evt.ID && !summary.Read {
						t.Fatalf("expected replayed message to be acked")
					}
				}
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestRuntimeRunLoopDoesNotDuplicateBufferedMessages(t *testing.T) {
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

	_, err := bus.Push(context.Background(), eventbus.EventInput{
		Stream:    "task_input",
		ScopeType: "task",
		ScopeID:   "operator",
		Body:      "first",
		Metadata: map[string]any{
			"source": "external",
			"target": "operator",
			"kind":   "message",
		},
	})
	if err != nil {
		t.Fatalf("push first message: %v", err)
	}
	_, err = bus.Push(context.Background(), eventbus.EventInput{
		Stream:    "task_input",
		ScopeType: "task",
		ScopeID:   "operator",
		Body:      "second",
		Metadata: map[string]any{
			"source": "external",
			"target": "operator",
			"kind":   "message",
		},
	})
	if err != nil {
		t.Fatalf("push second message: %v", err)
	}

	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for buffered messages to be acked")
		default:
			summaries, err := bus.List(context.Background(), "task_input", eventbus.ListOptions{
				Reader: "operator",
				Limit:  10,
				Order:  "fifo",
			})
			if err != nil {
				t.Fatalf("list task_input: %v", err)
			}
			readCount := 0
			for _, summary := range summaries {
				if summary.Read {
					readCount++
				}
			}
			if readCount < 2 {
				time.Sleep(10 * time.Millisecond)
				continue
			}

			items, err := mgr.List(context.Background(), tasks.ListFilter{
				Type:  "llm",
				Owner: "operator",
				Limit: 10,
			})
			if err != nil {
				t.Fatalf("list llm tasks: %v", err)
			}
			if len(items) == 0 {
				t.Fatalf("expected at least one llm task after buffered messages")
			}
			if len(items) > 2 {
				t.Fatalf("expected no duplicate llm tasks for 2 buffered messages, got %d", len(items))
			}
			return
		}
	}
}

func TestRuntimeRunLoopKeepsFailedMessageUnread(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	client := &ai.Client{LLM: llms.New(&failingLoopProvider{})}
	rt := NewRuntime(bus, mgr, client)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = rt.Run(ctx, "operator")
	}()

	evt, err := bus.Push(context.Background(), eventbus.EventInput{
		Stream:    "task_input",
		ScopeType: "task",
		ScopeID:   "operator",
		Body:      "fail please",
		Metadata: map[string]any{
			"source": "external",
			"target": "operator",
			"kind":   "message",
		},
	})
	if err != nil {
		t.Fatalf("push message: %v", err)
	}

	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for failed processing")
		default:
			items, err := mgr.List(context.Background(), tasks.ListFilter{
				Type:  "llm",
				Owner: "operator",
				Limit: 20,
			})
			if err != nil {
				t.Fatalf("list llm tasks: %v", err)
			}
			failed := false
			for _, item := range items {
				if item.Status == tasks.StatusFailed {
					failed = true
					break
				}
			}
			if !failed {
				time.Sleep(10 * time.Millisecond)
				continue
			}

			summaries, err := bus.List(context.Background(), "task_input", eventbus.ListOptions{
				Reader: "operator",
				Limit:  10,
				Order:  "fifo",
			})
			if err != nil {
				t.Fatalf("list task_input: %v", err)
			}
			for _, summary := range summaries {
				if summary.ID == evt.ID {
					if summary.Read {
						t.Fatalf("expected failed message to remain unread")
					}
					return
				}
			}
			t.Fatalf("failed message event not found")
		}
	}
}

func TestRuntimeRunLoopPrioritizesWakeOverLow(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	client := &ai.Client{LLM: llms.New(&loopProvider{})}
	rt := NewRuntime(bus, mgr, client)

	_, err := bus.Push(context.Background(), eventbus.EventInput{
		Stream:    "task_input",
		ScopeType: "task",
		ScopeID:   "operator",
		Body:      "low-priority-message",
		Metadata: map[string]any{
			"source":   "external",
			"target":   "operator",
			"priority": "low",
			"kind":     "message",
		},
	})
	if err != nil {
		t.Fatalf("push low message: %v", err)
	}
	_, err = bus.Push(context.Background(), eventbus.EventInput{
		Stream:    "task_input",
		ScopeType: "task",
		ScopeID:   "operator",
		Body:      "wake-priority-message",
		Metadata: map[string]any{
			"source":   "external",
			"target":   "operator",
			"priority": "wake",
			"kind":     "message",
		},
	})
	if err != nil {
		t.Fatalf("push wake message: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = rt.Run(ctx, "operator")
	}()

	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			inputs := llmInputsFromHistory(t, bus, "operator")
			t.Fatalf("did not observe wake+low ordering before timeout, llm_inputs=%v", inputs)
		default:
			inputs := llmInputsFromHistory(t, bus, "operator")
			wakeInputIdx := -1
			lowInputIdx := -1
			for i, input := range inputs {
				if wakeInputIdx < 0 && strings.Contains(input, "wake-priority-message") {
					wakeInputIdx = i
				}
				if lowInputIdx < 0 && strings.Contains(input, "low-priority-message") {
					lowInputIdx = i
				}
			}
			if wakeInputIdx >= 0 && lowInputIdx >= 0 {
				if wakeInputIdx > lowInputIdx {
					t.Fatalf("expected wake message before low message, llm_inputs=%v", inputs)
				}
				if wakeInputIdx == lowInputIdx {
					input := inputs[wakeInputIdx]
					wakeOffset := strings.Index(input, "wake-priority-message")
					lowOffset := strings.Index(input, "low-priority-message")
					if wakeOffset < 0 || lowOffset < 0 || wakeOffset > lowOffset {
						t.Fatalf("expected wake message before low message within llm_input, input=%q", input)
					}
				}
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func llmInputsFromHistory(t *testing.T, bus *eventbus.Bus, agentID string) []string {
	t.Helper()
	summaries, err := bus.List(context.Background(), "history", eventbus.ListOptions{
		ScopeType: "task",
		ScopeID:   agentID,
		Limit:     200,
		Order:     "fifo",
	})
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	if len(summaries) == 0 {
		return nil
	}
	ids := make([]string, 0, len(summaries))
	for _, summary := range summaries {
		ids = append(ids, summary.ID)
	}
	events, err := bus.Read(context.Background(), "history", ids, "")
	if err != nil {
		t.Fatalf("read history: %v", err)
	}
	sort.Slice(events, func(i, j int) bool {
		if events[i].CreatedAt.Equal(events[j].CreatedAt) {
			return events[i].ID < events[j].ID
		}
		return events[i].CreatedAt.Before(events[j].CreatedAt)
	})
	inputs := make([]string, 0, len(events))
	for _, evt := range events {
		entry, ok := HistoryEntryFromEvent(evt)
		if !ok || entry.Type != "llm_input" {
			continue
		}
		inputs = append(inputs, entry.Content)
	}
	return inputs
}

func TestRuntimeRunLoopWakesOnTaskOutputEvents(t *testing.T) {
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

	task, err := mgr.Spawn(context.Background(), tasks.Spec{
		Type:  "exec",
		Owner: "llm",
		Metadata: map[string]any{
			"notify_target": "operator",
		},
	})
	if err != nil {
		t.Fatalf("spawn task: %v", err)
	}
	if err := mgr.MarkRunning(context.Background(), task.ID); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	if err := mgr.Complete(context.Background(), task.ID, map[string]any{"ok": true}); err != nil {
		t.Fatalf("complete task: %v", err)
	}

	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for wake-driven run")
		default:
			session, ok := rt.GetSession("operator")
			if ok && session.LastOutput == "loop" {
				list, err := bus.List(context.Background(), "task_output", eventbus.ListOptions{
					Reader: "operator",
					Limit:  20,
					Order:  "fifo",
				})
				if err != nil {
					t.Fatalf("list task_output: %v", err)
				}
				for _, summary := range list {
					if summary.Read {
						return
					}
				}
				t.Fatalf("expected at least one task_output event to be acked")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}
