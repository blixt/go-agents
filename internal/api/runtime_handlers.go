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
	}
	if err := decodeJSON(r.Body, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	source := strings.TrimSpace(payload.Source)
	if source == "" {
		source = "human"
	}
	s.Runtime.EnsureAgentLoop(agentID)
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
	})
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	if s.Runtime == nil {
		writeError(w, http.StatusInternalServerError, errNotFound("runtime"))
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	agentID := strings.Trim(path, "/")
	if agentID == "" {
		writeError(w, http.StatusNotFound, errNotFound("session"))
		return
	}
	session, ok := s.Runtime.GetSession(agentID)
	if !ok {
		var err error
		session, err = s.Runtime.BuildSession(r.Context(), agentID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, session)
}
