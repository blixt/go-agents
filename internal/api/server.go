package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/flitsinc/go-agents/internal/engine"
	"github.com/flitsinc/go-agents/internal/eventbus"
	"github.com/flitsinc/go-agents/internal/idgen"
	"github.com/flitsinc/go-agents/internal/schema"
	"github.com/flitsinc/go-agents/internal/tasks"
)

type Server struct {
	Tasks   *tasks.Manager
	Bus     *eventbus.Bus
	Runtime *engine.Runtime
	NowFn   func() time.Time
}

func (s *Server) now() time.Time {
	if s == nil || s.NowFn == nil {
		return time.Now().UTC()
	}
	return s.NowFn().UTC()
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/tasks/queue", s.handleTaskQueue)
	mux.HandleFunc("/api/tasks/", s.handleTaskItem)
	mux.HandleFunc("/api/tasks", s.handleCreateTask)
	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/api/streams/subscribe", s.handleStreamSubscribe)

	return mux
}

func (s *Server) handleTaskQueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	kind := r.URL.Query().Get("type")
	limit := parseInt(r.URL.Query().Get("limit"), 1)
	items, err := s.Tasks.ClaimQueued(r.Context(), kind, limit)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleTaskItem(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/tasks/")
	segments := strings.Split(strings.Trim(path, "/"), "/")
	if len(segments) == 0 || segments[0] == "" {
		writeError(w, http.StatusNotFound, errNotFound("task"))
		return
	}
	taskID := segments[0]
	if len(segments) == 1 {
		writeMethodNotAllowed(w)
		return
	}

	action := segments[1]
	switch action {
	case "updates":
		s.handleTaskUpdates(w, r, taskID)
	case "complete":
		s.handleTaskComplete(w, r, taskID)
	case "fail":
		s.handleTaskFail(w, r, taskID)
	case "send":
		s.handleTaskSend(w, r, taskID)
	case "cancel":
		s.handleTaskCancel(w, r, taskID)
	case "kill":
		s.handleTaskKill(w, r, taskID)
	case "compact":
		s.handleTaskCompact(w, r, taskID)
	default:
		writeError(w, http.StatusNotFound, errNotFound("task action"))
	}
}

func (s *Server) handleTaskUpdates(w http.ResponseWriter, r *http.Request, taskID string) {
	switch r.Method {
	case http.MethodGet:
		kind := r.URL.Query().Get("kind")
		afterID := r.URL.Query().Get("after_id")
		limit := parseInt(r.URL.Query().Get("limit"), 200)
		updates, err := s.Tasks.ListUpdatesSince(r.Context(), taskID, afterID, kind, limit)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, updates)
	case http.MethodPost:
		var payload struct {
			Kind    string         `json:"kind"`
			Payload map[string]any `json:"payload"`
		}
		if err := decodeJSON(r.Body, &payload); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err := s.Tasks.RecordUpdate(r.Context(), taskID, payload.Kind, payload.Payload); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeMethodNotAllowed(w)
	}
}

func (s *Server) handleTaskComplete(w http.ResponseWriter, r *http.Request, taskID string) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	var payload struct {
		Result map[string]any `json:"result"`
	}
	if err := decodeJSON(r.Body, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.Tasks.Complete(r.Context(), taskID, payload.Result); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleTaskFail(w http.ResponseWriter, r *http.Request, taskID string) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	var payload struct {
		Error string `json:"error"`
	}
	if err := decodeJSON(r.Body, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.Tasks.Fail(r.Context(), taskID, payload.Error); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleTaskSend(w http.ResponseWriter, r *http.Request, taskID string) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	var payload struct {
		// Agent message fields
		Message   string         `json:"message"`
		Source    string         `json:"source"`
		Priority  string         `json:"priority"`
		RequestID string         `json:"request_id"`
		Context   map[string]any `json:"context"`
		// Generic task input
		Input map[string]any `json:"input"`
	}
	if err := decodeJSON(r.Body, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	// If a message is provided and we have a runtime, deliver it as an agent message.
	message := strings.TrimSpace(payload.Message)
	if message != "" && s.Runtime != nil {
		// Verify the task exists before delivering. No auto-creation.
		if _, err := s.Tasks.Get(r.Context(), taskID); err != nil {
			writeError(w, http.StatusNotFound, errNotFound("task"))
			return
		}
		source := strings.TrimSpace(payload.Source)
		if source == "" {
			source = "external"
		}
		priority := normalizePriority(payload.Priority)
		s.Runtime.EnsureAgentLoop(taskID)
		requestID := strings.TrimSpace(payload.RequestID)
		if requestID == "" {
			requestID = idgen.New()
		}
		meta := map[string]any{
			"priority":   priority,
			"request_id": requestID,
			"kind":       "message",
		}
		if len(payload.Context) > 0 {
			meta["context"] = payload.Context
		}
		_, err := s.Runtime.SendMessageWithMeta(r.Context(), taskID, message, source, meta)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}

	// Fall back to generic task send.
	if err := s.Tasks.Send(r.Context(), taskID, payload.Input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleTaskCancel(w http.ResponseWriter, r *http.Request, taskID string) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	var payload struct {
		Reason string `json:"reason"`
	}
	_ = decodeJSON(r.Body, &payload)
	if err := s.Tasks.Cancel(r.Context(), taskID, payload.Reason); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleTaskKill(w http.ResponseWriter, r *http.Request, taskID string) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	var payload struct {
		Reason string `json:"reason"`
	}
	_ = decodeJSON(r.Body, &payload)
	if err := s.Tasks.Kill(r.Context(), taskID, payload.Reason); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleStreamSubscribe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	streamsParam := r.URL.Query().Get("streams")
	if streamsParam == "" {
		streamsParam = schema.StreamHistory + "," + schema.StreamTaskOutput + "," + schema.StreamErrors
	}
	streamList := splitComma(streamsParam)

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, errNotFound("streaming support"))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	_, _ = w.Write([]byte(":ok\n\n"))
	flusher.Flush()

	ctx := r.Context()
	sub := s.Bus.Subscribe(ctx, streamList)
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			_, _ = w.Write([]byte(":keepalive\n\n"))
			flusher.Flush()
		case evt, ok := <-sub:
			if !ok {
				return
			}
			payload, _ := json.Marshal(evt)
			_, _ = w.Write([]byte("data: "))
			_, _ = w.Write(payload)
			_, _ = w.Write([]byte("\n\n"))
			flusher.Flush()
		}
	}
}

func decodeJSON(body io.Reader, dest any) error {
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	return dec.Decode(dest)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{"error": err.Error()})
}

func writeMethodNotAllowed(w http.ResponseWriter) {
	writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
}

func parseInt(value string, fallback int) int {
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func splitComma(value string) []string {
	parts := strings.Split(value, ",")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

type notFoundError struct {
	msg string
}

func (e notFoundError) Error() string { return e.msg }

func errNotFound(target string) error {
	return notFoundError{msg: target + " not found"}
}
