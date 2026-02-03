package api

import "net/http"

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeError(w, http.StatusInternalServerError, errNotFound("store"))
		return
	}
	switch r.Method {
	case http.MethodGet:
		limit := parseInt(r.URL.Query().Get("limit"), 50)
		agents, err := s.Store.ListAgents(r.Context(), limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, agents)
	case http.MethodPost:
		var payload struct {
			Profile string `json:"profile"`
			Status  string `json:"status"`
		}
		if err := decodeJSON(r.Body, &payload); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		agent, err := s.Store.CreateAgent(r.Context(), payload.Profile, payload.Status)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusCreated, agent)
	default:
		writeMethodNotAllowed(w)
	}
}

func (s *Server) handleActions(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeError(w, http.StatusInternalServerError, errNotFound("store"))
		return
	}
	switch r.Method {
	case http.MethodGet:
		limit := parseInt(r.URL.Query().Get("limit"), 100)
		items, err := s.Store.ListActions(r.Context(), limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	case http.MethodPost:
		var payload struct {
			AgentID  string         `json:"agent_id"`
			Content  string         `json:"content"`
			Status   string         `json:"status"`
			Metadata map[string]any `json:"metadata"`
		}
		if err := decodeJSON(r.Body, &payload); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		action, err := s.Store.CreateAction(r.Context(), payload.AgentID, payload.Content, payload.Status, payload.Metadata)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusCreated, action)
	default:
		writeMethodNotAllowed(w)
	}
}
