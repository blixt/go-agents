package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/flitsinc/go-agents/internal/ai"
	"github.com/flitsinc/go-agents/internal/engine"
	"github.com/flitsinc/go-agents/internal/eventbus"
	"github.com/flitsinc/go-agents/internal/state"
	"github.com/flitsinc/go-agents/internal/tasks"
	"github.com/flitsinc/go-agents/internal/testutil"
	"github.com/flitsinc/go-llms/content"
	"github.com/flitsinc/go-llms/llms"
	llmtools "github.com/flitsinc/go-llms/tools"
)

func TestServerTasksAndStreams(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	store := state.NewStore(db)
	restartCalled := false

	runtimeClient := &ai.Client{LLM: llms.New(&apiFakeProvider{})}
	rt := engine.NewRuntime(bus, mgr, runtimeClient)

	server := &Server{Tasks: mgr, Bus: bus, Store: store, Runtime: rt, Restart: func() error { restartCalled = true; return nil }, RestartToken: "token"}
	h := server.Handler()
	client := testutil.NewInProcessClient(h)

	// Create a task.
	createBody := map[string]any{
		"type":  "exec",
		"owner": "tester",
		"payload": map[string]any{
			"code": "globalThis.result = 1",
		},
	}
	resp := doJSON(t, client, "POST", "/api/tasks", createBody)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("unexpected status: %d body=%s", resp.StatusCode, readBody(t, resp))
	}
	var task tasks.Task
	decodeJSONResponse(t, resp, &task)
	if task.ID == "" {
		t.Fatalf("expected task id")
	}

	// List tasks.
	resp = doJSON(t, client, "GET", "/api/tasks?limit=5", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status: %d body=%s", resp.StatusCode, readBody(t, resp))
	}
	var list []tasks.Task
	decodeJSONResponse(t, resp, &list)
	if len(list) == 0 {
		t.Fatalf("expected task list")
	}

	// Get task.
	resp = doJSON(t, client, "GET", "/api/tasks/"+task.ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get status: %d body=%s", resp.StatusCode, readBody(t, resp))
	}

	// Post update.
	updatePayload := map[string]any{"kind": "progress", "payload": map[string]any{"pct": 10}}
	resp = doJSON(t, client, "POST", "/api/tasks/"+task.ID+"/updates", updatePayload)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update status: %d body=%s", resp.StatusCode, readBody(t, resp))
	}

	resp = doJSON(t, client, "GET", "/api/tasks/"+task.ID+"/updates?limit=5", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("updates list status: %d body=%s", resp.StatusCode, readBody(t, resp))
	}
	var updates []tasks.Update
	decodeJSONResponse(t, resp, &updates)
	if len(updates) == 0 {
		t.Fatalf("expected updates")
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

	// Events endpoint.
	resp = doJSON(t, client, "POST", "/api/events", map[string]any{
		"stream":  "errors",
		"subject": "oops",
		"body":    "failed",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("events status: %d body=%s", resp.StatusCode, readBody(t, resp))
	}

	// Streams list/read/ack
	resp = doJSON(t, client, "GET", "/api/streams/errors?limit=5", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("streams list status: %d body=%s", resp.StatusCode, readBody(t, resp))
	}
	var summaries []eventbus.EventSummary
	decodeJSONResponse(t, resp, &summaries)
	if len(summaries) == 0 {
		t.Fatalf("expected stream items")
	}

	resp = doJSON(t, client, "POST", "/api/streams/errors/read", map[string]any{"ids": []string{summaries[0].ID}, "reader": "tester"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("streams read status: %d body=%s", resp.StatusCode, readBody(t, resp))
	}

	resp = doJSON(t, client, "POST", "/api/streams/errors/ack", map[string]any{"ids": []string{summaries[0].ID}, "reader": "tester"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("streams ack status: %d body=%s", resp.StatusCode, readBody(t, resp))
	}

	// Restart endpoint with token.
	req, _ := http.NewRequest(http.MethodPost, "http://in-process/api/admin/restart", nil)
	req.Header.Set("X-Restart-Token", "token")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("restart request: %v", err)
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("restart status: %d body=%s", resp.StatusCode, readBody(t, resp))
	}
	if !restartCalled {
		t.Fatalf("expected restart to be called")
	}

	// Agents endpoint.
	resp = doJSON(t, client, "POST", "/api/agents", map[string]any{"profile": "operator", "status": "idle"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("agents create status: %d body=%s", resp.StatusCode, readBody(t, resp))
	}
	resp = doJSON(t, client, "GET", "/api/agents?limit=5", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("agents list status: %d body=%s", resp.StatusCode, readBody(t, resp))
	}

	// Actions endpoint.
	resp = doJSON(t, client, "POST", "/api/actions", map[string]any{"content": "run", "status": "queued"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("actions create status: %d body=%s", resp.StatusCode, readBody(t, resp))
	}
	resp = doJSON(t, client, "GET", "/api/actions?limit=5", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("actions list status: %d body=%s", resp.StatusCode, readBody(t, resp))
	}

	resp = doJSON(t, client, "GET", "/api/prompt", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("prompt status: %d body=%s", resp.StatusCode, readBody(t, resp))
	}

	resp = doJSON(t, client, "GET", "/api/sessions/operator", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("session status: %d body=%s", resp.StatusCode, readBody(t, resp))
	}

	resp = doJSON(t, client, "POST", "/api/agents/operator/run", map[string]any{"message": "hello"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("agent run status: %d body=%s", resp.StatusCode, readBody(t, resp))
	}

	resp = doJSON(t, client, "GET", "/api/sessions/operator", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("session status: %d body=%s", resp.StatusCode, readBody(t, resp))
	}
}

func TestServerStreamSubscribe(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	store := state.NewStore(db)
	rt := engine.NewRuntime(bus, mgr, nil)
	server := &Server{Tasks: mgr, Bus: bus, Store: store, Runtime: rt}
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
