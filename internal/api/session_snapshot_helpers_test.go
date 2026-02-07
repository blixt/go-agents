package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flitsinc/go-agents/internal/agenttools"
	"github.com/flitsinc/go-agents/internal/ai"
	"github.com/flitsinc/go-agents/internal/engine"
	"github.com/flitsinc/go-agents/internal/eventbus"
	"github.com/flitsinc/go-agents/internal/tasks"
	"github.com/flitsinc/go-agents/internal/testutil"
	"github.com/flitsinc/go-llms/content"
	"github.com/flitsinc/go-llms/llms"
	llmtools "github.com/flitsinc/go-llms/tools"
)

type deterministicSource struct {
	mu     sync.Mutex
	now    time.Time
	step   time.Duration
	nextID int
}

func newDeterministicSource(start time.Time, step time.Duration) *deterministicSource {
	if start.IsZero() {
		start = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	if step <= 0 {
		step = time.Second
	}
	return &deterministicSource{now: start.UTC(), step: step}
}

func (d *deterministicSource) Now() time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	current := d.now.UTC()
	d.now = d.now.Add(d.step)
	return current
}

func (d *deterministicSource) NewID() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.nextID++
	return fmt.Sprintf("id-%06d", d.nextID)
}

type ToolFactory func(*tasks.Manager) llmtools.Tool

func ExecToolFactory() ToolFactory {
	return func(mgr *tasks.Manager) llmtools.Tool {
		return agenttools.ExecTool(mgr)
	}
}

type MockExecParams struct {
	Code        string `json:"code"`
	ID          string `json:"id,omitempty"`
	WaitSeconds int    `json:"wait_seconds,omitempty"`
}

func MockExecToolFactory(result map[string]any) ToolFactory {
	cloned := cloneAnyMap(result)
	return func(_ *tasks.Manager) llmtools.Tool {
		return llmtools.Func(
			"Exec",
			"Run TypeScript code in an isolated Bun runtime and return a task id",
			"exec",
			func(_ llmtools.Runner, _ MockExecParams) llmtools.Result {
				out := map[string]any{
					"status":  "completed",
					"task_id": "mock-exec-task",
				}
				if len(cloned) > 0 {
					out["result"] = cloneAnyMap(cloned)
				}
				return llmtools.Success(out)
			},
		)
	}
}

func cloneAnyMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

type SnapshotFixtureOptions struct {
	StartTime     time.Time
	Tick          time.Duration
	Provider      llms.Provider
	ToolFactories []ToolFactory
}

type SnapshotFixture struct {
	DB      *sql.DB
	Bus     *eventbus.Bus
	Manager *tasks.Manager
	Runtime *engine.Runtime
	Client  *http.Client
	Clock   *deterministicSource
}

func NewSnapshotFixture(t *testing.T, opts SnapshotFixtureOptions) *SnapshotFixture {
	t.Helper()
	if opts.Provider == nil {
		t.Fatalf("snapshot fixture requires provider")
	}
	db, closeFn := testutil.OpenTestDB(t)
	t.Cleanup(closeFn)

	clock := newDeterministicSource(opts.StartTime, opts.Tick)
	bus := eventbus.NewBus(db,
		eventbus.WithClock(clock.Now),
		eventbus.WithIDGenerator(clock.NewID),
	)
	mgr := tasks.NewManager(db, bus,
		tasks.WithClock(clock.Now),
		tasks.WithIDGenerator(clock.NewID),
	)
	tools := make([]llmtools.Tool, 0, len(opts.ToolFactories))
	for _, factory := range opts.ToolFactories {
		if factory == nil {
			continue
		}
		tools = append(tools, factory(mgr))
	}
	runtimeClient := &ai.Client{LLM: llms.New(opts.Provider, tools...)}
	rt := engine.NewRuntime(bus, mgr, runtimeClient, engine.WithClock(clock.Now))

	server := &Server{Tasks: mgr, Bus: bus, Runtime: rt, NowFn: clock.Now}
	client := testutil.NewInProcessClient(server.Handler())

	return &SnapshotFixture{
		DB:      db,
		Bus:     bus,
		Manager: mgr,
		Runtime: rt,
		Client:  client,
		Clock:   clock,
	}
}

