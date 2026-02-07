package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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

func TestWeatherSessionStateSnapshot(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := tasks.NewManager(db, bus)
	provider := newWeatherSessionProvider()
	runtimeClient := &ai.Client{LLM: llms.New(provider, agenttools.ExecTool(mgr))}
	rt := engine.NewRuntime(bus, mgr, runtimeClient)

	server := &Server{Tasks: mgr, Bus: bus, Runtime: rt}
	client := testutil.NewInProcessClient(server.Handler())

	completeDone := make(chan error, 1)
	go func() {
		completeDone <- completeFirstExecTaskForWeather(mgr, 4*time.Second)
	}()

	session, err := rt.RunOnce(context.Background(), "operator", "what's the weather in amsterdam")
	if err != nil {
		t.Fatalf("run once: %v", err)
	}
	select {
	case completeErr := <-completeDone:
		if completeErr != nil {
			t.Fatalf("complete weather exec task: %v", completeErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for weather exec completion helper")
	}
	if !strings.Contains(session.LastOutput, "Perfect! Here's the current weather in Amsterdam:") {
		t.Fatalf("unexpected session output: %q", session.LastOutput)
	}

	stateResp := doJSON(t, client, "GET", "/api/state?tasks=100&updates=200&streams=200&history=400", nil)
	if stateResp.StatusCode != http.StatusOK {
		t.Fatalf("state status: %d body=%s", stateResp.StatusCode, readBody(t, stateResp))
	}
	defer stateResp.Body.Close()

	var raw any
	if err := json.NewDecoder(stateResp.Body).Decode(&raw); err != nil {
		t.Fatalf("decode state response: %v", err)
	}

	norm := newSnapshotNormalizer()
	normalized := norm.Normalize(raw)
	got, err := json.MarshalIndent(normalized, "", "  ")
	if err != nil {
		t.Fatalf("marshal normalized snapshot: %v", err)
	}
	got = append(got, '\n')

	assertSnapshot(t, filepath.Join("testdata", "weather_session_snapshot.json"), got)
}

type weatherSessionProvider struct {
	mu    sync.Mutex
	calls int
}

func newWeatherSessionProvider() *weatherSessionProvider {
	return &weatherSessionProvider{}
}

func (p *weatherSessionProvider) Company() string              { return "test" }
func (p *weatherSessionProvider) Model() string                { return "test" }
func (p *weatherSessionProvider) SetDebugger(_ llms.Debugger)  {}
func (p *weatherSessionProvider) SetHTTPClient(_ *http.Client) {}
func (p *weatherSessionProvider) Generate(_ context.Context, _ content.Content, _ []llms.Message, _ *llmtools.Toolbox, _ *llmtools.ValueSchema) llms.ProviderStream {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.calls == 1 {
		return newWeatherPlanStream()
	}
	return newWeatherFinalStream()
}

type weatherPlanStream struct {
	toolCall llms.ToolCall
}

func newWeatherPlanStream() *weatherPlanStream {
	args, _ := json.Marshal(map[string]any{
		"code": `// Fetch weather data for Amsterdam.
globalThis.result = {
  location: "Amsterdam, Netherlands",
  temperature: "5°C (41°F)",
  condition: "Partly Cloudy",
};`,
		"wait_seconds": 5,
	})
	return &weatherPlanStream{
		toolCall: llms.ToolCall{
			ID:        "toolu_weather_exec_1",
			Name:      "exec",
			Arguments: args,
		},
	}
}

func (s *weatherPlanStream) Err() error { return nil }
func (s *weatherPlanStream) Message() llms.Message {
	return llms.Message{
		Role:      "assistant",
		Content:   content.FromText("I'll fetch the current weather in Amsterdam for you.\n\n"),
		ToolCalls: []llms.ToolCall{s.toolCall},
	}
}
func (s *weatherPlanStream) Text() string {
	return "I'll fetch the current weather in Amsterdam for you.\n\n"
}
func (s *weatherPlanStream) Image() (string, string) { return "", "" }
func (s *weatherPlanStream) Thought() content.Thought {
	return content.Thought{
		ID:      "reasoning-weather-1",
		Text:    "I should call exec to gather fresh weather data before answering.",
		Summary: true,
	}
}
func (s *weatherPlanStream) ToolCall() llms.ToolCall { return s.toolCall }
func (s *weatherPlanStream) Usage() llms.Usage       { return llms.Usage{} }
func (s *weatherPlanStream) Iter() func(func(llms.StreamStatus) bool) {
	return func(yield func(llms.StreamStatus) bool) {
		if !yield(llms.StreamStatusThinking) {
			return
		}
		if !yield(llms.StreamStatusText) {
			return
		}
		if !yield(llms.StreamStatusToolCallBegin) {
			return
		}
		_ = yield(llms.StreamStatusToolCallReady)
	}
}

type weatherFinalStream struct{}

func newWeatherFinalStream() *weatherFinalStream { return &weatherFinalStream{} }

func (s *weatherFinalStream) Err() error { return nil }
func (s *weatherFinalStream) Message() llms.Message {
	return llms.Message{Role: "assistant", Content: content.FromText(weatherFinalText)}
}
func (s *weatherFinalStream) Text() string             { return weatherFinalText }
func (s *weatherFinalStream) Image() (string, string)  { return "", "" }
func (s *weatherFinalStream) Thought() content.Thought { return content.Thought{} }
func (s *weatherFinalStream) ToolCall() llms.ToolCall  { return llms.ToolCall{} }
func (s *weatherFinalStream) Usage() llms.Usage        { return llms.Usage{} }
func (s *weatherFinalStream) Iter() func(func(llms.StreamStatus) bool) {
	return func(yield func(llms.StreamStatus) bool) {
		_ = yield(llms.StreamStatusText)
	}
}

const weatherFinalText = `Perfect! Here's the current weather in Amsterdam:

🌤️ Amsterdam, Netherlands

Temperature: 5°C (41°F)
Condition: Partly Cloudy
Humidity: 75%
Wind: 19 km/h SW
Pressure: 1019 mb`

func completeFirstExecTaskForWeather(mgr *tasks.Manager, timeout time.Duration) error {
	deadline := time.After(timeout)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			return fmt.Errorf("timed out waiting for weather exec task")
		case <-ticker.C:
			items, err := mgr.List(context.Background(), tasks.ListFilter{
				Type:  "exec",
				Owner: "operator",
				Limit: 5,
			})
			if err != nil {
				return fmt.Errorf("list exec tasks: %w", err)
			}
			if len(items) == 0 {
				continue
			}
			sort.Slice(items, func(i, j int) bool {
				if items[i].CreatedAt.Equal(items[j].CreatedAt) {
					return items[i].ID < items[j].ID
				}
				return items[i].CreatedAt.Before(items[j].CreatedAt)
			})
			task := items[0]
			_ = mgr.MarkRunning(context.Background(), task.ID)
			_ = mgr.RecordUpdate(context.Background(), task.ID, "stdout", map[string]any{
				"text": "Amsterdam now: 5°C, partly cloudy.",
			})
			if err := mgr.Complete(context.Background(), task.ID, map[string]any{
				"location":    "Amsterdam, Netherlands",
				"temperature": "5°C (41°F)",
				"condition":   "Partly Cloudy",
				"humidity":    "75%",
				"wind":        "19 km/h SW",
				"pressure":    "1019 mb",
			}); err != nil {
				return fmt.Errorf("complete task: %w", err)
			}
			return nil
		}
	}
}

