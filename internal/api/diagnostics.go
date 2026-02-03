package api

import (
	"net/http"
	"runtime"
	"time"
)

type DiagnosticsInfo struct {
	HTTPAddr    string `json:"http_addr"`
	DataDir     string `json:"data_dir"`
	DBPath      string `json:"db_path"`
	SnapshotDir string `json:"snapshot_dir"`
	WebDir      string `json:"web_dir"`
	LLMProvider string `json:"llm_provider"`
	LLMModel    string `json:"llm_model"`
}

type DiagnosticsResponse struct {
	Time          time.Time       `json:"time"`
	StartedAt     time.Time       `json:"started_at"`
	UptimeSeconds int64           `json:"uptime_seconds"`
	GoVersion     string          `json:"go_version"`
	LLMConfigured bool            `json:"llm_configured"`
	Info          DiagnosticsInfo `json:"info"`
	EventBus      map[string]any  `json:"eventbus"`
	Runtime       map[string]any  `json:"runtime"`
}

func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	now := time.Now().UTC()
	started := s.StartedAt
	if started.IsZero() {
		started = now
	}
	resp := DiagnosticsResponse{
		Time:          now,
		StartedAt:     started,
		UptimeSeconds: int64(now.Sub(started).Seconds()),
		GoVersion:     runtime.Version(),
		LLMConfigured: s.Info.LLMProvider != "" && s.Info.LLMModel != "" && s.Runtime != nil && s.Runtime.LLM != nil && s.Runtime.LLM.LLM != nil,
		Info:          s.Info,
		EventBus:      map[string]any{},
		Runtime:       map[string]any{},
	}
	if s.Bus != nil {
		resp.EventBus["subscribers"] = s.Bus.SubscriberCount()
	}
	if s.Runtime != nil {
		resp.Runtime["sessions"] = s.Runtime.SessionCount()
	}
	writeJSON(w, http.StatusOK, resp)
}
