package engine

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/flitsinc/go-agents/internal/eventbus"
	"github.com/flitsinc/go-llms/content"
	"github.com/flitsinc/go-llms/llms"
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

func (r *Runtime) appendHistory(ctx context.Context, taskID, entryType, role, content, llmTaskID string, generation int64, data map[string]any) {
	if r.Bus == nil {
		return
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
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
		generation = r.historyGeneration(ctx, taskID)
	}

	body := strings.TrimSpace(content)
	if body == "" {
		body = entryType
	}
	payload := map[string]any{
		"agent_id":   taskID,
		"generation": generation,
		"type":       entryType,
		"role":       role,
		"content":    content,
		"task_id":    strings.TrimSpace(llmTaskID),
		"created_at": r.now().Format(time.RFC3339Nano),
	}
	for k, v := range data {
		if v == nil {
			continue
		}
		payload[k] = v
	}
	_, _ = r.Bus.Push(ctx, eventbus.EventInput{
		Stream:    "history",
		ScopeType: "task",
		ScopeID:   taskID,
		Subject:   fmt.Sprintf("%s:%s", role, entryType),
		Body:      body,
		Metadata: map[string]any{
			"kind":       "history_entry",
			"agent_id":   taskID,
			"generation": generation,
			"type":       entryType,
			"role":       role,
			"priority":   "low",
		},
		Payload: payload,
	})
}

func (r *Runtime) appendToolHistory(ctx context.Context, taskID, llmTaskID, entryType, toolCallID, toolName, toolStatus, content string, data map[string]any) {
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
	r.appendHistory(ctx, taskID, entryType, "tool", content, llmTaskID, 0, data)
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

func (r *Runtime) historyGeneration(ctx context.Context, taskID string) int64 {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return 1
	}
	r.historyMu.Lock()
	if gen, ok := r.historyGenerationByTask[taskID]; ok && gen > 0 {
		r.historyMu.Unlock()
		return gen
	}
	r.historyMu.Unlock()

	gen := int64(1)
	if r.Bus != nil {
		summaries, err := r.Bus.List(ctx, "history", eventbus.ListOptions{
			ScopeType: "task",
			ScopeID:   taskID,
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
					}
				}
			}
		}
	}

	r.historyMu.Lock()
	if existing, ok := r.historyGenerationByTask[taskID]; ok && existing > gen {
		gen = existing
	}
	r.historyGenerationByTask[taskID] = gen
	r.historyMu.Unlock()
	return gen
}


func (r *Runtime) shouldAppendGenerationPreamble(ctx context.Context, taskID string, generation int64) bool {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return false
	}
	if generation <= 0 {
		generation = r.historyGeneration(ctx, taskID)
	}

	r.historyMu.Lock()
	if existing, ok := r.historyPreambleByTask[taskID]; ok && existing == generation {
		r.historyMu.Unlock()
		return false
	}
	r.historyMu.Unlock()

	alreadyExists := false
	if r.Bus != nil {
		summaries, err := r.Bus.List(ctx, "history", eventbus.ListOptions{
			ScopeType: "task",
			ScopeID:   taskID,
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
	r.historyPreambleByTask[taskID] = generation
	r.historyMu.Unlock()
	return !alreadyExists
}


func (r *Runtime) CompactAgentContext(ctx context.Context, taskID, reason string) (int64, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return 0, fmt.Errorf("task_id is required")
	}
	now := r.now()
	current := r.historyGeneration(ctx, taskID)
	next := current + 1

	r.historyMu.Lock()
	r.historyGenerationByTask[taskID] = next
	r.historyMu.Unlock()

	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "context compaction requested"
	}
	r.appendHistory(ctx, taskID, "context_compaction", "system", reason, "", next, map[string]any{
		"compacted_at": now.Format(time.RFC3339Nano),
		"previous_gen": current,
		"next_gen":     next,
	})
	return next, nil
}

// loadConversationMessages reads history entries for the given agent and
// generation, returning the stored system prompt text and a reconstructed
// []llms.Message conversation suitable for passing to ChatUsingMessages.
// Consecutive messages with the same role are merged.
func (r *Runtime) loadConversationMessages(ctx context.Context, agentID string, generation int64) (storedPrompt string, messages []llms.Message, err error) {
	if r.Bus == nil {
		return "", nil, nil
	}
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return "", nil, nil
	}

	summaries, err := r.Bus.List(ctx, "history", eventbus.ListOptions{
		ScopeType: "task",
		ScopeID:   agentID,
		Limit:     2000,
		Order:     "fifo",
	})
	if err != nil || len(summaries) == 0 {
		return "", nil, err
	}

	ids := make([]string, len(summaries))
	for i, s := range summaries {
		ids[i] = s.ID
	}
	events, err := r.Bus.Read(ctx, "history", ids, "")
	if err != nil {
		return "", nil, err
	}

	// Accumulate user/assistant messages, merging consecutive same-role entries.
	var lastRole string
	var lastText string
	flush := func() {
		if lastRole == "" || strings.TrimSpace(lastText) == "" {
			return
		}
		messages = append(messages, llms.Message{
			Role:    lastRole,
			Content: content.FromText(lastText),
		})
	}
	for _, evt := range events {
		entry, ok := HistoryEntryFromEvent(evt)
		if !ok || entry.Generation != generation {
			continue
		}
		switch entry.Type {
		case "system_prompt":
			if storedPrompt == "" {
				storedPrompt = entry.Content
			}
		case "user_message":
			text := strings.TrimSpace(entry.Content)
			if text == "" {
				continue
			}
			if lastRole == "user" {
				lastText += "\n\n" + text
			} else {
				flush()
				lastRole = "user"
				lastText = text
			}
		case "assistant_message":
			text := strings.TrimSpace(entry.Content)
			if text == "" {
				continue
			}
			if lastRole == "assistant" {
				lastText += "\n\n" + text
			} else {
				flush()
				lastRole = "assistant"
				lastText = text
			}
		}
	}
	flush()

	// Drop trailing user message (indicates a failed turn with no response).
	if len(messages) > 0 && messages[len(messages)-1].Role == "user" {
		messages = messages[:len(messages)-1]
	}

	return storedPrompt, messages, nil
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