type snapshotNormalizer struct {
}

func newSnapshotNormalizer() *snapshotNormalizer {
	return &snapshotNormalizer{}
}

var (
	ulidPattern    = regexp.MustCompile(`\b[0-9A-HJKMNP-TV-Z]{26}\b`)
	rfc3339Pattern = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z\b`)
)

func (n *snapshotNormalizer) Normalize(v any) any {
	switch value := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(value))
		keys := make([]string, 0, len(value))
		for k := range value {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			subv := value[k]
			normKey := n.normalizeString(k)
			normVal := n.Normalize(subv)
			if normKey == "entries" {
				if entries, ok := normVal.([]any); ok {
					filtered := make([]any, 0, len(entries))
					for _, entry := range entries {
						if m, ok := entry.(map[string]any); ok {
							if fmt.Sprintf("%v", m["type"]) == "context_event" {
								continue
							}
						}
						filtered = append(filtered, entry)
					}
					sort.SliceStable(filtered, func(i, j int) bool {
						return snapshotEntrySortKey(filtered[i]) < snapshotEntrySortKey(filtered[j])
					})
					normVal = filtered
				}
			}
			out[normKey] = normVal
		}
		return out
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = n.Normalize(item)
		}
		return out
	case string:
		return n.normalizeString(value)
	default:
		return value
	}
}

func snapshotEntrySortKey(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Sprintf("%T:%v", v, v)
	}
	entryType := fmt.Sprintf("%v", m["type"])
	role := fmt.Sprintf("%v", m["role"])
	content := fmt.Sprintf("%v", m["content"])
	toolCallID := fmt.Sprintf("%v", m["tool_call_id"])
	toolName := fmt.Sprintf("%v", m["tool_name"])
	toolStatus := fmt.Sprintf("%v", m["tool_status"])
	data := ""
	if raw, ok := m["data"]; ok {
		if encoded, err := json.Marshal(raw); err == nil {
			data = string(encoded)
		} else {
			data = fmt.Sprintf("%v", raw)
		}
	}
	return strings.Join([]string{
		entryType,
		role,
		content,
		toolCallID,
		toolName,
		toolStatus,
		data,
	}, "|")
}

func (n *snapshotNormalizer) normalizeString(s string) string {
	if _, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return "<time>"
	}
	s = rfc3339Pattern.ReplaceAllString(s, "<time>")
	return ulidPattern.ReplaceAllString(s, "<id>")
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
