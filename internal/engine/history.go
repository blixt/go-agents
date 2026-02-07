package engine

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/flitsinc/go-agents/internal/eventbus"
)

type AgentHistoryEntry struct {
	ID         string         `json:"id"`
	AgentID    string         `json:"agent_id"`
	Generation int64          `json:"generation"`
	Type       string         `json:"type"`
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	TaskID     string         `json:"task_id,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolName   string         `json:"tool_name,omitempty"`
	ToolStatus string         `json:"tool_status,omitempty"`
	Data       map[string]any `json:"data,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
}

type AgentHistory struct {
	AgentID    string              `json:"agent_id"`
	Generation int64               `json:"generation"`
	Entries    []AgentHistoryEntry `json:"entries"`
}

func (r *Runtime) appendHistory(ctx context.Context, agentID, entryType, role, content, taskID string, generation int64, data map[string]any) {
	if r.Bus == nil {
		return
	}
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return
	}
	entryType = strings.TrimSpace(entryType)
	if entryType == "" {
		entryType = "note"
	}
	role = strings.TrimSpace(role)
	if role == "" {
		role = "system"
	}
	if generation <= 0 {
		generation = r.historyGeneration(ctx, agentID)
	}
	r.rememberAgent(agentID)

	body := strings.TrimSpace(content)
	if body == "" {
		body = entryType
	}
	payload := map[string]any{
		"agent_id":   agentID,
		"generation": generation,
		"type":       entryType,
		"role":       role,
		"content":    content,
		"task_id":    strings.TrimSpace(taskID),
		"created_at": time.Now().UTC().Format(time.RFC3339Nano),
	}
	for k, v := range data {
		if v == nil {
			continue
		}
		payload[k] = v
	}
	_, _ = r.Bus.Push(ctx, eventbus.EventInput{
		Stream:    "history",
		ScopeType: "agent",
		ScopeID:   agentID,
		Subject:   fmt.Sprintf("%s:%s", role, entryType),
		Body:      body,
		Metadata: map[string]any{
			"kind":       "history_entry",
			"agent_id":   agentID,
			"generation": generation,
			"type":       entryType,
			"role":       role,
			"priority":   "low",
		},
		Payload: payload,
	})
}

func (r *Runtime) appendToolHistory(ctx context.Context, agentID, taskID, entryType, toolCallID, toolName, toolStatus, content string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	if strings.TrimSpace(toolCallID) != "" {
		data["tool_call_id"] = strings.TrimSpace(toolCallID)
	}
	if strings.TrimSpace(toolName) != "" {
		data["tool_name"] = strings.TrimSpace(toolName)
	}
	if strings.TrimSpace(toolStatus) != "" {
		data["tool_status"] = strings.TrimSpace(toolStatus)
	}
	r.appendHistory(ctx, agentID, entryType, "tool", content, taskID, 0, data)
}

func (r *Runtime) rememberAgent(agentID string) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return
	}
	r.knownMu.Lock()
	r.knownAgents[agentID] = struct{}{}
	r.knownMu.Unlock()
}

func (r *Runtime) KnownAgentIDs() []string {
	seen := map[string]struct{}{}
	r.knownMu.RLock()
	for id := range r.knownAgents {
		if strings.TrimSpace(id) == "" {
			continue
		}
		seen[id] = struct{}{}
	}
	r.knownMu.RUnlock()

	r.agentMu.RLock()
	for id := range r.agents {
		if strings.TrimSpace(id) == "" {
			continue
		}
		seen[id] = struct{}{}
	}
	r.agentMu.RUnlock()

	r.mu.RLock()
	for id := range r.sessions {
		if strings.TrimSpace(id) == "" {
			continue
		}
		seen[id] = struct{}{}
	}
	r.mu.RUnlock()

	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func (r *Runtime) SessionsSnapshot() map[string]Session {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]Session, len(r.sessions))
	for id, session := range r.sessions {
		out[id] = session
	}
	return out
}

