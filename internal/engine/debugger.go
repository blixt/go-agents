package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/flitsinc/go-agents/internal/eventbus"
	"github.com/flitsinc/go-llms/llms"
)

type llmDebugEntry struct {
	Index     int            `json:"index"`
	Kind      string         `json:"kind"`
	CreatedAt string         `json:"created_at"`
	Payload   map[string]any `json:"payload,omitempty"`
}

type llmDebugTrace struct {
	AgentID   string          `json:"agent_id"`
	TaskID    string          `json:"task_id"`
	CreatedAt string          `json:"created_at"`
	UpdatedAt string          `json:"updated_at"`
	Entries   []llmDebugEntry `json:"entries"`
}

type busDebugger struct {
	bus        *eventbus.Bus
	agentID    string
	taskID     string
	redactions []string
	keyPattern *regexp.Regexp
	debugPath  string
	mu         sync.Mutex
	trace      llmDebugTrace
}

func newBusDebugger(bus *eventbus.Bus, agentID, taskID, debugDir string) *busDebugger {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	debugger := &busDebugger{
		bus:        bus,
		agentID:    agentID,
		taskID:     taskID,
		redactions: loadAPIKeySecrets(),
		keyPattern: regexp.MustCompile(`([&?]key)=([^&]+)`),
		trace: llmDebugTrace{
			AgentID:   strings.TrimSpace(agentID),
			TaskID:    strings.TrimSpace(taskID),
			CreatedAt: now,
			UpdatedAt: now,
			Entries:   make([]llmDebugEntry, 0, 8),
		},
	}
	if path := tracePath(debugDir, agentID, taskID); path != "" {
		debugger.debugPath = path
		debugger.persist()
	}
	return debugger
}

func tracePath(debugDir, agentID, taskID string) string {
	dir := strings.TrimSpace(debugDir)
	if dir == "" {
		return ""
	}
	id := sanitizeTraceName(taskID)
	if id == "" {
		id = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	name := id + ".json"
	if agent := sanitizeTraceName(agentID); agent != "" {
		name = agent + "-" + name
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	return filepath.Join(dir, name)
}

func sanitizeTraceName(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(raw))
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

func loadAPIKeySecrets() []string {
	out := []string{}
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if !strings.HasSuffix(key, "API_KEY") || value == "" {
			continue
		}
		out = append(out, value)
	}
	return out
}

func (d *busDebugger) redact(text string) string {
	if text == "" {
		return text
	}
	if d.keyPattern != nil {
		text = d.keyPattern.ReplaceAllString(text, "$1=…")
	}
	for _, secret := range d.redactions {
		if secret == "" {
			continue
		}
		text = strings.ReplaceAll(text, secret, "…")
	}
	return text
}

func (d *busDebugger) push(subject string, payload map[string]any) {
	if d.bus == nil {
		return
	}
	_, _ = d.bus.Push(context.Background(), eventbus.EventInput{
		Stream:    "signals",
		ScopeType: "agent",
		ScopeID:   d.agentID,
		Subject:   subject,
		Body:      subject,
		Metadata: map[string]any{
			"kind":      "llm_debug",
			"agent_id":  d.agentID,
			"task_id":   d.taskID,
			"direction": subject,
			"priority":  "low",
		},
		Payload: payload,
	})
}

func (d *busDebugger) record(kind string, payload map[string]any) {
	if d.debugPath == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	entry := llmDebugEntry{
		Index:     len(d.trace.Entries) + 1,
		Kind:      strings.TrimSpace(kind),
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if len(payload) > 0 {
		clone := make(map[string]any, len(payload))
		for k, v := range payload {
			clone[k] = v
		}
		entry.Payload = clone
	}
	d.trace.Entries = append(d.trace.Entries, entry)
	d.trace.UpdatedAt = entry.CreatedAt
	d.persistLocked()
}

func (d *busDebugger) persist() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.persistLocked()
}

func (d *busDebugger) persistLocked() {
	if d.debugPath == "" {
		return
	}
	blob, err := json.MarshalIndent(d.trace, "", "  ")
	if err != nil {
		return
	}
	blob = append(blob, '\n')
	_ = os.WriteFile(d.debugPath, blob, 0o644)
}

func (d *busDebugger) RawRequest(endpoint string, data []byte) {
	payload := map[string]any{
		"endpoint": d.redact(endpoint),
		"data":     d.redact(string(data)),
	}
	d.push("llm_debug_request", payload)
	d.record("request", payload)
}

func (d *busDebugger) RawEvent(data []byte) {
	payload := map[string]any{
		"data": d.redact(string(data)),
	}
	d.push("llm_debug_event", payload)
	d.record("event", payload)
}

func (r *Runtime) attachDebugger(llm *llms.LLM, agentID, taskID string) {
	if llm == nil || r.Bus == nil {
		return
	}
	llm.WithDebugger(newBusDebugger(r.Bus, agentID, taskID, r.LLMDebugDir))
}
