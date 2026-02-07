package api

import (
	"net/http"
	"strings"

	"github.com/flitsinc/go-agents/internal/idgen"
)

func (s *Server) handleAgentItem(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/agents/")
	segments := strings.Split(strings.Trim(path, "/"), "/")
	if len(segments) == 0 || segments[0] == "" {
		writeError(w, http.StatusNotFound, errNotFound("agent action"))
		return
	}
	requestedAgentID := ""
	action := ""
	switch len(segments) {
	case 1:
		action = strings.TrimSpace(segments[0])
	case 2:
		requestedAgentID = strings.TrimSpace(segments[0])
		action = strings.TrimSpace(segments[1])
	default:
		writeError(w, http.StatusNotFound, errNotFound("agent action"))
		return
	}
	if action != "run" && action != "compact" {
		writeError(w, http.StatusNotFound, errNotFound("agent action"))
		return
	}
	if action == "compact" && requestedAgentID == "" {
		writeError(w, http.StatusNotFound, errNotFound("agent"))
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
		generation, err := s.Runtime.CompactAgentContext(r.Context(), requestedAgentID, payload.Reason)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{
			"status":     "compacted",
			"agent_id":   requestedAgentID,
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
	agentID := s.Runtime.ResolveAgentID(r.Context(), requestedAgentID)
	requestID := strings.TrimSpace(payload.RequestID)
	if requestID == "" {
		requestID = idgen.New()
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
		"kind":       "message",
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":             "queued",
		"agent_id":           agentID,
		"requested_agent_id": requestedAgentID,
		"event_id":           evt.ID,
		"recipient":          agentID,
		"task_id":            rootTask.ID,
		"request_id":         requestID,
		"priority":           priority,
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