func (r *Runtime) historyGeneration(ctx context.Context, agentID string) int64 {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return 1
	}
	r.historyMu.Lock()
	if gen, ok := r.historyGenerationByAgent[agentID]; ok && gen > 0 {
		r.historyMu.Unlock()
		return gen
	}
	r.historyMu.Unlock()

	gen := int64(1)
	cutoff := time.Time{}
	if r.Bus != nil {
		summaries, err := r.Bus.List(ctx, "history", eventbus.ListOptions{
			ScopeType: "agent",
			ScopeID:   agentID,
			Limit:     300,
			Order:     "lifo",
		})
		if err == nil && len(summaries) > 0 {
			ids := make([]string, 0, len(summaries))
			for _, item := range summaries {
				ids = append(ids, item.ID)
			}
			events, err := r.Bus.Read(ctx, "history", ids, "")
			if err == nil {
				for _, evt := range events {
					entry, ok := HistoryEntryFromEvent(evt)
					if !ok {
						continue
					}
					if entry.Generation > gen {
						gen = entry.Generation
						cutoff = time.Time{}
					}
					if entry.Type == "context_compaction" && entry.Generation == gen && entry.CreatedAt.After(cutoff) {
						cutoff = entry.CreatedAt
					}
				}
			}
		}
	}

	r.historyMu.Lock()
	if existing, ok := r.historyGenerationByAgent[agentID]; ok && existing > gen {
		gen = existing
	}
	r.historyGenerationByAgent[agentID] = gen
	if !cutoff.IsZero() {
		if prev, ok := r.historyCompactionCutoff[agentID]; !ok || cutoff.After(prev) {
			r.historyCompactionCutoff[agentID] = cutoff
		}
	}
	r.historyMu.Unlock()
	return gen
}

func (r *Runtime) currentHistoryGeneration(ctx context.Context, agentID string) int64 {
	return r.historyGeneration(ctx, agentID)
}

func (r *Runtime) shouldAppendGenerationPreamble(ctx context.Context, agentID string, generation int64) bool {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return false
	}
	if generation <= 0 {
		generation = r.historyGeneration(ctx, agentID)
	}

	r.historyMu.Lock()
	if existing, ok := r.historyPreambleByAgent[agentID]; ok && existing == generation {
		r.historyMu.Unlock()
		return false
	}
	r.historyMu.Unlock()

	alreadyExists := false
	if r.Bus != nil {
		summaries, err := r.Bus.List(ctx, "history", eventbus.ListOptions{
			ScopeType: "agent",
			ScopeID:   agentID,
			Limit:     200,
			Order:     "lifo",
		})
		if err == nil && len(summaries) > 0 {
			ids := make([]string, 0, len(summaries))
			for _, item := range summaries {
				ids = append(ids, item.ID)
			}
			events, err := r.Bus.Read(ctx, "history", ids, "")
			if err == nil {
				for _, evt := range events {
					entry, ok := HistoryEntryFromEvent(evt)
					if !ok || entry.Generation != generation {
						continue
					}
					if entry.Type == "tools_config" || entry.Type == "system_prompt" {
						alreadyExists = true
						break
					}
				}
			}
		}
	}

	r.historyMu.Lock()
	r.historyPreambleByAgent[agentID] = generation
	r.historyMu.Unlock()
	return !alreadyExists
}

func (r *Runtime) compactionCutoff(agentID string) time.Time {
	r.historyMu.Lock()
	defer r.historyMu.Unlock()
	return r.historyCompactionCutoff[agentID]
}

