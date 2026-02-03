package api

import (
	"net/http"

	agentctx "github.com/flitsinc/go-agents/internal/prompt"
)

func (s *Server) handlePrompt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"system_prompt": agentctx.DefaultSystemPrompt,
	})
}
