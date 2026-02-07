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
		Stream:    "messages",
		ScopeType: "agent",
		ScopeID:   "operator",
		Body:      "hello",
		Metadata: map[string]any{
			"source": "external",
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

func TestRuntimeRunLoopReplaysUnreadMessages(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	client := &ai.Client{LLM: llms.New(&loopProvider{})}
	rt := NewRuntime(bus, mgr, client)

	evt, err := bus.Push(context.Background(), eventbus.EventInput{
		Stream:    "messages",
		ScopeType: "agent",
		ScopeID:   "operator",
		Body:      "queued-before-loop",
		Metadata: map[string]any{
			"source": "external",
			"target": "operator",
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
			if ok && session.LastInput == "queued-before-loop" && session.LastOutput == "loop" {
				summaries, err := bus.List(context.Background(), "messages", eventbus.ListOptions{
					Reader: "operator",
					Limit:  10,
					Order:  "fifo",
				})
				if err != nil {
					t.Fatalf("list messages: %v", err)
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
		Stream:    "messages",
		ScopeType: "agent",
		ScopeID:   "operator",
		Body:      "first",
		Metadata: map[string]any{
			"source": "external",
			"target": "operator",
		},
	})
	if err != nil {
		t.Fatalf("push first message: %v", err)
	}
	_, err = bus.Push(context.Background(), eventbus.EventInput{
		Stream:    "messages",
		ScopeType: "agent",
		ScopeID:   "operator",
		Body:      "second",
		Metadata: map[string]any{
			"source": "external",
			"target": "operator",
		},
	})
	if err != nil {
		t.Fatalf("push second message: %v", err)
	}

	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for two llm tasks")
		default:
			items, err := mgr.List(context.Background(), tasks.ListFilter{
				Type:  "llm",
				Owner: "operator",
				Limit: 10,
			})
			if err != nil {
				t.Fatalf("list llm tasks: %v", err)
			}
			if len(items) >= 2 {
				time.Sleep(150 * time.Millisecond)
				items, err = mgr.List(context.Background(), tasks.ListFilter{
					Type:  "llm",
					Owner: "operator",
					Limit: 10,
				})
				if err != nil {
					t.Fatalf("list llm tasks: %v", err)
				}
				if len(items) != 2 {
					t.Fatalf("expected exactly 2 llm tasks, got %d", len(items))
				}
				return
			}
			time.Sleep(10 * time.Millisecond)
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
		Stream:    "messages",
		ScopeType: "agent",
		ScopeID:   "operator",
		Body:      "fail please",
		Metadata: map[string]any{
			"source": "external",
			"target": "operator",
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

			summaries, err := bus.List(context.Background(), "messages", eventbus.ListOptions{
				Reader: "operator",
				Limit:  10,
				Order:  "fifo",
			})
			if err != nil {
				t.Fatalf("list messages: %v", err)
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
		Stream:    "messages",
		ScopeType: "agent",
		ScopeID:   "operator",
		Body:      "low-priority-message",
		Metadata: map[string]any{
			"source":   "external",
			"target":   "operator",
			"priority": "low",
		},
	})
	if err != nil {
		t.Fatalf("push low message: %v", err)
	}
	_, err = bus.Push(context.Background(), eventbus.EventInput{
		Stream:    "messages",
		ScopeType: "agent",
		ScopeID:   "operator",
		Body:      "wake-priority-message",
		Metadata: map[string]any{
			"source":   "external",
			"target":   "operator",
			"priority": "wake",
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

	var llmItems []tasks.Task
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for llm tasks")
		default:
			items, listErr := mgr.List(context.Background(), tasks.ListFilter{
				Type:  "llm",
				Owner: "operator",
				Limit: 10,
			})
			if listErr != nil {
				t.Fatalf("list llm tasks: %v", listErr)
			}
			if len(items) >= 2 {
				llmItems = items
				goto ready
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

ready:
	sort.Slice(llmItems, func(i, j int) bool {
		if llmItems[i].CreatedAt.Equal(llmItems[j].CreatedAt) {
			return llmItems[i].ID < llmItems[j].ID
		}
		return llmItems[i].CreatedAt.Before(llmItems[j].CreatedAt)
	})

	collected := make([]string, 0, 2)
	for _, item := range llmItems {
		updates, listErr := mgr.ListUpdates(context.Background(), item.ID, 10)
		if listErr != nil {
			t.Fatalf("list task updates: %v", listErr)
		}
		for _, upd := range updates {
			if upd.Kind != "input" {
				continue
			}
			msg, _ := upd.Payload["message"].(string)
			msg = strings.TrimSpace(msg)
			if msg == "" {
				continue
			}
			collected = append(collected, msg)
			break
		}
		if len(collected) >= 2 {
			break
		}
	}
	if len(collected) < 2 {
		t.Fatalf("expected at least two non-empty llm inputs, got %v", collected)
	}
	firstInput := collected[0]
	secondInput := collected[1]
	if firstInput != "wake-priority-message" {
		t.Fatalf("expected wake message first, got %q", firstInput)
	}
	if secondInput != "low-priority-message" {
		t.Fatalf("expected low message second, got %q", secondInput)
	}
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
