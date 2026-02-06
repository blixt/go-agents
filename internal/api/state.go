package api

import (
	"net/http"
	"time"

	"github.com/flitsinc/go-agents/internal/engine"
	"github.com/flitsinc/go-agents/internal/eventbus"
	"github.com/flitsinc/go-agents/internal/tasks"
)

type stateResponse struct {
	GeneratedAt time.Time                   `json:"generated_at"`
	Tasks       []tasks.Task                `json:"tasks"`
	Updates     map[string][]tasks.Update   `json:"updates"`
	Sessions    map[string]engine.Session   `json:"sessions"`
	Streams     map[string][]eventbus.Event `json:"streams"`
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}

	taskLimit := parseInt(r.URL.Query().Get("tasks"), 200)
	updateLimit := parseInt(r.URL.Query().Get("updates"), 200)
	streamLimit := parseInt(r.URL.Query().Get("streams"), 100)
	streamList := splitComma(r.URL.Query().Get("stream_names"))
	if len(streamList) == 0 {
		streamList = []string{"messages", "signals", "errors", "external", "task_output"}
	}

	resp := stateResponse{
		GeneratedAt: time.Now().UTC(),
		Updates:     map[string][]tasks.Update{},
		Sessions:    map[string]engine.Session{},
		Streams:     map[string][]eventbus.Event{},
	}

	if s.Tasks != nil {
		tasksList, err := s.Tasks.List(r.Context(), tasks.ListFilter{Limit: taskLimit})
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		resp.Tasks = tasksList
		for _, task := range tasksList {
			updates, err := s.Tasks.ListUpdates(r.Context(), task.ID, updateLimit)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			if len(updates) > 0 {
				resp.Updates[task.ID] = updates
			}
		}
	}

	if s.Runtime != nil {
		agentIDs := map[string]struct{}{"operator": {}}
		for _, task := range resp.Tasks {
			if task.Owner != "" {
				agentIDs[task.Owner] = struct{}{}
			}
			if task.Metadata != nil {
				if val, ok := task.Metadata["agent_id"].(string); ok && val != "" {
					agentIDs[val] = struct{}{}
				}
			}
		}
		for agentID := range agentIDs {
			session, ok := s.Runtime.GetSession(agentID)
			if !ok {
				session = engine.Session{AgentID: agentID, UpdatedAt: time.Now().UTC()}
			}
			resp.Sessions[agentID] = session
		}
	}

	if s.Bus != nil {
		for _, stream := range streamList {
			summaries, err := s.Bus.List(r.Context(), stream, eventbus.ListOptions{
				Limit: streamLimit,
				Order: "lifo",
			})
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			if len(summaries) == 0 {
				continue
			}
			ids := make([]string, 0, len(summaries))
			for _, summary := range summaries {
				ids = append(ids, summary.ID)
			}
			events, err := s.Bus.Read(r.Context(), stream, ids, "")
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			byID := map[string]eventbus.Event{}
			for _, evt := range events {
				byID[evt.ID] = evt
			}
			ordered := make([]eventbus.Event, 0, len(summaries))
			for _, summary := range summaries {
				if evt, ok := byID[summary.ID]; ok {
					ordered = append(ordered, evt)
				}
			}
			resp.Streams[stream] = ordered
		}
	}

	writeJSON(w, http.StatusOK, resp)
}
