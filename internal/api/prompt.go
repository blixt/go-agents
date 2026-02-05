package api

import (
	"context"
	"net/http"
	"time"
)

func (s *Server) handlePrompt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	if s.Runtime == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": "runtime unavailable",
		})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	promptText, err := s.Runtime.BuildPrompt(ctx, "operator")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"system_prompt": promptText,
	})
}