func (f *SnapshotFixture) FetchState(t *testing.T) stateResponse {
	t.Helper()
	resp := doJSON(t, f.Client, "GET", "/api/state?tasks=100&updates=200&streams=200&history=400", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("state status: %d body=%s", resp.StatusCode, readBody(t, resp))
	}
	defer resp.Body.Close()

	var state stateResponse
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		t.Fatalf("decode state response: %v", err)
	}
	return state
}

func waitForSpawnedTask(ctx context.Context, bus *eventbus.Bus, taskType string, handler func(taskID string) error) error {
	if bus == nil {
		return fmt.Errorf("bus is required")
	}
	sub := bus.Subscribe(ctx, []string{"task_input"})
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for %s task: %w", taskType, ctx.Err())
		case evt, ok := <-sub:
			if !ok {
				return fmt.Errorf("task_input subscription closed")
			}
			if evt.Stream != "task_input" {
				continue
			}
			if mapString(evt.Metadata, "action") != "spawn" || mapString(evt.Metadata, "task_type") != taskType {
				continue
			}
			taskID := strings.TrimSpace(mapString(evt.Metadata, "task_id"))
			if taskID == "" {
				continue
			}
			return handler(taskID)
		}
	}
}

type scriptedProvider struct {
	mu      sync.Mutex
	streams []llms.ProviderStream
	next    int
}

func newScriptedProvider(streams ...llms.ProviderStream) *scriptedProvider {
	return &scriptedProvider{streams: append([]llms.ProviderStream(nil), streams...)}
}

func (p *scriptedProvider) Company() string              { return "test" }
func (p *scriptedProvider) Model() string                { return "test" }
func (p *scriptedProvider) SetDebugger(_ llms.Debugger)  {}
func (p *scriptedProvider) SetHTTPClient(_ *http.Client) {}

func (p *scriptedProvider) Generate(_ context.Context, _ content.Content, _ []llms.Message, _ *llmtools.Toolbox, _ *llmtools.ValueSchema) llms.ProviderStream {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.next >= len(p.streams) {
		return newScriptedStream(scriptedStreamSpec{Err: fmt.Errorf("unexpected provider call #%d", p.next+1)})
	}
	stream := p.streams[p.next]
	p.next++
	return stream
}

type scriptedStreamSpec struct {
	Message   llms.Message
	Text      string
	Thought   content.Thought
	ToolCall  llms.ToolCall
	Statuses  []llms.StreamStatus
	Usage     llms.Usage
	ImageURL  string
	ImageMIME string
	Err       error
}

type scriptedStream struct {
	spec scriptedStreamSpec
}

func newScriptedStream(spec scriptedStreamSpec) *scriptedStream {
	if spec.Message.Role == "" {
		spec.Message.Role = "assistant"
	}
	if spec.Text != "" {
		spec.Message.Content = content.FromText(spec.Text)
	}
	if spec.ToolCall.ID != "" && len(spec.Message.ToolCalls) == 0 {
		spec.Message.ToolCalls = []llms.ToolCall{spec.ToolCall}
	}
	if len(spec.Statuses) == 0 {
		var statuses []llms.StreamStatus
		if strings.TrimSpace(spec.Thought.ID) != "" || strings.TrimSpace(spec.Thought.Text) != "" {
			statuses = append(statuses, llms.StreamStatusThinking)
		}
		if spec.Text != "" {
			statuses = append(statuses, llms.StreamStatusText)
		}
		if len(spec.Message.ToolCalls) > 0 {
			statuses = append(statuses, llms.StreamStatusToolCallBegin, llms.StreamStatusToolCallReady)
		}
		spec.Statuses = statuses
	}
	return &scriptedStream{spec: spec}
}

