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
	"github.com/flitsinc/go-agents/internal/state"
	"github.com/flitsinc/go-agents/internal/tasks"
)

type Server struct {
	Tasks        *tasks.Manager
	Bus          *eventbus.Bus
	Store        *state.Store
	Runtime      *engine.Runtime
	Restart      func() error
	RestartToken string
	StartedAt    time.Time
	Info         DiagnosticsInfo
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/health", s.handleHealth)
	mux.HandleFunc("/api/tasks", s.handleTasks)
	mux.HandleFunc("/api/tasks/queue", s.handleTaskQueue)
	mux.HandleFunc("/api/tasks/", s.handleTaskItem)
	mux.HandleFunc("/api/events", s.handleEvents)
	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/api/prompt", s.handlePrompt)
	mux.HandleFunc("/api/agents", s.handleAgents)
	mux.HandleFunc("/api/agents/", s.handleAgentItem)
	mux.HandleFunc("/api/actions", s.handleActions)
	mux.HandleFunc("/api/sessions/", s.handleSessions)
	mux.HandleFunc("/api/streams/subscribe", s.handleStreamSubscribe)
	mux.HandleFunc("/api/streams/ws", s.handleStreamWS)
	mux.HandleFunc("/api/streams/", s.handleStreams)
	mux.HandleFunc("/api/admin/restart", s.handleRestart)
	mux.HandleFunc("/api/diagnostics", s.handleDiagnostics)

	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "time": time.Now().UTC()})
}

func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		status := r.URL.Query().Get("status")
		typeFilter := r.URL.Query().Get("type")
		owner := r.URL.Query().Get("owner")
		limit := parseInt(r.URL.Query().Get("limit"), 100)
		items, err := s.Tasks.List(r.Context(), tasks.ListFilter{
			Type:   typeFilter,
			Status: tasks.Status(status),
			Owner:  owner,
			Limit:  limit,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	case http.MethodPost:
		var spec tasks.Spec
		if err := decodeJSON(r.Body, &spec); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		task, err := s.Tasks.Spawn(r.Context(), spec)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusCreated, task)
	default:
		writeMethodNotAllowed(w)
	}
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
		switch r.Method {
		case http.MethodGet:
			task, err := s.Tasks.Get(r.Context(), taskID)
			if err != nil {
				writeError(w, http.StatusNotFound, err)
				return
			}
			writeJSON(w, http.StatusOK, task)
		default:
			writeMethodNotAllowed(w)
		}
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
	default:
		writeError(w, http.StatusNotFound, errNotFound("task action"))
	}
}

func (s *Server) handleTaskUpdates(w http.ResponseWriter, r *http.Request, taskID string) {
	switch r.Method {
	case http.MethodGet:
		limit := parseInt(r.URL.Query().Get("limit"), 200)
		updates, err := s.Tasks.ListUpdates(r.Context(), taskID, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
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
		Input map[string]any `json:"input"`
	}
	if err := decodeJSON(r.Body, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
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

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	var payload struct {
		Stream   string         `json:"stream"`
		Subject  string         `json:"subject"`
		Body     string         `json:"body"`
		Metadata map[string]any `json:"metadata"`
		Payload  map[string]any `json:"payload"`
	}
	if err := decodeJSON(r.Body, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	evt, err := s.Bus.Push(r.Context(), eventbus.EventInput{
		Stream:   payload.Stream,
		Subject:  payload.Subject,
		Body:     payload.Body,
		Metadata: payload.Metadata,
		Payload:  payload.Payload,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, evt)
}

func (s *Server) handleStreams(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/streams/")
	segments := strings.Split(strings.Trim(path, "/"), "/")
	if len(segments) == 0 || segments[0] == "" {
		writeError(w, http.StatusNotFound, errNotFound("stream"))
		return
	}
	stream := segments[0]
	if len(segments) == 1 {
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w)
			return
		}
		limit := parseInt(r.URL.Query().Get("limit"), 50)
		order := r.URL.Query().Get("order")
		reader := r.URL.Query().Get("reader")
		items, err := s.Bus.List(r.Context(), stream, eventbus.ListOptions{
			Limit:  limit,
			Order:  order,
			Reader: reader,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
		return
	}
	action := segments[1]
	switch action {
	case "read":
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w)
			return
		}
		var payload struct {
			IDs    []string `json:"ids"`
			Reader string   `json:"reader"`
		}
		if err := decodeJSON(r.Body, &payload); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		events, err := s.Bus.Read(r.Context(), stream, payload.IDs, payload.Reader)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, events)
	case "ack":
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w)
			return
		}
		var payload struct {
			IDs    []string `json:"ids"`
			Reader string   `json:"reader"`
		}
		if err := decodeJSON(r.Body, &payload); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err := s.Bus.Ack(r.Context(), stream, payload.IDs, payload.Reader); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeError(w, http.StatusNotFound, errNotFound("stream action"))
	}
}

func (s *Server) handleStreamSubscribe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	streamsParam := r.URL.Query().Get("streams")
	if streamsParam == "" {
		streamsParam = "task_output,errors"
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

	for {
		select {
		case <-ctx.Done():
			return
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

func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if s.Restart == nil {
		writeError(w, http.StatusNotImplemented, errNotFound("restart"))
		return
	}
	if token := s.RestartToken; token != "" {
		header := r.Header.Get("X-Restart-Token")
		if header != token {
			writeError(w, http.StatusUnauthorized, errNotFound("invalid restart token"))
			return
		}
	}
	if err := s.Restart(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
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