func (r *Runtime) CompactAgentContext(ctx context.Context, agentID, reason string) (int64, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return 0, fmt.Errorf("agent_id is required")
	}
	now := time.Now().UTC()
	current := r.historyGeneration(ctx, agentID)
	next := current + 1

	r.historyMu.Lock()
	r.historyGenerationByAgent[agentID] = next
	r.historyCompactionCutoff[agentID] = now
	r.historyMu.Unlock()

	state := r.ensureAgentState(agentID)
	if state != nil {
		state.mu.Lock()
		state.RootTaskID = ""
		state.mu.Unlock()
	}
	r.rememberAgent(agentID)

	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "context compaction requested"
	}
	r.appendHistory(ctx, agentID, "context_compaction", "system", reason, "", next, map[string]any{
		"compacted_at": now.Format(time.RFC3339Nano),
		"previous_gen": current,
		"next_gen":     next,
	})
	return next, nil
}

func HistoryEntryFromEvent(evt eventbus.Event) (AgentHistoryEntry, bool) {
	if evt.Stream != "history" {
		return AgentHistoryEntry{}, false
	}
	entry := AgentHistoryEntry{
		ID:        evt.ID,
		CreatedAt: evt.CreatedAt,
	}

	agentID := ""
	if evt.Payload != nil {
		agentID = mapString(evt.Payload, "agent_id")
	}
	if agentID == "" && evt.Metadata != nil {
		agentID = mapString(evt.Metadata, "agent_id")
	}
	if agentID == "" {
		agentID = evt.ScopeID
	}
	entry.AgentID = strings.TrimSpace(agentID)
	if entry.AgentID == "" {
		return AgentHistoryEntry{}, false
	}

	entry.Generation = historyGenerationFromMaps(evt.Payload, evt.Metadata)
	if entry.Generation <= 0 {
		entry.Generation = 1
	}
	entry.Type = mapString(evt.Payload, "type")
	if entry.Type == "" {
		entry.Type = mapString(evt.Metadata, "type")
	}
	if entry.Type == "" {
		entry.Type = "note"
	}
	entry.Role = mapString(evt.Payload, "role")
	if entry.Role == "" {
		entry.Role = mapString(evt.Metadata, "role")
	}
	if entry.Role == "" {
		entry.Role = "system"
	}
	content, hasContent := mapContentString(evt.Payload, "content")
	entry.Content = content
	if !hasContent {
		entry.Content = evt.Body
	}
	entry.TaskID = mapString(evt.Payload, "task_id")
	entry.ToolCallID = mapString(evt.Payload, "tool_call_id")
	entry.ToolName = mapString(evt.Payload, "tool_name")
	entry.ToolStatus = mapString(evt.Payload, "tool_status")

	data := map[string]any{}
	for k, v := range evt.Payload {
		switch k {
		case "agent_id", "generation", "type", "role", "content", "task_id", "created_at":
			continue
		default:
			data[k] = v
		}
	}
	if len(data) > 0 {
		entry.Data = data
	}
	return entry, true
}

func historyGenerationFromMaps(maps ...map[string]any) int64 {
	for _, m := range maps {
		if m == nil {
			continue
		}
		if val, ok := m["generation"]; ok {
			if parsed := anyToInt64(val); parsed > 0 {
				return parsed
			}
		}
	}
	return 0
}

func mapString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	val, ok := m[key]
	if !ok || val == nil {
		return ""
	}
	switch v := val.(type) {
	case string:
		return strings.TrimSpace(v)
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", v))
	}
}

func mapContentString(m map[string]any, key string) (string, bool) {
	if m == nil {
		return "", false
	}
	val, ok := m[key]
	if !ok || val == nil {
		return "", false
	}
	switch v := val.(type) {
	case string:
		return v, true
	default:
		return fmt.Sprintf("%v", v), true
	}
}

func anyToInt64(v any) int64 {
	switch n := v.(type) {
	case int:
		return int64(n)
	case int64:
		return n
	case int32:
		return int64(n)
	case float64:
		return int64(n)
	case float32:
		return int64(n)
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64)
		if err != nil {
			return 0
		}
		return parsed
	default:
		return 0
	}
}
