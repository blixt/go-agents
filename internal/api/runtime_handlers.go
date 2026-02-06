package api

import (
	"net/http"
	"strings"
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
	if action != "run" {
		writeError(w, http.StatusNotFound, errNotFound("agent action"))
		return
	}
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if s.Runtime == nil {
		writeError(w, http.StatusInternalServerError, errNotFound("runtime"))
		return
	}
	var payload struct {
		Message string `json:"message"`
		Source  string `json:"source"`
		System  string `json:"system"`
		Model   string `json:"model"`
	}
	if err := decodeJSON(r.Body, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	source := strings.TrimSpace(payload.Source)
	if source == "" {
		source = "human"
	}
	if payload.System != "" {
		s.Runtime.SetAgentSystem(agentID, payload.System)
	}
	if payload.Model != "" {
		s.Runtime.SetAgentModel(agentID, payload.Model)
	}
	s.Runtime.EnsureAgentLoop(agentID)
	rootTask, _ := s.Runtime.EnsureRootTask(r.Context(), agentID)
	evt, err := s.Runtime.SendMessage(r.Context(), agentID, payload.Message, source)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":    "queued",
		"agent_id":  agentID,
		"event_id":  evt.ID,
		"recipient": agentID,
		"task_id":   rootTask.ID,
	})
}
