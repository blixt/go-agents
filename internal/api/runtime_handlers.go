package api

import (
	"net/http"
	"strings"

	"github.com/oklog/ulid/v2"
)

func (s *Server) handleAgentItem(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/agents/")
	segments := strings.Split(strings.Trim(path, "/"), "/")
	if len(segments) < 2 {
		writeError(w, http.StatusNotFound, errNotFound("agent action"))
		return
	}
	agentID := segments[0]
	action := segments[1]
	if action != "run" && action != "compact" {
		writeError(w, http.StatusNotFound, errNotFound("agent action"))
		return
	}
	if s.Runtime == nil {
		writeError(w, http.StatusInternalServerError, errNotFound("runtime"))
		return
	}
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}

	if action == "compact" {
		var payload struct {
			Reason string `json:"reason"`
		}
		_ = decodeJSON(r.Body, &payload)
		generation, err := s.Runtime.CompactAgentContext(r.Context(), agentID, payload.Reason)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{
			"status":     "compacted",
			"agent_id":   agentID,
			"generation": generation,
		})
		return
	}

	var payload struct {
		Message   string `json:"message"`
		Source    string `json:"source"`
		System    string `json:"system"`
		Model     string `json:"model"`
		RequestID string `json:"request_id"`
		Priority  string `json:"priority"`
	}
	if err := decodeJSON(r.Body, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	source := strings.TrimSpace(payload.Source)
	if source == "" {
		source = "external"
	}
	requestID := strings.TrimSpace(payload.RequestID)
	if requestID == "" {
		requestID = ulid.Make().String()
	}
	priority := normalizePriority(payload.Priority)
	if payload.System != "" {
		s.Runtime.SetAgentSystem(agentID, payload.System)
	}
	if payload.Model != "" {
		s.Runtime.SetAgentModel(agentID, payload.Model)
	}
	s.Runtime.EnsureAgentLoop(agentID)
	rootTask, _ := s.Runtime.EnsureRootTask(r.Context(), agentID)
	evt, err := s.Runtime.SendMessageWithMeta(r.Context(), agentID, payload.Message, source, map[string]any{
		"priority":   priority,
		"request_id": requestID,
		"kind":       "input",
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":     "queued",
		"agent_id":   agentID,
		"event_id":   evt.ID,
		"recipient":  agentID,
		"task_id":    rootTask.ID,
		"request_id": requestID,
		"priority":   priority,
	})
}

func normalizePriority(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "interrupt", "wake", "normal", "low":
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return "wake"
	}
}
