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

	// Agent run enqueues work via the event bus.
	resp = doJSON(t, client, "POST", "/api/agents/operator/run", map[string]any{"message": "hello", "source": "human"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("agent run status: %d body=%s", resp.StatusCode, readBody(t, resp))
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