func (s *scriptedStream) Err() error               { return s.spec.Err }
func (s *scriptedStream) Message() llms.Message    { return s.spec.Message }
func (s *scriptedStream) Text() string             { return s.spec.Text }
func (s *scriptedStream) Image() (string, string)  { return s.spec.ImageURL, s.spec.ImageMIME }
func (s *scriptedStream) Thought() content.Thought { return s.spec.Thought }
func (s *scriptedStream) ToolCall() llms.ToolCall {
	if s.spec.ToolCall.ID != "" {
		return s.spec.ToolCall
	}
	if len(s.spec.Message.ToolCalls) > 0 {
		return s.spec.Message.ToolCalls[0]
	}
	return llms.ToolCall{}
}
func (s *scriptedStream) Usage() llms.Usage { return s.spec.Usage }
func (s *scriptedStream) Iter() func(func(llms.StreamStatus) bool) {
	statuses := append([]llms.StreamStatus(nil), s.spec.Statuses...)
	return func(yield func(llms.StreamStatus) bool) {
		for _, status := range statuses {
			if !yield(status) {
				return
			}
		}
	}
}

func renderSessionSnapshotMarkdown(title string, state stateResponse) ([]byte, error) {
	var b strings.Builder
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Session Snapshot"
	}
	b.WriteString("# ")
	b.WriteString(title)
	b.WriteString("\n\n")

	agents := append([]agentState(nil), state.Agents...)
	sort.Slice(agents, func(i, j int) bool { return agents[i].ID < agents[j].ID })
	b.WriteString("## Agents\n\n")
	appendJSONBlock(&b, projectAgents(agents))

	sessionIDs := sortedMapKeys(state.Sessions)
	b.WriteString("## Sessions\n\n")
	for _, agentID := range sessionIDs {
		b.WriteString("### ")
		b.WriteString(agentID)
		b.WriteString("\n\n")
		appendJSONBlock(&b, projectSession(state.Sessions[agentID]))
	}

	tasksList := append([]tasks.Task(nil), state.Tasks...)
	sort.Slice(tasksList, func(i, j int) bool {
		if tasksList[i].CreatedAt.Equal(tasksList[j].CreatedAt) {
			return tasksList[i].ID < tasksList[j].ID
		}
		return tasksList[i].CreatedAt.Before(tasksList[j].CreatedAt)
	})
	b.WriteString("## Tasks\n\n")
	appendJSONBlock(&b, projectTasks(tasksList))

	updateTaskIDs := sortedMapKeys(state.Updates)
	b.WriteString("## Task Updates\n\n")
	for _, taskID := range updateTaskIDs {
		updates := append([]tasks.Update(nil), state.Updates[taskID]...)
		sort.Slice(updates, func(i, j int) bool {
			return updateSortKey(updates[i]) < updateSortKey(updates[j])
		})
		b.WriteString("### ")
		b.WriteString(taskID)
		b.WriteString("\n\n")
		appendJSONBlock(&b, projectUpdates(updates))
	}

	historyAgentIDs := sortedMapKeys(state.Histories)
	b.WriteString("## Histories\n\n")
	for _, agentID := range historyAgentIDs {
		history := state.Histories[agentID]
		b.WriteString("### ")
		b.WriteString(agentID)
		b.WriteString(" (generation ")
		b.WriteString(strconv.FormatInt(history.Generation, 10))
		b.WriteString(")\n\n")

		entries := append([]engine.AgentHistoryEntry(nil), history.Entries...)
		sort.Slice(entries, func(i, j int) bool {
			return historyEntrySortKey(entries[i]) < historyEntrySortKey(entries[j])
		})
		for i, entry := range entries {
			b.WriteString("#### Entry ")
			b.WriteString(strconv.Itoa(i + 1))
			b.WriteString(" · ")
			b.WriteString(entry.Type)
			b.WriteString(" · ")
			b.WriteString(entry.Role)
			b.WriteString("\n\n")

			entryMeta := map[string]any{}
			if strings.TrimSpace(entry.TaskID) != "" {
				entryMeta["task_id"] = strings.TrimSpace(entry.TaskID)
			}
			if entry.ToolCallID != "" {
				entryMeta["tool_call_id"] = entry.ToolCallID
			}
			if entry.ToolName != "" {
				entryMeta["tool_name"] = entry.ToolName
			}
			if entry.ToolStatus != "" {
				entryMeta["tool_status"] = entry.ToolStatus
			}
			if len(entryMeta) > 0 {
				appendJSONBlock(&b, entryMeta)
			}

			if entry.Content != "" {
				fence := "text"
				if strings.Contains(entry.Content, "<user_turn") {
					fence = "xml"
				}
				renderedContent := entry.Content
				if fence == "xml" {
					renderedContent = canonicalizeLLMInputXML(renderedContent)
				}
				b.WriteString("```")
				b.WriteString(fence)
				b.WriteString("\n")
				b.WriteString(renderedContent)
				if !strings.HasSuffix(renderedContent, "\n") {
					b.WriteString("\n")
				}
				b.WriteString("```\n\n")
			}

			if data := normalizedHistoryData(entry.Data); len(data) > 0 {
				appendJSONBlock(&b, data)
			}
		}
	}

	return []byte(b.String()), nil
}

