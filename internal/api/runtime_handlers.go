package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/flitsinc/go-agents/internal/engine"
	"github.com/flitsinc/go-agents/internal/idgen"
	"github.com/flitsinc/go-agents/internal/tasks"
)

// applyAgentConfig sets system prompt and model on a runtime from the payload.
func applyAgentConfig(rt *engine.Runtime, taskID string, payload map[string]any) {
	if rt == nil || payload == nil {
		return
	}
	if system, ok := payload["system"].(string); ok && system != "" {
		rt.SetAgentSystem(taskID, system)
	}
	if model, ok := payload["model"].(string); ok && model != "" {
		rt.SetAgentModel(taskID, model)
	}
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	var payload struct {
		ID      string         `json:"id"`
		Type    string         `json:"type"`
		Payload map[string]any `json:"payload"`
		Source  string         `json:"source"`
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
		writeError(w, http.StatusBadRequest, fmt.Errorf("type is required"))
		return
	}
	source := strings.TrimSpace(payload.Source)

	if s.Tasks == nil {
		writeError(w, http.StatusInternalServerError, errNotFound("task manager"))
		return
	}

	// Upsert: if a custom ID is provided and the task already exists,
	// re-apply config and ensure the loop is running.
	if customID != "" {
		if existing, err := s.Tasks.Get(r.Context(), customID); err == nil && existing.ID != "" {
			if taskType == "agent" && s.Runtime != nil {
				applyAgentConfig(s.Runtime, existing.ID, payload.Payload)
				s.Runtime.EnsureAgentLoop(existing.ID)
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"task_id": existing.ID,
				"status":  string(existing.Status),
				"type":    existing.Type,
				"created": false,
			})
			return
		}
	}

	spec := tasks.Spec{
		ID:   customID,
		Type: taskType,
		Mode: "async",
		Metadata: map[string]any{
			"source": source,
		},
	}
	if payload.Payload != nil {
		spec.Payload = payload.Payload
	}

	created, err := s.Tasks.Spawn(r.Context(), spec)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	// For agent tasks, set up the runtime loop
	if taskType == "agent" && s.Runtime != nil {
		_ = s.Tasks.MarkRunning(r.Context(), created.ID)
		applyAgentConfig(s.Runtime, created.ID, payload.Payload)
		s.Runtime.EnsureAgentLoop(created.ID)
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"task_id": created.ID,
		"status":  string(created.Status),
		"type":    created.Type,
		"created": true,
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
