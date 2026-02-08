package api

import (
	"net/http"
	"strings"

	"github.com/flitsinc/go-agents/internal/idgen"
	"github.com/flitsinc/go-agents/internal/schema"
	"github.com/flitsinc/go-agents/internal/tasks"
)

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	var payload struct {
		ID       string         `json:"id"`
		Type     string         `json:"type"`
		Name     string         `json:"name"`
		Payload  map[string]any `json:"payload"`
		Source   string         `json:"source"`
		Priority string         `json:"priority"`
	}
	if err := decodeJSON(r.Body, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	customID := strings.TrimSpace(payload.ID)
	if customID != "" {
		if err := idgen.ValidateCustomID(customID); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	taskType := strings.TrimSpace(payload.Type)
	if taskType == "" {
		taskType = "agent"
	}
	source := strings.TrimSpace(payload.Source)
	if source == "" {
		source = "external"
	}
	priority := normalizePriority(payload.Priority)

	spec := tasks.Spec{
		ID:   customID,
		Type: taskType,
		Name: strings.TrimSpace(payload.Name),
		Mode: "async",
		Metadata: map[string]any{
			"source":   source,
			"priority": priority,
		},
	}
	if payload.Payload != nil {
		spec.Payload = payload.Payload
	}

	if s.Tasks == nil {
		writeError(w, http.StatusInternalServerError, errNotFound("task manager"))
		return
	}
	created, err := s.Tasks.Spawn(r.Context(), spec)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	// For agent tasks, set up the runtime loop
	if taskType == "agent" && s.Runtime != nil {
		_ = s.Tasks.MarkRunning(r.Context(), created.ID)
		// Set task-level config from payload
		if payload.Payload != nil {
			if system, ok := payload.Payload["system"].(string); ok && system != "" {
				s.Runtime.SetAgentSystem(created.ID, system)
			}
			if model, ok := payload.Payload["model"].(string); ok && model != "" {
				s.Runtime.SetAgentModel(created.ID, model)
			}
		}
		// Update metadata with routing targets
		spec.Metadata["input_target"] = created.ID
		spec.Metadata["notify_target"] = created.ID
		spec.Owner = created.ID

		s.Runtime.EnsureAgentLoop(created.ID)

		// Deliver initial message if present
		if payload.Payload != nil {
			if message, ok := payload.Payload["message"].(string); ok && strings.TrimSpace(message) != "" {
				requestID := idgen.New()
				_, _ = s.Runtime.SendMessageWithMeta(r.Context(), created.ID, message, source, map[string]any{
					"priority":   priority,
					"request_id": requestID,
					"kind":       "message",
				})
			}
		}
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"task_id": created.ID,
		"status":  string(created.Status),
		"type":    created.Type,
	})
}

func (s *Server) handleTaskCompact(w http.ResponseWriter, r *http.Request, taskID string) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if s.Runtime == nil {
		writeError(w, http.StatusInternalServerError, errNotFound("runtime"))
		return
	}
	var payload struct {
		Reason string `json:"reason"`
	}
	_ = decodeJSON(r.Body, &payload)
	generation, err := s.Runtime.CompactAgentContext(r.Context(), taskID, payload.Reason)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":     "compacted",
		"task_id":    taskID,
		"generation": generation,
	})
}

func normalizePriority(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return string(schema.PriorityWake)
	}
	return string(schema.ParsePriority(raw))
}
