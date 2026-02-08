package api

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/flitsinc/go-agents/internal/engine"
	"github.com/flitsinc/go-agents/internal/eventbus"
	"github.com/flitsinc/go-agents/internal/schema"
	"github.com/flitsinc/go-agents/internal/tasks"
)

type agentState struct {
	ID          string    `json:"id"`
	Status      string    `json:"status"`
	ActiveTasks int       `json:"active_tasks"`
	UpdatedAt   time.Time `json:"updated_at"`
	LastError   string    `json:"last_error,omitempty"`
	Generation  int64     `json:"generation"`
}

type stateResponse struct {
	GeneratedAt time.Time                      `json:"generated_at"`
	Agents      []agentState                   `json:"agents"`
	Tasks       []tasks.Task                   `json:"tasks"`
	Updates     map[string][]tasks.Update      `json:"updates"`
	Sessions    map[string]engine.Session      `json:"sessions"`
	Histories   map[string]engine.AgentHistory `json:"histories"`
	Streams     map[string][]eventbus.Event    `json:"streams"`
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}

	taskLimit := parseInt(r.URL.Query().Get("tasks"), 200)
	updateLimit := parseInt(r.URL.Query().Get("updates"), 200)
	streamLimit := parseInt(r.URL.Query().Get("streams"), 100)
	historyLimit := parseInt(r.URL.Query().Get("history"), 800)
	streamList := splitComma(r.URL.Query().Get("stream_names"))
	if len(streamList) == 0 {
		streamList = append(schema.AgentStreams, schema.StreamHistory)
	}

	resp := stateResponse{
		GeneratedAt: s.now(),
		Updates:     map[string][]tasks.Update{},
		Sessions:    map[string]engine.Session{},
		Histories:   map[string]engine.AgentHistory{},
		Streams:     map[string][]eventbus.Event{},
	}

	agentIDs := map[string]struct{}{}
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
			if task.Type == "agent" {
				agentIDs[task.ID] = struct{}{}
			}
			if task.Metadata != nil {
				if val, ok := task.Metadata["notify_target"].(string); ok && strings.TrimSpace(val) != "" {
					agentIDs[strings.TrimSpace(val)] = struct{}{}
				}
			}
		}
	}

	if s.Runtime != nil {
		for id, session := range s.Runtime.SessionsSnapshot() {
			if strings.TrimSpace(id) == "" {
				continue
			}
			taskID := strings.TrimSpace(id)
			agentIDs[taskID] = struct{}{}
			resp.Sessions[taskID] = session
		}
	}

	orderedAgentIDs := make([]string, 0, len(agentIDs))
	for agentID := range agentIDs {
		orderedAgentIDs = append(orderedAgentIDs, agentID)
		if _, ok := resp.Sessions[agentID]; !ok {
			resp.Sessions[agentID] = engine.Session{TaskID: agentID}
		}
	}
	sort.Strings(orderedAgentIDs)

	if s.Bus != nil && historyLimit > 0 {
		for _, agentID := range orderedAgentIDs {
			history, err := readAgentHistory(r.Context(), s.Bus, agentID, historyLimit)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			resp.Histories[agentID] = history
		}
	}

	orderedAgentIDs = filterVisibleAgentIDs(orderedAgentIDs, resp.Tasks, resp.Sessions, resp.Histories)
	resp.Agents = buildAgentState(orderedAgentIDs, resp.Tasks, resp.Sessions, resp.Histories)

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