func appendJSONBlock(b *strings.Builder, v any) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		b.WriteString("```text\n")
		b.WriteString(err.Error())
		b.WriteString("\n```\n\n")
		return
	}
	b.WriteString("```json\n")
	b.Write(data)
	b.WriteString("\n```\n\n")
}

func sortedMapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func mapString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	val, ok := m[key]
	if !ok || val == nil {
		return ""
	}
	str, ok := val.(string)
	if !ok {
		return ""
	}
	return str
}

func historyEntrySortKey(entry engine.AgentHistoryEntry) string {
	turn := historyTurn(entry.Data)
	normalizedData := normalizedHistoryData(entry.Data)
	dataJSON, _ := json.Marshal(normalizedData)
	return strings.Join([]string{
		fmt.Sprintf("%03d", turn),
		fmt.Sprintf("%03d", historyTypeRank(entry.Type)),
		entry.Type,
		entry.Role,
		strings.TrimSpace(entry.ToolCallID),
		strings.TrimSpace(entry.ToolName),
		strings.TrimSpace(entry.ToolStatus),
		strings.TrimSpace(entry.Content),
		string(dataJSON),
	}, "|")
}

func historyTurn(data map[string]any) int {
	if data == nil {
		return 0
	}
	raw, ok := data["turn"]
	if !ok {
		return 0
	}
	switch v := raw.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

func historyTypeRank(entryType string) int {
	switch entryType {
	case "tools_config":
		return 10
	case "system_prompt":
		return 20
	case "user_message":
		return 30
	case "llm_input":
		return 40
	case "reasoning":
		return 50
	case "assistant_message":
		return 60
	case "tool_call":
		return 70
	case "tool_status":
		return 80
	case "tool_result":
		return 90
	case "context_event":
		return 100
	case "system_update":
		return 110
	case "error":
		return 120
	default:
		return 999
	}
}

func normalizedHistoryData(data map[string]any) map[string]any {
	if len(data) == 0 {
		return nil
	}
	out := make(map[string]any, len(data))
	for key, value := range data {
		switch key {
		case "event_id", "created_at":
			continue
		default:
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

type agentSnapshot struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	ActiveTasks int    `json:"active_tasks"`
	Generation  int64  `json:"generation"`
}

func projectAgents(agents []agentState) []agentSnapshot {
	out := make([]agentSnapshot, 0, len(agents))
	for _, agent := range agents {
		out = append(out, agentSnapshot{
			ID:          agent.ID,
			Status:      agent.Status,
			ActiveTasks: agent.ActiveTasks,
			Generation:  agent.Generation,
		})
	}
	return out
}

type sessionSnapshot struct {
	AgentID    string `json:"agent_id"`
	RootTaskID string `json:"root_task_id,omitempty"`
	LLMTaskID  string `json:"llm_task_id,omitempty"`
	Prompt     string `json:"prompt"`
	LastInput  string `json:"last_input"`
	LastOutput string `json:"last_output"`
	LastError  string `json:"last_error,omitempty"`
}

func projectSession(s engine.Session) sessionSnapshot {
	return sessionSnapshot{
		AgentID:    s.AgentID,
		RootTaskID: s.RootTaskID,
		LLMTaskID:  s.LLMTaskID,
		Prompt:     s.Prompt,
		LastInput:  s.LastInput,
		LastOutput: s.LastOutput,
		LastError:  s.LastError,
	}
}

type taskSnapshot struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Status   tasks.Status   `json:"status"`
	Owner    string         `json:"owner"`
	ParentID string         `json:"parent_id,omitempty"`
	Mode     string         `json:"mode,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
	Payload  map[string]any `json:"payload,omitempty"`
	Result   map[string]any `json:"result,omitempty"`
	Error    string         `json:"error,omitempty"`
}

func projectTasks(items []tasks.Task) []taskSnapshot {
	out := make([]taskSnapshot, 0, len(items))
	for _, item := range items {
		out = append(out, taskSnapshot{
			ID:       item.ID,
			Type:     item.Type,
			Status:   item.Status,
			Owner:    item.Owner,
			ParentID: item.ParentID,
			Mode:     item.Mode,
			Metadata: item.Metadata,
			Payload:  item.Payload,
			Result:   item.Result,
			Error:    item.Error,
		})
	}
	return out
}

type updateSnapshot struct {
	Kind    string         `json:"kind"`
	Payload map[string]any `json:"payload,omitempty"`
}

func projectUpdates(items []tasks.Update) []updateSnapshot {
	out := make([]updateSnapshot, 0, len(items))
	for _, item := range items {
		out = append(out, updateSnapshot{
			Kind:    item.Kind,
			Payload: item.Payload,
		})
	}
	return out
}

func updateSortKey(update tasks.Update) string {
	payload, _ := json.Marshal(update.Payload)
	return strings.Join([]string{update.Kind, string(payload)}, "|")
}

func canonicalizeLLMInputXML(raw string) string {
	dec := xml.NewDecoder(strings.NewReader(raw))
	var out strings.Builder
	enc := xml.NewEncoder(&out)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return raw
		}
		switch t := tok.(type) {
		case xml.StartElement:
			attrs := make([]xml.Attr, 0, len(t.Attr))
			for _, attr := range t.Attr {
				val := attr.Value
				switch attr.Name.Local {
				case "id":
					val = "<id>"
				case "created_at", "generated_at", "previous", "current":
					val = "<time>"
				case "elapsed_seconds":
					val = "<seconds>"
				}
				attrs = append(attrs, xml.Attr{Name: attr.Name, Value: val})
			}
			sort.Slice(attrs, func(i, j int) bool {
				li := attrs[i].Name.Space + ":" + attrs[i].Name.Local
				lj := attrs[j].Name.Space + ":" + attrs[j].Name.Local
				return li < lj
			})
			t.Attr = attrs
			if err := enc.EncodeToken(t); err != nil {
				return raw
			}
		default:
			if err := enc.EncodeToken(tok); err != nil {
				return raw
			}
		}
	}
	if err := enc.Flush(); err != nil {
		return raw
	}
	return out.String()
}

func assertSnapshot(t *testing.T, relPath string, got []byte) {
	t.Helper()
	path := filepath.Join(".", relPath)
	if os.Getenv("UPDATE_SNAPSHOTS") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir snapshot dir: %v", err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write snapshot: %v", err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read snapshot %s: %v", path, err)
	}
	if string(got) != string(want) {
		t.Fatalf("snapshot mismatch for %s\n\n--- got ---\n%s\n--- want ---\n%s", path, string(got), string(want))
	}
}
