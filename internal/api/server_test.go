package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/flitsinc/go-agents/internal/ai"
	"github.com/flitsinc/go-agents/internal/engine"
	"github.com/flitsinc/go-agents/internal/eventbus"
	"github.com/flitsinc/go-agents/internal/tasks"
	"github.com/flitsinc/go-agents/internal/testutil"
	"github.com/flitsinc/go-llms/content"
	"github.com/flitsinc/go-llms/llms"
	llmtools "github.com/flitsinc/go-llms/tools"
)

func TestServerStateAndQueue(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)

	runtimeClient := &ai.Client{LLM: llms.New(&apiFakeProvider{})}
	rt := engine.NewRuntime(bus, mgr, runtimeClient)

	server := &Server{Tasks: mgr, Bus: bus, Runtime: rt}
	h := server.Handler()
	client := testutil.NewInProcessClient(h)

	// Create a task directly.
	task, err := mgr.Spawn(context.Background(), tasks.Spec{
		Type:  "exec",
		Owner: "operator",
		Payload: map[string]any{
			"code": "globalThis.result = 1",
		},
	})
	if err != nil {
		t.Fatalf("spawn task: %v", err)
	}

	// Post update.
	updatePayload := map[string]any{"kind": "progress", "payload": map[string]any{"pct": 10}}
	resp := doJSON(t, client, "POST", "/api/tasks/"+task.ID+"/updates", updatePayload)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update status: %d body=%s", resp.StatusCode, readBody(t, resp))
	}

	// Complete task.
	resp = doJSON(t, client, "POST", "/api/tasks/"+task.ID+"/complete", map[string]any{"result": map[string]any{"ok": true}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("complete status: %d body=%s", resp.StatusCode, readBody(t, resp))
	}

	// Claim queue should be empty (task completed).
	resp = doJSON(t, client, "GET", "/api/tasks/queue?type=exec&limit=1", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("queue status: %d body=%s", resp.StatusCode, readBody(t, resp))
	}
	var queued []tasks.Task
	decodeJSONResponse(t, resp, &queued)
	if len(queued) != 0 {
		t.Fatalf("expected no queued tasks")
	}

	// Create agent task via POST /api/tasks.
	resp = doJSON(t, client, "POST", "/api/tasks", map[string]any{
		"type":    "agent",
		"payload": map[string]any{"message": "hello"},
		"source":  "external",
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("create task status: %d body=%s", resp.StatusCode, readBody(t, resp))
	}
	var run map[string]any
	decodeJSONResponse(t, resp, &run)
	taskID, _ := run["task_id"].(string)
	if taskID == "" {
		t.Fatalf("expected task_id, got %#v", run)
	}

	// State snapshot.
	resp = doJSON(t, client, "GET", "/api/state?tasks=10&updates=10&streams=10", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("state status: %d body=%s", resp.StatusCode, readBody(t, resp))
	}
	var snapshot map[string]any
	decodeJSONResponse(t, resp, &snapshot)
	if snapshot["tasks"] == nil {
		t.Fatalf("expected tasks in snapshot")
	}
	if snapshot["agents"] == nil {
		t.Fatalf("expected agents in snapshot")
	}
	if snapshot["histories"] == nil {
		t.Fatalf("expected histories in snapshot")
	}
}

func TestServerCreateTaskUpsert(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	runtimeClient := &ai.Client{LLM: llms.New(&apiFakeProvider{})}
	rt := engine.NewRuntime(bus, mgr, runtimeClient)
	server := &Server{Tasks: mgr, Bus: bus, Runtime: rt}
	client := testutil.NewInProcessClient(server.Handler())

	// First call creates the agent.
	resp := doJSON(t, client, "POST", "/api/tasks", map[string]any{
		"type": "agent",
		"id":   "planner",
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("create task status: %d body=%s", resp.StatusCode, readBody(t, resp))
	}
	var first map[string]any
	decodeJSONResponse(t, resp, &first)
	if first["task_id"] != "planner" {
		t.Fatalf("expected planner, got %#v", first["task_id"])
	}
	if first["created"] != true {
		t.Fatalf("expected created=true, got %#v", first["created"])
	}

	// Second call upserts — returns the existing agent.
	resp = doJSON(t, client, "POST", "/api/tasks", map[string]any{
		"type": "agent",
		"id":   "planner",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upsert task status: %d body=%s", resp.StatusCode, readBody(t, resp))
	}
	var second map[string]any
	decodeJSONResponse(t, resp, &second)
	if second["task_id"] != "planner" {
		t.Fatalf("expected planner, got %#v", second["task_id"])
	}
	if second["created"] != false {
		t.Fatalf("expected created=false, got %#v", second["created"])
	}
}

func TestServerTaskSendIncludesServiceIDInMessageMetadata(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	runtimeClient := &ai.Client{LLM: llms.New(&apiFakeProvider{})}
	rt := engine.NewRuntime(bus, mgr, runtimeClient)
	server := &Server{Tasks: mgr, Bus: bus, Runtime: rt}
	client := testutil.NewInProcessClient(server.Handler())

	_, err := mgr.Spawn(context.Background(), tasks.Spec{
		ID:    "operator",
		Type:  "agent",
		Owner: "operator",
		Mode:  "async",
		Metadata: map[string]any{
			"input_target":  "operator",
			"notify_target": "operator",
		},
	})
	if err != nil {
		t.Fatalf("spawn operator: %v", err)
	}
	_ = mgr.MarkRunning(context.Background(), "operator")

	resp := doJSON(t, client, "POST", "/api/tasks/operator/send", map[string]any{
		"message":    "hello from service",
		"source":     "telegram-bot",
		"service_id": "telegram-bot",
		"request_id": "req-123",
		"context": map[string]any{
			"chat_id":    "12345",
			"service_id": "telegram-bot",
		},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("send status: %d body=%s", resp.StatusCode, readBody(t, resp))
	}
	var sendResp map[string]any
	decodeJSONResponse(t, resp, &sendResp)
	if sendResp["request_id"] != "req-123" {
		t.Fatalf("expected request_id in response, got %#v", sendResp["request_id"])
	}
	if sendResp["service_id"] != "telegram-bot" {
		t.Fatalf("expected service_id in response, got %#v", sendResp["service_id"])
	}

	summaries, err := bus.List(context.Background(), "task_input", eventbus.ListOptions{
		ScopeType: "task",
		ScopeID:   "operator",
		Limit:     20,
		Order:     "lifo",
	})
	if err != nil {
		t.Fatalf("list task_input: %v", err)
	}
	if len(summaries) == 0 {
		t.Fatalf("expected task_input events")
	}
	ids := make([]string, 0, len(summaries))
	for _, summary := range summaries {
		ids = append(ids, summary.ID)
	}
	events, err := bus.Read(context.Background(), "task_input", ids, "")
	if err != nil {
		t.Fatalf("read task_input: %v", err)
	}
	found := false
	for _, evt := range events {
		if evt.Metadata == nil {
			continue
		}
		if evt.Metadata["request_id"] != "req-123" {
			continue
		}
		if evt.Metadata["service_id"] != "telegram-bot" {
			t.Fatalf("expected service_id metadata, got %#v", evt.Metadata["service_id"])
		}
		rawCtx, ok := evt.Metadata["context"].(map[string]any)
		if !ok {
			t.Fatalf("expected context metadata map, got %#v", evt.Metadata["context"])
		}
		if rawCtx["service_id"] != "telegram-bot" {
			t.Fatalf("expected context.service_id metadata, got %#v", rawCtx["service_id"])
		}
		found = true
		break
	}
	if !found {
		t.Fatalf("expected task_input event with request_id req-123")
	}
}

func TestServerTaskSendDoesNotInferServiceIDFromSource(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	runtimeClient := &ai.Client{LLM: llms.New(&apiFakeProvider{})}
	rt := engine.NewRuntime(bus, mgr, runtimeClient)
	server := &Server{Tasks: mgr, Bus: bus, Runtime: rt}
	client := testutil.NewInProcessClient(server.Handler())

	_, err := mgr.Spawn(context.Background(), tasks.Spec{
		ID:    "operator",
		Type:  "agent",
		Owner: "operator",
		Mode:  "async",
		Metadata: map[string]any{
			"input_target":  "operator",
			"notify_target": "operator",
		},
	})
	if err != nil {
		t.Fatalf("spawn operator: %v", err)
	}
	_ = mgr.MarkRunning(context.Background(), "operator")

	resp := doJSON(t, client, "POST", "/api/tasks/operator/send", map[string]any{
		"message":    "hello from service",
		"source":     "telegram-bot",
		"request_id": "req-no-service-id",
		"context": map[string]any{
			"chat_id": "12345",
		},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("send status: %d body=%s", resp.StatusCode, readBody(t, resp))
	}
	var sendResp map[string]any
	decodeJSONResponse(t, resp, &sendResp)
	if _, ok := sendResp["service_id"]; ok {
		t.Fatalf("did not expect service_id in response when omitted, got %#v", sendResp["service_id"])
	}

	summaries, err := bus.List(context.Background(), "task_input", eventbus.ListOptions{
		ScopeType: "task",
		ScopeID:   "operator",
		Limit:     20,
		Order:     "lifo",
	})
	if err != nil {
		t.Fatalf("list task_input: %v", err)
	}
	ids := make([]string, 0, len(summaries))
	for _, summary := range summaries {
		ids = append(ids, summary.ID)
	}
	events, err := bus.Read(context.Background(), "task_input", ids, "")
	if err != nil {
		t.Fatalf("read task_input: %v", err)
	}
	for _, evt := range events {
		if evt.Metadata == nil || evt.Metadata["request_id"] != "req-no-service-id" {
			continue
		}
		if _, ok := evt.Metadata["service_id"]; ok {
			t.Fatalf("did not expect inferred service_id from source, got %#v", evt.Metadata["service_id"])
		}
		return
	}
	t.Fatalf("expected task_input event with request_id req-no-service-id")
}

func TestServerTaskSendRejectsConflictingServiceID(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	runtimeClient := &ai.Client{LLM: llms.New(&apiFakeProvider{})}
	rt := engine.NewRuntime(bus, mgr, runtimeClient)
	server := &Server{Tasks: mgr, Bus: bus, Runtime: rt}
	client := testutil.NewInProcessClient(server.Handler())

	_, err := mgr.Spawn(context.Background(), tasks.Spec{
		ID:    "operator",
		Type:  "agent",
		Owner: "operator",
		Mode:  "async",
		Metadata: map[string]any{
			"input_target":  "operator",
			"notify_target": "operator",
		},
	})
	if err != nil {
		t.Fatalf("spawn operator: %v", err)
	}
	_ = mgr.MarkRunning(context.Background(), "operator")

	resp := doJSON(t, client, "POST", "/api/tasks/operator/send", map[string]any{
		"message":    "hello",
		"service_id": "service-a",
		"context": map[string]any{
			"service_id": "service-b",
		},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for conflicting service_id, got %d body=%s", resp.StatusCode, readBody(t, resp))
	}
	body := readBody(t, resp)
	if !strings.Contains(body, "service_id conflict") {
		t.Fatalf("expected service_id conflict error, got %q", body)
	}
}

func TestServerEmptySlicesEncodeAsJSONArray(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	runtimeClient := &ai.Client{LLM: llms.New(&apiFakeProvider{})}
	rt := engine.NewRuntime(bus, mgr, runtimeClient)
	server := &Server{Tasks: mgr, Bus: bus, Runtime: rt}
	client := testutil.NewInProcessClient(server.Handler())

	resp := doJSON(t, client, "GET", "/api/tasks/queue?type=exec&limit=10", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("queue status: %d body=%s", resp.StatusCode, readBody(t, resp))
	}
	if body := strings.TrimSpace(readBody(t, resp)); body != "[]" {
		t.Fatalf("expected empty queue to encode as [], got %q", body)
	}

	resp = doJSON(t, client, "GET", "/api/state?tasks=10&updates=10&streams=10", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("state status: %d body=%s", resp.StatusCode, readBody(t, resp))
	}
	body := readBody(t, resp)
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("decode state response: %v", err)
	}
	if got := strings.TrimSpace(string(payload["agents"])); got != "[]" {
		t.Fatalf("expected state.agents to be [], got %q", got)
	}
	if got := strings.TrimSpace(string(payload["tasks"])); got != "[]" {
		t.Fatalf("expected state.tasks to be [], got %q", got)
	}

	_, err := mgr.Spawn(context.Background(), tasks.Spec{
		ID:    "operator",
		Type:  "agent",
		Owner: "operator",
		Mode:  "async",
		Metadata: map[string]any{
			"input_target":  "operator",
			"notify_target": "operator",
		},
	})
	if err != nil {
		t.Fatalf("spawn operator: %v", err)
	}
	_ = mgr.MarkRunning(context.Background(), "operator")

	resp = doJSON(t, client, "GET", "/api/tasks/operator/updates?kind=does-not-exist", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("updates status: %d body=%s", resp.StatusCode, readBody(t, resp))
	}
	if body := strings.TrimSpace(readBody(t, resp)); body != "[]" {
		t.Fatalf("expected empty updates to encode as [], got %q", body)
	}
}

func TestServerAgentCompact(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	runtimeClient := &ai.Client{LLM: llms.New(&apiFakeProvider{})}
	rt := engine.NewRuntime(bus, mgr, runtimeClient)

	server := &Server{Tasks: mgr, Bus: bus, Runtime: rt}
	h := server.Handler()
	client := testutil.NewInProcessClient(h)

	resp := doJSON(t, client, "POST", "/api/tasks/operator/compact", map[string]any{
		"reason": "test compact",
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("compact status: %d body=%s", resp.StatusCode, readBody(t, resp))
	}

	list, err := bus.List(context.Background(), "history", eventbus.ListOptions{
		ScopeType: "task",
		ScopeID:   "operator",
		Limit:     20,
		Order:     "lifo",
	})
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	if len(list) == 0 {
		t.Fatalf("expected compact history event")
	}
	events, err := bus.Read(context.Background(), "history", []string{list[0].ID}, "")
	if err != nil || len(events) == 0 {
		t.Fatalf("read history event: %v", err)
	}
	entry, ok := engine.HistoryEntryFromEvent(events[0])
	if !ok {
		t.Fatalf("expected parseable history entry")
	}
	if entry.Type != "context_compaction" {
		t.Fatalf("expected context_compaction entry, got %q", entry.Type)
	}
	if entry.Generation < 2 {
		t.Fatalf("expected generation >= 2 after compact, got %d", entry.Generation)
	}
}

func TestServerStreamSubscribe(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	rt := engine.NewRuntime(bus, mgr, nil)
	server := &Server{Tasks: mgr, Bus: bus, Runtime: rt}
	mux := server.Handler()

	req := testutil.NewRequest(http.MethodGet, "/api/streams/subscribe?streams=task_output", nil)
	rec := testutil.NewStreamRecorder()
	resp := &http.Response{StatusCode: rec.Code, Body: rec.Body}
	errChan := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req = req.WithContext(ctx)
	go func() {
		mux.ServeHTTP(rec, req)
		errChan <- rec.Close()
	}()
	defer resp.Body.Close()

	got := make(chan struct{}, 1)

	go func() {
		reader := bufio.NewReader(resp.Body)
		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				return
			}
			if bytes.HasPrefix(line, []byte("data:")) {
				got <- struct{}{}
				return
			}
		}
	}()

	time.Sleep(50 * time.Millisecond)
	_, _ = bus.Push(context.Background(), eventbus.EventInput{Stream: "task_output", Body: "hello"})

	select {
	case <-got:
		cancel()
		return
	case <-ctx.Done():
		t.Fatalf("timeout waiting for sse")
	}
}

func doJSON(t *testing.T, client *http.Client, method, path string, payload any) *http.Response {
	t.Helper()
	var body *bytes.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		body = bytes.NewReader(data)
	} else {
		body = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, "http://in-process"+path, body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}

func decodeJSONResponse(t *testing.T, resp *http.Response, dest any) {
	t.Helper()
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(dest); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return string(data)
}

type apiFakeProvider struct{}

type apiFakeStream struct{}

func (p *apiFakeProvider) Company() string              { return "fake" }
func (p *apiFakeProvider) Model() string                { return "fake" }
func (p *apiFakeProvider) SetDebugger(_ llms.Debugger)  {}
func (p *apiFakeProvider) SetHTTPClient(_ *http.Client) {}
func (p *apiFakeProvider) Generate(_ context.Context, _ content.Content, _ []llms.Message, _ *llmtools.Toolbox, _ *llmtools.ValueSchema) llms.ProviderStream {
	return &apiFakeStream{}
}

func (s *apiFakeStream) Err() error { return nil }
func (s *apiFakeStream) Message() llms.Message {
	return llms.Message{Role: "assistant", Content: content.FromText("ok")}
}
func (s *apiFakeStream) Text() string             { return "ok" }
func (s *apiFakeStream) Image() (string, string)  { return "", "" }
func (s *apiFakeStream) Thought() content.Thought { return content.Thought{} }
func (s *apiFakeStream) ToolCall() llms.ToolCall  { return llms.ToolCall{} }
func (s *apiFakeStream) Usage() llms.Usage        { return llms.Usage{} }
func (s *apiFakeStream) Iter() func(func(llms.StreamStatus) bool) {
	return func(yield func(llms.StreamStatus) bool) {
		yield(llms.StreamStatusText)
	}
}