func buildAgentState(agentIDs []string, allTasks []tasks.Task, sessions map[string]engine.Session, histories map[string]engine.AgentHistory) []agentState {
	if len(agentIDs) == 0 {
		return nil
	}
	out := make([]agentState, 0, len(agentIDs))
	for _, agentID := range agentIDs {
		active := 0
		lastTaskUpdate := time.Time{}
		for _, task := range allTasks {
			if task.Owner != agentID && schema.GetMetaString(task.Metadata, schema.MetaNotifyTarget) != agentID {
				continue
			}
			if task.UpdatedAt.After(lastTaskUpdate) {
				lastTaskUpdate = task.UpdatedAt
			}
			// The root agent task stays "running" for the lifetime of the
			// agent loop — skip it so only child tasks (llm, exec, etc.)
			// contribute to the active count.
			if task.ID == agentID {
				continue
			}
			if task.Status == tasks.StatusQueued || task.Status == tasks.StatusRunning {
				active++
			}
		}

		session := sessions[agentID]
		history := histories[agentID]
		lastHistory := time.Time{}
		if len(history.Entries) > 0 {
			lastHistory = history.Entries[len(history.Entries)-1].CreatedAt
		}
		updatedAt := maxTime(session.UpdatedAt, maxTime(lastTaskUpdate, lastHistory))
		status := "idle"
		if active > 0 {
			status = "running"
		} else if strings.TrimSpace(session.LastError) != "" {
			status = "failed"
		}
		out = append(out, agentState{
			ID:          agentID,
			Status:      status,
			ActiveTasks: active,
			UpdatedAt:   updatedAt,
			LastError:   strings.TrimSpace(session.LastError),
			Generation:  history.Generation,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out
}

func readAgentHistory(ctx context.Context, bus *eventbus.Bus, agentID string, limit int) (engine.AgentHistory, error) {
	if bus == nil {
		return engine.AgentHistory{AgentID: agentID, Generation: 1}, nil
	}
	if limit <= 0 {
		limit = 400
	}
	readLimit := limit * 4
	summaries, err := bus.List(ctx, "history", eventbus.ListOptions{
		ScopeType: "task",
		ScopeID:   agentID,
		Limit:     readLimit,
		Order:     "lifo",
	})
	if err != nil {
		return engine.AgentHistory{}, err
	}
	if len(summaries) == 0 {
		return engine.AgentHistory{AgentID: agentID, Generation: 1}, nil
	}
	ids := make([]string, 0, len(summaries))
	for _, summary := range summaries {
		ids = append(ids, summary.ID)
	}
	events, err := bus.Read(ctx, "history", ids, "")
	if err != nil {
		return engine.AgentHistory{}, err
	}
	byID := map[string]eventbus.Event{}
	for _, evt := range events {
		byID[evt.ID] = evt
	}

	newestFirst := make([]engine.AgentHistoryEntry, 0, len(summaries))
	for _, summary := range summaries {
		evt, ok := byID[summary.ID]
		if !ok {
			continue
		}
		entry, ok := engine.HistoryEntryFromEvent(evt)
		if !ok {
			continue
		}
		newestFirst = append(newestFirst, entry)
	}

	currentGeneration := int64(1)
	for _, entry := range newestFirst {
		if entry.Generation > currentGeneration {
			currentGeneration = entry.Generation
		}
	}
	filtered := make([]engine.AgentHistoryEntry, 0, len(newestFirst))
	for _, entry := range newestFirst {
		if entry.Generation == currentGeneration {
			filtered = append(filtered, entry)
		}
		if len(filtered) >= limit {
			break
		}
	}
	for i, j := 0, len(filtered)-1; i < j; i, j = i+1, j-1 {
		filtered[i], filtered[j] = filtered[j], filtered[i]
	}
	return engine.AgentHistory{
		AgentID:    agentID,
		Generation: currentGeneration,
		Entries:    filtered,
	}, nil
}


func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func filterVisibleAgentIDs(
	agentIDs []string,
	allTasks []tasks.Task,
	sessions map[string]engine.Session,
	histories map[string]engine.AgentHistory,
) []string {
	if len(agentIDs) == 0 {
		return nil
	}
	out := make([]string, 0, len(agentIDs))
	for _, raw := range agentIDs {
		agentID := strings.TrimSpace(raw)
		if agentID == "" {
			continue
		}
		history := histories[agentID]
		hasHistory := len(history.Entries) > 0
		session := sessions[agentID]
		hasSession := strings.TrimSpace(session.LLMTaskID) != "" ||
			strings.TrimSpace(session.LastInput) != "" ||
			strings.TrimSpace(session.LastOutput) != "" ||
			strings.TrimSpace(session.LastError) != ""

		hasAgentTasks := false
		for _, task := range allTasks {
			if task.Type != "agent" && task.Type != "llm" {
				continue
			}
			if task.Owner == agentID || task.ID == agentID || schema.GetMetaString(task.Metadata, schema.MetaNotifyTarget) == agentID {
				hasAgentTasks = true
				break
			}
		}
		if hasHistory || hasSession || hasAgentTasks {
			out = append(out, agentID)
		}
	}
	return out
}
