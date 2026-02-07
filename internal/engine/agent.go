package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/flitsinc/go-agents/internal/agentcontext"
	"github.com/flitsinc/go-agents/internal/ai"
	"github.com/flitsinc/go-agents/internal/eventbus"
	"github.com/flitsinc/go-agents/internal/goagents"
	agentctx "github.com/flitsinc/go-agents/internal/prompt"
	"github.com/flitsinc/go-agents/internal/tasks"
	"github.com/flitsinc/go-llms/content"
	"github.com/flitsinc/go-llms/llms"
	llmtools "github.com/flitsinc/go-llms/tools"
)

type Session struct {
	AgentID    string    `json:"agent_id"`
	RootTaskID string    `json:"root_task_id,omitempty"`
	LLMTaskID  string    `json:"llm_task_id,omitempty"`
	Prompt     string    `json:"prompt"`
	LastInput  string    `json:"last_input"`
	LastOutput string    `json:"last_output"`
	LastError  string    `json:"last_error,omitempty"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type AgentState struct {
	ID         string
	Prompt     *agentctx.Manager
	RootTaskID string
	System     string
	Model      string
	mu         sync.Mutex
}

type ConversationTurn struct {
	Source    string
	Input     string
	Output    string
	Priority  string
	CreatedAt time.Time
}

type TurnContext struct {
	Now         time.Time
	Previous    time.Time
	TimePassed  bool
	Elapsed     time.Duration
	DateChanged bool
}

type Runtime struct {
	Bus        *eventbus.Bus
	Tasks      *tasks.Manager
	LLM        *ai.Client
	Context    *agentctx.Manager
	LLMFactory func() (*llms.LLM, error)

	baseCtx context.Context
	loopMu  sync.Mutex
	loops   map[string]context.CancelFunc

	mu       sync.RWMutex
	sessions map[string]Session

	agentMu sync.RWMutex
	agents  map[string]*AgentState

	inflightMu sync.Mutex
	inflight   map[string]context.CancelFunc

	wakeMu   sync.Mutex
	lastWake map[string]time.Time

	turnMu        sync.Mutex
	lastTurnStart map[string]time.Time

	knownMu     sync.RWMutex
	knownAgents map[string]struct{}

	historyMu                sync.Mutex
	historyGenerationByAgent map[string]int64
	historyCompactionCutoff  map[string]time.Time
	historyPreambleByAgent   map[string]int64
}

const (
	maxConversationTurns  = 8
	maxHistoryInputChars  = 400
	maxHistoryOutputChars = 800
	maxHistoryBytes       = 8000
	maxToolContentChars   = 1200

	maxContextEventsPerTurn = 24
	maxContextEventBody     = 500
	maxContextEventData     = 900
	minTimePassedDelta      = 60 * time.Second
)

func NewRuntime(bus *eventbus.Bus, tasksMgr *tasks.Manager, client *ai.Client) *Runtime {
	home, err := goagents.EnsureHome()
	if err != nil {
		home = ""
	}
	ctxMgr := &agentctx.Manager{Home: home}

	return &Runtime{
		Bus:                      bus,
		Tasks:                    tasksMgr,
		LLM:                      client,
		Context:                  ctxMgr,
		loops:                    map[string]context.CancelFunc{},
		sessions:                 map[string]Session{},
		agents:                   map[string]*AgentState{},
		inflight:                 map[string]context.CancelFunc{},
		lastWake:                 map[string]time.Time{},
		lastTurnStart:            map[string]time.Time{},
		knownAgents:              map[string]struct{}{},
		historyGenerationByAgent: map[string]int64{},
		historyCompactionCutoff:  map[string]time.Time{},
		historyPreambleByAgent:   map[string]int64{},
	}
}

func (r *Runtime) Start(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	r.baseCtx = ctx
	if r.Tasks != nil && r.Bus != nil {
		go r.monitorTaskHealth(ctx)
	}
}

func (r *Runtime) monitorTaskHealth(ctx context.Context) {
	const taskHealthInterval = 30 * time.Second
	ticker := time.NewTicker(taskHealthInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.emitTaskHealth(ctx)
		}
	}
}

func (r *Runtime) emitTaskHealth(ctx context.Context) {
	const taskHealthStale = 30 * time.Second
	const taskHealthWakeCooldown = 30 * time.Second
	if r.Tasks == nil || r.Bus == nil {
		return
	}
	tasksList, err := r.Tasks.List(ctx, tasks.ListFilter{
		Status: tasks.StatusRunning,
		Limit:  200,
	})
	if err != nil || len(tasksList) == 0 {
		return
	}

	now := time.Now().UTC()
	byTarget := map[string][]map[string]any{}
	staleByTarget := map[string][]map[string]any{}
	for _, task := range tasksList {
		target := ""
		if task.Metadata != nil {
			if val, ok := task.Metadata["notify_target"].(string); ok {
				target = val
			}
		}
		if target == "" {
			target = task.Owner
		}
		if target == "" {
			target = "*"
		}
		entry := map[string]any{
			"id":              task.ID,
			"type":            task.Type,
			"status":          task.Status,
			"owner":           task.Owner,
			"parent_id":       task.ParentID,
			"created_at":      task.CreatedAt,
			"updated_at":      task.UpdatedAt,
			"age_seconds":     int64(now.Sub(task.CreatedAt).Seconds()),
			"updated_seconds": int64(now.Sub(task.UpdatedAt).Seconds()),
		}
		byTarget[target] = append(byTarget[target], entry)
		if task.Type == "exec" && now.Sub(task.UpdatedAt) >= taskHealthStale {
			staleByTarget[target] = append(staleByTarget[target], entry)
		}
	}

	for target, list := range byTarget {
		scopeType := "agent"
		scopeID := target
		if target == "*" {
			scopeType = "global"
			scopeID = "*"
		}
		_, _ = r.Bus.Push(ctx, eventbus.EventInput{
			Stream:    "signals",
			Subject:   "task_health",
			Body:      "task health snapshot",
			ScopeType: scopeType,
			ScopeID:   scopeID,
			Payload: map[string]any{
				"generated_at": now,
				"tasks":        list,
			},
			Metadata: map[string]any{
				"kind":     "task_health",
				"target":   target,
				"priority": "low",
			},
		})
	}

	for target, list := range staleByTarget {
		if target == "" || target == "*" {
			continue
		}
		var wakeIDs []string
		for _, entry := range list {
			id, _ := entry["id"].(string)
			if id == "" {
				continue
			}
			if !r.shouldWakeTask(id, now, taskHealthWakeCooldown) {
				continue
			}
			wakeIDs = append(wakeIDs, id)
		}
		if len(wakeIDs) == 0 {
			continue
		}
		body := fmt.Sprintf("task_health: stale tasks detected (%d). See signals/task_health. ids=%s", len(wakeIDs), strings.Join(wakeIDs, ","))
		_, _ = r.Bus.Push(ctx, eventbus.EventInput{
			Stream:    "messages",
			ScopeType: "agent",
			ScopeID:   target,
			Subject:   fmt.Sprintf("wake: task_health ids=%s", strings.Join(wakeIDs, ",")),
			Body:      body,
			Metadata: map[string]any{
				"priority": "low",
				"kind":     "wake",
				"reason":   "task_health",
				"task_ids": wakeIDs,
				"source":   "runtime",
			},
		})
	}
}

func (r *Runtime) shouldWakeTask(taskID string, now time.Time, cooldown time.Duration) bool {
	r.wakeMu.Lock()
	defer r.wakeMu.Unlock()
	last, ok := r.lastWake[taskID]
	if ok && now.Sub(last) < cooldown {
		return false
	}
	r.lastWake[taskID] = now
	return true
}

func (r *Runtime) EnsureAgentLoop(agentID string) {
	if agentID == "" {
		return
	}
	r.rememberAgent(agentID)
	r.loopMu.Lock()
	if _, ok := r.loops[agentID]; ok {
		r.loopMu.Unlock()
		return
	}
	base := r.baseCtx
	if base == nil {
		base = context.Background()
	}
	loopCtx, cancel := context.WithCancel(base)
	r.loops[agentID] = cancel
	r.loopMu.Unlock()

	go func() {
		_ = r.Run(loopCtx, agentID)
		r.loopMu.Lock()
		delete(r.loops, agentID)
		r.loopMu.Unlock()
	}()
}

func (r *Runtime) ensureAgentState(agentID string) *AgentState {
	if agentID == "" {
		return nil
	}
	r.rememberAgent(agentID)
	r.agentMu.Lock()
	state, ok := r.agents[agentID]
	if !ok {
		state = &AgentState{
			ID:     agentID,
			Prompt: clonePromptManager(r.Context),
		}
		r.agents[agentID] = state
	}
	r.agentMu.Unlock()
	return state
}

func (r *Runtime) ensureAgentLLM(state *AgentState) (*llms.LLM, error) {
	if state == nil {
		return nil, fmt.Errorf("agent state is nil")
	}
	if r.LLMFactory != nil {
		return r.LLMFactory()
	}
	if r.LLM != nil {
		if state.Model != "" {
			if llm, err := r.LLM.NewSessionWithModel(state.Model); err == nil {
				return llm, nil
			}
		}
		if llm, err := r.LLM.NewSession(); err == nil {
			return llm, nil
		}
		if r.LLM.LLM != nil {
			return r.LLM.LLM, nil
		}
		return nil, fmt.Errorf("LLM not configured")
	}
	return nil, fmt.Errorf("LLM not configured")
}

func clonePromptManager(src *agentctx.Manager) *agentctx.Manager {
	if src == nil {
		return nil
	}
	toolNames := append([]string{}, src.ToolNames...)
	return &agentctx.Manager{Home: src.Home, ToolNames: toolNames}
}

func (r *Runtime) SetPromptTools(toolNames []string) {
	normalized := normalizePromptToolNames(toolNames)
	if r.Context != nil {
		r.Context.ToolNames = append([]string{}, normalized...)
	}

	r.agentMu.Lock()
	defer r.agentMu.Unlock()
	for _, state := range r.agents {
		if state == nil {
			continue
		}
		if state.Prompt == nil {
			state.Prompt = clonePromptManager(r.Context)
			continue
		}
		state.Prompt.ToolNames = append([]string{}, normalized...)
	}
}

func normalizePromptToolNames(toolNames []string) []string {
	if len(toolNames) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(toolNames))
	for _, raw := range toolNames {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (r *Runtime) SetAgentSystem(agentID, system string) {
	if agentID == "" {
		return
	}
	state := r.ensureAgentState(agentID)
	if state == nil {
		return
	}
	state.mu.Lock()
	state.System = strings.TrimSpace(system)
	state.mu.Unlock()
}

func (r *Runtime) SetAgentModel(agentID, model string) {
	if agentID == "" {
		return
	}
	state := r.ensureAgentState(agentID)
	if state == nil {
		return
	}
	state.mu.Lock()
	state.Model = strings.TrimSpace(model)
	state.mu.Unlock()
}

func (r *Runtime) EnsureRootTask(ctx context.Context, agentID string) (tasks.Task, error) {
	if r.Tasks == nil {
		return tasks.Task{}, fmt.Errorf("task manager unavailable")
	}
	if agentID == "" {
		return tasks.Task{}, fmt.Errorf("agent_id is required")
	}
	state := r.ensureAgentState(agentID)
	if state == nil {
		return tasks.Task{}, fmt.Errorf("agent state unavailable")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.RootTaskID != "" {
		task, err := r.Tasks.Get(ctx, state.RootTaskID)
		if err == nil && task.ID != "" {
			return task, nil
		}
	}
	metadata := map[string]any{
		"agent_id":      agentID,
		"input_target":  agentID,
		"notify_target": agentID,
	}
	created, err := r.Tasks.Spawn(ctx, tasks.Spec{
		Type:     "agent",
		Owner:    agentID,
		Mode:     "async",
		Metadata: metadata,
	})
	if err != nil {
		return tasks.Task{}, err
	}
	_ = r.Tasks.MarkRunning(ctx, created.ID)
	state.RootTaskID = created.ID
	return created, nil
}

func (r *Runtime) RunOnce(ctx context.Context, agentID, message string) (Session, error) {
	return r.HandleMessage(ctx, agentID, "", message, nil)
}

func (r *Runtime) HandleMessage(ctx context.Context, agentID, source, message string, messageMeta map[string]any) (Session, error) {
	if agentID == "" {
		return Session{}, fmt.Errorf("agent_id is required")
	}
	r.rememberAgent(agentID)
	state := r.ensureAgentState(agentID)
	if state == nil {
		return Session{}, fmt.Errorf("agent state unavailable")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	currentGeneration := r.currentHistoryGeneration(ctx, agentID)

	var promptContent content.Content
	var promptText string
	if state.Prompt != nil {
		prompt, text, err := state.Prompt.BuildSystemPrompt(ctx, r.Bus)
		if err != nil {
			return Session{}, err
		}
		promptContent = prompt
		promptText = text
	} else {
		return Session{}, fmt.Errorf("prompt unavailable")
	}
	if state.System != "" {
		promptText = fmt.Sprintf("%s\n\n%s", promptText, state.System)
		promptContent = content.FromText(promptText)
	}

	var rootTask tasks.Task
	var llmTask tasks.Task
	if r.Tasks != nil {
		if state.RootTaskID == "" {
			created, _ := r.Tasks.Spawn(ctx, tasks.Spec{
				Type:  "agent",
				Owner: agentID,
				Mode:  "async",
				Metadata: map[string]any{
					"agent_id":      agentID,
					"input_target":  agentID,
					"notify_target": agentID,
				},
			})
			_ = r.Tasks.MarkRunning(ctx, created.ID)
			state.RootTaskID = created.ID
			rootTask = created
		} else {
			rootTask, _ = r.Tasks.Get(ctx, state.RootTaskID)
			if rootTask.ID == "" {
				rootTask.ID = state.RootTaskID
			}
		}

		llmTask, _ = r.Tasks.Spawn(ctx, tasks.Spec{
			Type:     "llm",
			Owner:    agentID,
			ParentID: state.RootTaskID,
			Mode:     "sync",
			Metadata: map[string]any{
				"agent_id":           agentID,
				"input_target":       agentID,
				"notify_target":      agentID,
				"source":             source,
				"priority":           eventPriority(messageMeta),
				"request_id":         getMetaString(messageMeta, "request_id"),
				"event_id":           getMetaString(messageMeta, "event_id"),
				"history_generation": currentGeneration,
			},
		})
		_ = r.Tasks.MarkRunning(ctx, llmTask.ID)
		_ = r.Tasks.Send(ctx, llmTask.ID, map[string]any{"message": message})
	}

	session := Session{
		AgentID:    agentID,
		RootTaskID: state.RootTaskID,
		LLMTaskID:  llmTask.ID,
		Prompt:     promptText,
		LastInput:  message,
		UpdatedAt:  time.Now().UTC(),
	}
	turnCtx := r.nextTurnContext(agentID, session.UpdatedAt)
	contextEvents, _ := r.collectUnreadContextEvents(ctx, agentID, maxContextEventsPerTurn*2)
	contextEvents = selectContextEventsForPrompt(contextEvents, maxContextEventsPerTurn)
	toolsSnapshot := []string{}
	if state.Prompt != nil && len(state.Prompt.ToolNames) > 0 {
		toolsSnapshot = append(toolsSnapshot, state.Prompt.ToolNames...)
		sort.Strings(toolsSnapshot)
	}
	if r.shouldAppendGenerationPreamble(ctx, agentID, currentGeneration) {
		r.appendHistory(ctx, agentID, "tools_config", "system", strings.Join(toolsSnapshot, ", "), llmTask.ID, currentGeneration, map[string]any{
			"tools": toolsSnapshot,
		})
		r.appendHistory(ctx, agentID, "system_prompt", "system", promptText, llmTask.ID, currentGeneration, nil)
	}
	if strings.TrimSpace(message) != "" {
		r.appendHistory(ctx, agentID, "user_message", "user", message, llmTask.ID, currentGeneration, map[string]any{
			"source":     source,
			"priority":   eventPriority(messageMeta),
			"request_id": getMetaString(messageMeta, "request_id"),
			"event_id":   getMetaString(messageMeta, "event_id"),
		})
	} else if strings.EqualFold(getMetaString(messageMeta, "kind"), "wake") {
		r.appendHistory(ctx, agentID, "wake", "system", "wake turn", llmTask.ID, currentGeneration, map[string]any{
			"stream":   getMetaString(messageMeta, "stream"),
			"event_id": getMetaString(messageMeta, "event_id"),
			"priority": eventPriority(messageMeta),
		})
	}
	r.appendContextUpdateHistory(ctx, agentID, llmTask.ID, currentGeneration, turnCtx, contextEvents)

	if r.Bus != nil {
		_, _ = r.Bus.Push(ctx, eventbus.EventInput{
			Stream:    "signals",
			Subject:   "agent_run_start",
			Body:      "agent run started",
			ScopeType: "agent",
			ScopeID:   agentID,
			Metadata: map[string]any{
				"agent_id": agentID,
			},
		})
	}

	llmClient, err := r.ensureAgentLLM(state)
	if err != nil || llmClient == nil {
		session.LastError = "LLM not configured. Set the provider API key (e.g. GO_AGENTS_ANTHROPIC_API_KEY) and configure llm_provider/llm_model in config.json."
		session.LastOutput = session.LastError
		r.SetSession(session)
		r.appendHistory(ctx, agentID, "assistant_message", "assistant", session.LastOutput, llmTask.ID, currentGeneration, map[string]any{
			"error": true,
		})
		if r.Tasks != nil {
			ctx := context.Background()
			if llmTask.ID != "" {
				_ = r.Tasks.Fail(ctx, llmTask.ID, session.LastError)
			}
		}
		if source != "" && source != agentID {
			_, _ = r.pushMessage(ctx, source, session.LastOutput, agentID, nil)
		}
		r.ackContextEvents(context.Background(), agentID, contextEvents)
		return session, nil
	}
	r.attachDebugger(llmClient, agentID, llmTask.ID)

	var output string
	{
		llmCtx := tasks.WithParentTaskID(ctx, llmTask.ID)
		llmCtx = agentcontext.WithAgentID(llmCtx, agentID)
		llmCtx, cancel := context.WithCancel(llmCtx)
		r.registerInflight(llmTask.ID, cancel)
		defer func() {
			cancel()
			r.clearInflight(llmTask.ID)
		}()

		if r.Bus != nil {
			interruptCtx, interruptCancel := context.WithCancel(ctx)
			defer interruptCancel()
			go r.watchInterrupts(interruptCtx, agentID, llmTask.ID, cancel)
			go r.watchTaskCommands(interruptCtx, llmTask.ID, cancel)
		}

		prev := llmClient.SystemPrompt
		llmClient.SystemPrompt = func() content.Content { return promptContent }
		defer func() {
			llmClient.SystemPrompt = prev
		}()

		turns := r.loadRecentConversation(llmCtx, agentID, maxConversationTurns)
		input := buildInputWithHistory(turns, source, message, messageMeta, turnCtx, contextEvents)
		updates := llmClient.ChatWithContext(llmCtx, input)
		toolInputRaw := map[string]string{}
		toolStreamingMarked := map[string]bool{}
		for update := range updates {
			switch u := update.(type) {
			case llms.TextUpdate:
				output += u.Text
				r.recordLLMUpdate(llmCtx, llmTask.ID, "llm_text", map[string]any{"text": u.Text})
			case llms.MessageStartUpdate:
				r.recordLLMUpdate(llmCtx, llmTask.ID, "llm_message_start", map[string]any{"message_id": u.MessageID})
			case llms.ThinkingUpdate:
				r.recordLLMUpdate(llmCtx, llmTask.ID, "llm_thinking", map[string]any{
					"id":      u.ID,
					"text":    u.Text,
					"summary": u.Summary,
				})
				reasoning := strings.TrimSpace(u.Text)
				if reasoning != "" || strings.TrimSpace(u.ID) != "" {
					r.appendHistory(llmCtx, agentID, "reasoning", "assistant", reasoning, llmTask.ID, currentGeneration, map[string]any{
						"reasoning_id": strings.TrimSpace(u.ID),
						"summary":      u.Summary,
					})
				}
			case llms.ThinkingDoneUpdate:
				r.recordLLMUpdate(llmCtx, llmTask.ID, "llm_thinking_done", map[string]any{"done": true})
			case llms.ToolStartUpdate:
				r.recordLLMUpdate(llmCtx, llmTask.ID, "llm_tool_start", map[string]any{
					"tool_call_id": u.ToolCallID,
					"tool_name":    u.Tool.FuncName(),
					"tool_label":   u.Tool.Label(),
					"tool_desc":    u.Tool.Description(),
				})
				r.appendToolHistory(llmCtx, agentID, llmTask.ID, "tool_call", u.ToolCallID, u.Tool.FuncName(), "start", "", map[string]any{
					"tool_label": u.Tool.Label(),
					"tool_desc":  u.Tool.Description(),
				})
			case llms.ToolDeltaUpdate:
				r.recordLLMUpdate(llmCtx, llmTask.ID, "llm_tool_delta", map[string]any{
					"tool_call_id": u.ToolCallID,
					"delta":        string(u.Delta),
				})
				if strings.TrimSpace(u.ToolCallID) != "" {
					toolInputRaw[u.ToolCallID] += string(u.Delta)
					if !toolStreamingMarked[u.ToolCallID] {
						toolStreamingMarked[u.ToolCallID] = true
						r.appendToolHistory(llmCtx, agentID, llmTask.ID, "tool_status", u.ToolCallID, "", "streaming", "", map[string]any{
							"delta_bytes": len(toolInputRaw[u.ToolCallID]),
						})
					}
				}
			case llms.ToolStatusUpdate:
				r.recordLLMUpdate(llmCtx, llmTask.ID, "llm_tool_status", map[string]any{
					"tool_call_id": u.ToolCallID,
					"status":       u.Status,
					"tool_name":    u.Tool.FuncName(),
				})
				r.appendToolHistory(llmCtx, agentID, llmTask.ID, "tool_status", u.ToolCallID, u.Tool.FuncName(), u.Status, "", nil)
			case llms.ToolDoneUpdate:
				payload := map[string]any{
					"tool_call_id": u.ToolCallID,
					"tool_name":    u.Tool.FuncName(),
				}
				if raw := strings.TrimSpace(toolInputRaw[u.ToolCallID]); raw != "" {
					payload["args_raw"] = raw
					if parsed, ok := parseJSONValue(raw); ok {
						payload["args"] = parsed
					}
				}
				if u.Result != nil {
					payload["result"] = summarizeToolResult(u.Result)
				}
				if u.Metadata != nil {
					payload["metadata"] = u.Metadata
				}
				r.recordLLMUpdate(llmCtx, llmTask.ID, "llm_tool_done", payload)
				toolStatus := "done"
				if u.Result != nil && u.Result.Error() != nil {
					toolStatus = "failed"
				}
				r.appendToolHistory(llmCtx, agentID, llmTask.ID, "tool_result", u.ToolCallID, u.Tool.FuncName(), toolStatus, "", payload)
			case llms.ImageUpdate:
				r.recordLLMUpdate(llmCtx, llmTask.ID, "llm_image", map[string]any{
					"url":       u.URL,
					"mime_type": u.MimeType,
				})
			}
		}
		if err := llmClient.Err(); err != nil {
			session.LastError = err.Error()
			session.LastOutput = output
			r.SetSession(session)
			if strings.TrimSpace(output) != "" {
				r.appendHistory(llmCtx, agentID, "assistant_message", "assistant", output, llmTask.ID, currentGeneration, map[string]any{
					"partial": true,
				})
			}
			r.appendHistory(llmCtx, agentID, "error", "system", err.Error(), llmTask.ID, currentGeneration, nil)
			if r.Tasks != nil {
				ctx := context.Background()
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					if llmTask.ID != "" {
						_ = r.Tasks.Cancel(ctx, llmTask.ID, "interrupted")
					}
				} else {
					if llmTask.ID != "" {
						_ = r.Tasks.Fail(ctx, llmTask.ID, err.Error())
					}
				}
			}
			if r.Bus != nil {
				_, _ = r.Bus.Push(ctx, eventbus.EventInput{
					Stream:    "errors",
					Subject:   "agent_run_error",
					Body:      session.LastError,
					ScopeType: "agent",
					ScopeID:   agentID,
					Metadata: map[string]any{
						"agent_id": agentID,
					},
				})
			}
			if source != "" && source != agentID {
				reply := output
				if reply == "" {
					reply = fmt.Sprintf("[error] %s", session.LastError)
				} else {
					reply = fmt.Sprintf("%s\n\n[error] %s", reply, session.LastError)
				}
				_, _ = r.pushMessage(ctx, source, reply, agentID, nil)
			}
			return session, err
		}
	}

	r.ackContextEvents(context.Background(), agentID, contextEvents)
	session.LastOutput = output
	r.SetSession(session)
	if strings.TrimSpace(output) != "" {
		r.appendHistory(ctx, agentID, "assistant_message", "assistant", output, llmTask.ID, currentGeneration, nil)
	}
	if r.Tasks != nil {
		ctx := context.Background()
		if llmTask.ID != "" {
			_ = r.Tasks.Complete(ctx, llmTask.ID, map[string]any{"output": output})
		}
	}
	if r.Bus != nil {
		_, _ = r.Bus.Push(ctx, eventbus.EventInput{
			Stream:    "signals",
			Subject:   "agent_run_complete",
			Body:      "agent run complete",
			ScopeType: "agent",
			ScopeID:   agentID,
			Metadata: map[string]any{
				"agent_id": agentID,
			},
		})
	}
	if source != "" && source != agentID && strings.TrimSpace(output) != "" {
		_, _ = r.pushMessage(ctx, source, output, agentID, nil)
	}
	return session, nil
}

func (r *Runtime) registerInflight(taskID string, cancel context.CancelFunc) {
	if taskID == "" || cancel == nil {
		return
	}
	r.inflightMu.Lock()
	r.inflight[taskID] = cancel
	r.inflightMu.Unlock()
}

func (r *Runtime) recordLLMUpdate(ctx context.Context, taskID, kind string, payload map[string]any) {
	if r.Tasks == nil || taskID == "" {
		return
	}
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		err = r.Tasks.RecordUpdate(ctx, taskID, kind, payload)
		if err == nil {
			return
		}
		if strings.Contains(err.Error(), "database is locked") {
			time.Sleep(25 * time.Millisecond)
			continue
		}
		break
	}
	if err != nil && r.Bus != nil {
		_, _ = r.Bus.Push(context.Background(), eventbus.EventInput{
			Stream:    "signals",
			ScopeType: "global",
			ScopeID:   "*",
			Subject:   "llm_update_error",
			Body:      err.Error(),
			Metadata: map[string]any{
				"kind":    "llm_update_error",
				"task_id": taskID,
				"update":  kind,
			},
		})
	}
}

func summarizeToolResult(result llmtools.Result) map[string]any {
	if result == nil {
		return nil
	}
	out := map[string]any{
		"label": result.Label(),
	}
	if err := result.Error(); err != nil {
		out["error"] = err.Error()
	}
	out["content"] = summarizeContent(result.Content())
	return out
}

func summarizeContent(items content.Content) []map[string]any {
	if len(items) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		switch v := item.(type) {
		case *content.Text:
			text := v.Text
			truncated := len(text) > maxToolContentChars
			out = append(out, map[string]any{
				"type":      "text",
				"text":      clipText(text, maxToolContentChars),
				"truncated": truncated,
			})
		case *content.JSON:
			data := string(v.Data)
			truncated := len(data) > maxToolContentChars
			out = append(out, map[string]any{
				"type":      "json",
				"data":      clipText(data, maxToolContentChars),
				"truncated": truncated,
			})
		case *content.ImageURL:
			urlValue := strings.TrimSpace(v.URL)
			display := urlValue
			if strings.HasPrefix(display, "data:") {
				if idx := strings.Index(display, ","); idx > 0 {
					display = display[:idx] + ",<omitted>"
				} else {
					display = "data:<omitted>"
				}
			}
			truncated := len(display) > maxToolContentChars || display != urlValue
			out = append(out, map[string]any{
				"type":      "image",
				"url":       clipText(display, maxToolContentChars),
				"mime_type": v.MimeType,
				"truncated": truncated,
			})
		case *content.Thought:
			text := v.Text
			truncated := len(text) > maxToolContentChars
			out = append(out, map[string]any{
				"type":      "thought",
				"id":        v.ID,
				"text":      clipText(text, maxToolContentChars),
				"summary":   v.Summary,
				"truncated": truncated,
			})
		case *content.CacheHint:
			out = append(out, map[string]any{"type": "cache_hint", "duration": v.Duration})
		default:
			out = append(out, map[string]any{"type": fmt.Sprintf("%T", item)})
		}
	}
	return out
}

func parseJSONValue(raw string) (any, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, false
	}
	var parsed any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, false
	}
	return parsed, true
}

func (r *Runtime) clearInflight(taskID string) {
	if taskID == "" {
		return
	}
	r.inflightMu.Lock()
	delete(r.inflight, taskID)
	r.inflightMu.Unlock()
}

func (r *Runtime) SendMessage(ctx context.Context, target, body, source string) (eventbus.Event, error) {
	return r.SendMessageWithMeta(ctx, target, body, source, nil)
}

func (r *Runtime) SendMessageWithMeta(ctx context.Context, target, body, source string, metadata map[string]any) (eventbus.Event, error) {
	return r.pushMessage(ctx, target, body, source, metadata)
}

func (r *Runtime) pushMessage(ctx context.Context, target, body, source string, metadata map[string]any) (eventbus.Event, error) {
	if r.Bus == nil {
		return eventbus.Event{}, fmt.Errorf("event bus unavailable")
	}
	if strings.TrimSpace(target) == "" {
		return eventbus.Event{}, fmt.Errorf("target agent is required")
	}
	message := strings.TrimSpace(body)
	if message == "" {
		return eventbus.Event{}, fmt.Errorf("message is required")
	}
	if strings.TrimSpace(source) == "" {
		source = "system"
	}
	meta := map[string]any{
		"kind":     "message",
		"source":   source,
		"target":   target,
		"priority": "wake",
	}
	for k, v := range metadata {
		meta[k] = v
	}
	if _, ok := meta["priority"]; !ok {
		meta["priority"] = "wake"
	}
	return r.Bus.Push(ctx, eventbus.EventInput{
		Stream:    "messages",
		ScopeType: "agent",
		ScopeID:   target,
		Subject:   fmt.Sprintf("Message from %s", source),
		Body:      message,
		Metadata:  meta,
	})
}

func (r *Runtime) watchInterrupts(ctx context.Context, agentID, taskID string, cancel context.CancelFunc) {
	if r.Bus == nil {
		return
	}
	sub := r.Bus.Subscribe(ctx, []string{"signals", "errors", "external", "messages", "task_output"})
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-sub:
			if !ok {
				return
			}
			if !eventTargetsAgent(evt, agentID) {
				continue
			}
			if evt.Metadata != nil {
				if priority, ok := evt.Metadata["priority"].(string); ok && strings.EqualFold(priority, "interrupt") {
					cancel()
					if r.Tasks != nil && taskID != "" {
						_ = r.Tasks.Kill(context.Background(), taskID, "interrupt")
					}
					return
				}
			}
		}
	}
}

func (r *Runtime) watchTaskCommands(ctx context.Context, taskID string, cancel context.CancelFunc) {
	if r.Bus == nil || taskID == "" {
		return
	}
	sub := r.Bus.Subscribe(ctx, []string{"task_input"})
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-sub:
			if !ok {
				return
			}
			if evt.Metadata == nil {
				continue
			}
			if id, ok := evt.Metadata["task_id"].(string); ok && id == taskID {
				if action, ok := evt.Metadata["action"].(string); ok {
					if action == "cancel" || action == "kill" {
						cancel()
						return
					}
				}
			}
		}
	}
}

func eventTargetsAgent(evt eventbus.Event, agentID string) bool {
	if agentID == "" {
		return true
	}
	if evt.ScopeType == "agent" {
		return evt.ScopeID == agentID
	}
	if evt.ScopeType == "global" || evt.ScopeType == "" {
		return evt.ScopeID == "" || evt.ScopeID == "*" || evt.ScopeID == agentID
	}
	return false
}

func (r *Runtime) GetSession(agentID string) (Session, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.sessions[agentID]
	return s, ok
}

func (r *Runtime) SetSession(session Session) {
	if session.AgentID == "" {
		return
	}
	r.rememberAgent(session.AgentID)
	r.mu.Lock()
	r.sessions[session.AgentID] = session
	r.mu.Unlock()
}

func (r *Runtime) SessionCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.sessions)
}

func (r *Runtime) BuildSession(ctx context.Context, agentID string) (Session, error) {
	if agentID == "" {
		return Session{}, fmt.Errorf("agent_id is required")
	}
	state := r.ensureAgentState(agentID)
	if state == nil || state.Prompt == nil {
		session := Session{AgentID: agentID, UpdatedAt: time.Now().UTC()}
		r.SetSession(session)
		return session, nil
	}
	_, promptText, err := state.Prompt.BuildSystemPrompt(ctx, r.Bus)
	if err != nil {
		return Session{}, err
	}
	state.mu.Lock()
	systemOverride := state.System
	state.mu.Unlock()
	if systemOverride != "" {
		promptText = fmt.Sprintf("%s\n\n%s", promptText, systemOverride)
	}
	session := Session{AgentID: agentID, Prompt: promptText, UpdatedAt: time.Now().UTC()}
	r.SetSession(session)
	return session, nil
}

func (r *Runtime) BuildPrompt(ctx context.Context, agentID string) (string, error) {
	if agentID == "" {
		return "", fmt.Errorf("agent_id is required")
	}
	state := r.ensureAgentState(agentID)
	if state == nil || state.Prompt == nil {
		return "", fmt.Errorf("prompt unavailable")
	}
	_, promptText, err := state.Prompt.BuildSystemPrompt(ctx, r.Bus)
	if err != nil {
		return "", err
	}
	state.mu.Lock()
	systemOverride := state.System
	state.mu.Unlock()
	if systemOverride != "" {
		promptText = fmt.Sprintf("%s\n\n%s", promptText, systemOverride)
	}
	return promptText, nil
}

func (r *Runtime) Run(ctx context.Context, agentID string) error {
	if r.Bus == nil {
		return nil
	}
	if agentID == "" {
		return fmt.Errorf("agent_id is required")
	}
	sub := r.Bus.Subscribe(ctx, append([]string{"messages"}, contextEventStreams...))
	replayTicker := time.NewTicker(500 * time.Millisecond)
	defer replayTicker.Stop()

	for {
		// Recover from missed in-memory fanout by replaying unread durable messages.
		replayed, err := r.replayUnreadMessages(ctx, agentID, 64)
		if err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		if replayed > 0 {
			continue
		}
		// If no direct message is pending, wake on unread wake/interrupt context events.
		replayedWake, err := r.replayUnreadWakeEvents(ctx, agentID, maxContextEventsPerTurn*2)
		if err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		if replayedWake > 0 {
			continue
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-replayTicker.C:
			continue
		case evt, ok := <-sub:
			if !ok {
				return ctx.Err()
			}
			if !eventTargetsAgent(evt, agentID) {
				continue
			}
			if _, err := r.replayUnreadMessages(ctx, agentID, 64); err != nil && ctx.Err() != nil {
				return ctx.Err()
			}
			if _, err := r.replayUnreadWakeEvents(ctx, agentID, maxContextEventsPerTurn*2); err != nil && ctx.Err() != nil {
				return ctx.Err()
			}
		}
	}
}

func (r *Runtime) replayUnreadMessages(ctx context.Context, agentID string, limit int) (int, error) {
	if r.Bus == nil {
		return 0, nil
	}
	if limit <= 0 {
		limit = 64
	}
	summaries, err := r.Bus.List(ctx, "messages", eventbus.ListOptions{
		Reader: agentID,
		Limit:  limit,
		Order:  "fifo",
	})
	if err != nil {
		return 0, err
	}
	if len(summaries) == 0 {
		return 0, nil
	}

	ids := make([]string, 0, len(summaries))
	for _, summary := range summaries {
		if !summary.Read {
			ids = append(ids, summary.ID)
		}
	}
	if len(ids) == 0 {
		return 0, nil
	}

	events, err := r.Bus.Read(ctx, "messages", ids, agentID)
	if err != nil {
		return 0, err
	}
	byID := map[string]eventbus.Event{}
	for _, evt := range events {
		byID[evt.ID] = evt
	}

	unread := make([]eventbus.Event, 0, len(summaries))
	for _, summary := range summaries {
		if summary.Read {
			continue
		}
		evt, ok := byID[summary.ID]
		if !ok {
			continue
		}
		unread = append(unread, evt)
	}
	sort.SliceStable(unread, func(i, j int) bool {
		pi := eventPriority(unread[i].Metadata)
		pj := eventPriority(unread[j].Metadata)
		if priorityRank(pi) != priorityRank(pj) {
			return priorityRank(pi) < priorityRank(pj)
		}
		if unread[i].CreatedAt.Equal(unread[j].CreatedAt) {
			return unread[i].ID < unread[j].ID
		}
		return unread[i].CreatedAt.Before(unread[j].CreatedAt)
	})

	processed := 0
	for _, evt := range unread {
		if r.handleMessageEvent(ctx, agentID, evt) {
			processed++
		}
	}
	return processed, nil
}

func (r *Runtime) replayUnreadWakeEvents(ctx context.Context, agentID string, limit int) (int, error) {
	if r.Bus == nil {
		return 0, nil
	}
	events, err := r.collectUnreadContextEvents(ctx, agentID, limit)
	if err != nil {
		return 0, err
	}
	if len(events) == 0 {
		return 0, nil
	}
	sort.SliceStable(events, func(i, j int) bool {
		pi := eventPriority(events[i].Metadata)
		pj := eventPriority(events[j].Metadata)
		if priorityRank(pi) != priorityRank(pj) {
			return priorityRank(pi) < priorityRank(pj)
		}
		if events[i].CreatedAt.Equal(events[j].CreatedAt) {
			return events[i].ID < events[j].ID
		}
		return events[i].CreatedAt.Before(events[j].CreatedAt)
	})
	for _, evt := range events {
		priority := eventPriority(evt.Metadata)
		if priority != "wake" && priority != "interrupt" {
			continue
		}
		if r.isInternalLLMWakeEvent(ctx, agentID, evt) {
			r.ackContextEvents(context.Background(), agentID, []eventbus.Event{evt})
			continue
		}
		meta := map[string]any{
			"priority": priority,
			"stream":   evt.Stream,
			"event_id": evt.ID,
			"kind":     "wake",
			"source":   "runtime",
		}
		if _, err := r.HandleMessage(ctx, agentID, "", "", meta); err != nil {
			return 0, nil
		}
		return 1, nil
	}
	return 0, nil
}

func (r *Runtime) isInternalLLMWakeEvent(ctx context.Context, agentID string, evt eventbus.Event) bool {
	if r.Tasks == nil || evt.Stream != "task_output" {
		return false
	}
	taskID := getMetaString(evt.Metadata, "task_id")
	if taskID == "" {
		return false
	}
	task, err := r.Tasks.Get(ctx, taskID)
	if err != nil {
		return false
	}
	if task.Type != "llm" {
		return false
	}
	if task.Owner == "" {
		return true
	}
	return task.Owner == agentID
}

func (r *Runtime) handleMessageEvent(ctx context.Context, agentID string, evt eventbus.Event) bool {
	if r.Bus == nil {
		return false
	}
	if r.messageAlreadyAcked(ctx, agentID, evt.ID) {
		return false
	}
	if !eventTargetsAgent(evt, agentID) {
		return false
	}
	if strings.TrimSpace(evt.Body) == "" {
		_ = r.Bus.Ack(ctx, "messages", []string{evt.ID}, agentID)
		return true
	}
	source := ""
	if evt.Metadata != nil {
		if val, ok := evt.Metadata["source"].(string); ok {
			source = val
		}
	}
	if source == "" {
		source = "external"
	}
	meta := map[string]any{
		"event_id": evt.ID,
	}
	for k, v := range evt.Metadata {
		meta[k] = v
	}
	if _, err := r.HandleMessage(ctx, agentID, source, evt.Body, meta); err != nil {
		return false
	}
	_ = r.Bus.Ack(ctx, "messages", []string{evt.ID}, agentID)
	return true
}

func (r *Runtime) messageAlreadyAcked(ctx context.Context, agentID, eventID string) bool {
	if r.Bus == nil || agentID == "" || eventID == "" {
		return false
	}
	events, err := r.Bus.Read(ctx, "messages", []string{eventID}, agentID)
	if err != nil || len(events) == 0 {
		return false
	}
	return events[0].Read
}

var contextEventStreams = []string{"task_output", "signals", "errors", "external"}

func eventPriority(metadata map[string]any) string {
	if metadata == nil {
		return "normal"
	}
	if val, ok := metadata["priority"].(string); ok {
		switch strings.ToLower(strings.TrimSpace(val)) {
		case "interrupt", "wake", "normal", "low":
			return strings.ToLower(strings.TrimSpace(val))
		}
	}
	return "normal"
}

func priorityRank(priority string) int {
	switch priority {
	case "interrupt":
		return 0
	case "wake":
		return 1
	case "normal":
		return 2
	case "low":
		return 3
	default:
		return 2
	}
}

func getMetaString(meta map[string]any, key string) string {
	if meta == nil {
		return ""
	}
	val, ok := meta[key]
	if !ok {
		return ""
	}
	str, ok := val.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(str)
}

func (r *Runtime) nextTurnContext(agentID string, now time.Time) TurnContext {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	r.turnMu.Lock()
	previous := r.lastTurnStart[agentID]
	r.lastTurnStart[agentID] = now
	r.turnMu.Unlock()

	ctx := TurnContext{
		Now:      now,
		Previous: previous,
	}
	if !previous.IsZero() {
		ctx.Elapsed = now.Sub(previous)
		ctx.TimePassed = ctx.Elapsed >= minTimePassedDelta
		ctx.DateChanged = previous.UTC().Format("2006-01-02") != now.Format("2006-01-02")
	}
	return ctx
}

func (r *Runtime) appendContextUpdateHistory(
	ctx context.Context,
	agentID, taskID string,
	generation int64,
	turnCtx TurnContext,
	events []eventbus.Event,
) {
	if strings.TrimSpace(agentID) == "" {
		return
	}
	if !turnCtx.Previous.IsZero() && turnCtx.TimePassed {
		r.appendHistory(ctx, agentID, "system_update", "system", "time passed", taskID, generation, map[string]any{
			"kind":            "time_passed",
			"previous":        turnCtx.Previous.UTC().Format(time.RFC3339),
			"current":         turnCtx.Now.UTC().Format(time.RFC3339),
			"elapsed_seconds": int64(turnCtx.Elapsed.Seconds()),
		})
	}
	if turnCtx.DateChanged {
		r.appendHistory(ctx, agentID, "system_update", "system", "date changed", taskID, generation, map[string]any{
			"kind":          "date_changed",
			"previous_date": turnCtx.Previous.UTC().Format("2006-01-02"),
			"current_date":  turnCtx.Now.UTC().Format("2006-01-02"),
		})
	}
	for _, evt := range events {
		priority := eventPriority(evt.Metadata)
		subject := clipText(strings.TrimSpace(evt.Subject), maxContextEventBody)
		body := clipText(strings.TrimSpace(evt.Body), maxContextEventBody)
		content := subject
		if content == "" {
			content = body
		}
		if content == "" {
			content = fmt.Sprintf("%s event", strings.TrimSpace(evt.Stream))
		}
		data := map[string]any{
			"kind":       "context_event",
			"stream":     strings.TrimSpace(evt.Stream),
			"event_id":   strings.TrimSpace(evt.ID),
			"priority":   priority,
			"subject":    subject,
			"body":       body,
			"created_at": evt.CreatedAt.UTC().Format(time.RFC3339Nano),
		}
		if metadata := previewJSON(evt.Metadata, maxContextEventData); metadata != "" {
			data["metadata"] = metadata
		}
		if payload := previewJSON(evt.Payload, maxContextEventData); payload != "" {
			data["payload"] = payload
		}
		r.appendHistory(ctx, agentID, "context_event", "system", content, taskID, generation, data)
	}
}

func (r *Runtime) collectUnreadContextEvents(ctx context.Context, agentID string, limit int) ([]eventbus.Event, error) {
	if r.Bus == nil || strings.TrimSpace(agentID) == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = maxContextEventsPerTurn * 2
	}

	idsByStream := map[string][]string{}
	for _, stream := range contextEventStreams {
		summaries, err := r.Bus.List(ctx, stream, eventbus.ListOptions{
			Reader: agentID,
			Limit:  limit,
			Order:  "lifo",
		})
		if err != nil {
			return nil, err
		}
		for _, summary := range summaries {
			if summary.Read {
				continue
			}
			idsByStream[stream] = append(idsByStream[stream], summary.ID)
		}
	}

	seen := map[string]struct{}{}
	out := make([]eventbus.Event, 0, limit)
	for stream, ids := range idsByStream {
		events, err := r.Bus.Read(ctx, stream, ids, agentID)
		if err != nil {
			return nil, err
		}
		for _, evt := range events {
			if evt.Read {
				continue
			}
			if !eventTargetsAgent(evt, agentID) {
				continue
			}
			if isContextNoiseEvent(evt) {
				_ = r.Bus.Ack(ctx, evt.Stream, []string{evt.ID}, agentID)
				continue
			}
			if r.isInternalLLMWakeEvent(ctx, agentID, evt) {
				_ = r.Bus.Ack(ctx, evt.Stream, []string{evt.ID}, agentID)
				continue
			}
			key := evt.Stream + ":" + evt.ID
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, evt)
		}
	}
	return out, nil
}

func isContextNoiseEvent(evt eventbus.Event) bool {
	if evt.Stream != "signals" {
		return false
	}
	kind := strings.ToLower(strings.TrimSpace(getMetaString(evt.Metadata, "kind")))
	subject := strings.ToLower(strings.TrimSpace(evt.Subject))
	if kind == "llm_debug" {
		return true
	}
	if strings.HasPrefix(subject, "llm_debug_") {
		return true
	}
	return false
}

func selectContextEventsForPrompt(events []eventbus.Event, limit int) []eventbus.Event {
	if len(events) == 0 {
		return nil
	}
	if limit <= 0 || len(events) <= limit {
		out := append([]eventbus.Event{}, events...)
		sort.SliceStable(out, func(i, j int) bool {
			if out[i].CreatedAt.Equal(out[j].CreatedAt) {
				return out[i].ID < out[j].ID
			}
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		})
		return out
	}

	scored := append([]eventbus.Event{}, events...)
	sort.SliceStable(scored, func(i, j int) bool {
		pi := eventPriority(scored[i].Metadata)
		pj := eventPriority(scored[j].Metadata)
		if priorityRank(pi) != priorityRank(pj) {
			return priorityRank(pi) < priorityRank(pj)
		}
		if scored[i].CreatedAt.Equal(scored[j].CreatedAt) {
			return scored[i].ID < scored[j].ID
		}
		return scored[i].CreatedAt.After(scored[j].CreatedAt)
	})
	scored = scored[:limit]
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].CreatedAt.Equal(scored[j].CreatedAt) {
			return scored[i].ID < scored[j].ID
		}
		return scored[i].CreatedAt.Before(scored[j].CreatedAt)
	})
	return scored
}

func (r *Runtime) ackContextEvents(ctx context.Context, agentID string, events []eventbus.Event) {
	if r.Bus == nil || strings.TrimSpace(agentID) == "" || len(events) == 0 {
		return
	}
	idsByStream := map[string][]string{}
	for _, evt := range events {
		if evt.Stream == "" || evt.ID == "" {
			continue
		}
		idsByStream[evt.Stream] = append(idsByStream[evt.Stream], evt.ID)
	}
	for stream, ids := range idsByStream {
		if len(ids) == 0 {
			continue
		}
		_ = r.Bus.Ack(ctx, stream, uniqueStrings(ids), agentID)
	}
}

func buildInputWithHistory(turns []ConversationTurn, source, message string, metadata map[string]any, turnCtx TurnContext, contextEvents []eventbus.Event) string {
	var b strings.Builder
	b.Grow(maxHistoryBytes + 3000)
	if len(turns) > 0 {
		b.WriteString("Recent context:\n")
		for _, turn := range turns {
			if turn.Source != "" {
				b.WriteString("- source: ")
				b.WriteString(turn.Source)
				if turn.Priority != "" {
					b.WriteString(" (")
					b.WriteString(turn.Priority)
					b.WriteString(")")
				}
				b.WriteString("\n")
			}
			if turn.Input != "" {
				b.WriteString("  input: ")
				b.WriteString(turn.Input)
				b.WriteString("\n")
			}
			if turn.Output != "" {
				b.WriteString("  output: ")
				b.WriteString(turn.Output)
				b.WriteString("\n")
			}
			if b.Len() >= maxHistoryBytes {
				break
			}
		}
		b.WriteString("\n")
	}

	b.WriteString(renderContextUpdatesXML(turnCtx, contextEvents))
	b.WriteString("\n")
	b.WriteString("Current input:\n")
	b.WriteString("- source: ")
	if source == "" {
		source = "external"
	}
	b.WriteString(source)
	b.WriteString(" (")
	b.WriteString(eventPriority(metadata))
	b.WriteString(")\n")
	b.WriteString("- message: ")
	b.WriteString(message)
	return b.String()
}

func renderContextUpdatesXML(turnCtx TurnContext, events []eventbus.Event) string {
	var b strings.Builder
	b.WriteString("<context_updates generated_at=\"")
	b.WriteString(turnCtx.Now.UTC().Format(time.RFC3339))
	b.WriteString("\"")
	if !turnCtx.Previous.IsZero() {
		b.WriteString(" elapsed_seconds=\"")
		b.WriteString(fmt.Sprintf("%d", int64(turnCtx.Elapsed.Seconds())))
		b.WriteString("\"")
	}
	b.WriteString(">\n")

	if !turnCtx.Previous.IsZero() && turnCtx.TimePassed {
		b.WriteString("  <system_update kind=\"time_passed\" previous=\"")
		b.WriteString(turnCtx.Previous.UTC().Format(time.RFC3339))
		b.WriteString("\" current=\"")
		b.WriteString(turnCtx.Now.UTC().Format(time.RFC3339))
		b.WriteString("\" elapsed_seconds=\"")
		b.WriteString(fmt.Sprintf("%d", int64(turnCtx.Elapsed.Seconds())))
		b.WriteString("\" />\n")
	}
	if turnCtx.DateChanged {
		b.WriteString("  <system_update kind=\"date_changed\" previous_date=\"")
		b.WriteString(turnCtx.Previous.UTC().Format("2006-01-02"))
		b.WriteString("\" current_date=\"")
		b.WriteString(turnCtx.Now.UTC().Format("2006-01-02"))
		b.WriteString("\" />\n")
	}

	for _, evt := range events {
		priority := eventPriority(evt.Metadata)
		b.WriteString("  <event stream=\"")
		b.WriteString(xmlEscape(evt.Stream))
		b.WriteString("\" id=\"")
		b.WriteString(xmlEscape(evt.ID))
		b.WriteString("\" priority=\"")
		b.WriteString(xmlEscape(priority))
		b.WriteString("\" created_at=\"")
		b.WriteString(evt.CreatedAt.UTC().Format(time.RFC3339))
		b.WriteString("\">\n")
		if strings.TrimSpace(evt.Subject) != "" {
			b.WriteString("    <subject>")
			b.WriteString(xmlEscape(clipText(strings.TrimSpace(evt.Subject), maxContextEventBody)))
			b.WriteString("</subject>\n")
		}
		if strings.TrimSpace(evt.Body) != "" {
			b.WriteString("    <body>")
			b.WriteString(xmlEscape(clipText(strings.TrimSpace(evt.Body), maxContextEventBody)))
			b.WriteString("</body>\n")
		}
		if metadata := previewJSON(evt.Metadata, maxContextEventData); metadata != "" {
			b.WriteString("    <metadata>")
			b.WriteString(xmlEscape(metadata))
			b.WriteString("</metadata>\n")
		}
		if payload := previewJSON(evt.Payload, maxContextEventData); payload != "" {
			b.WriteString("    <payload>")
			b.WriteString(xmlEscape(payload))
			b.WriteString("</payload>\n")
		}
		b.WriteString("  </event>\n")
	}
	b.WriteString("</context_updates>")
	return b.String()
}

func previewJSON(v any, limit int) string {
	if v == nil {
		return ""
	}
	data, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return clipText(strings.TrimSpace(string(data)), limit)
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, raw := range values {
		val := strings.TrimSpace(raw)
		if val == "" {
			continue
		}
		if _, ok := seen[val]; ok {
			continue
		}
		seen[val] = struct{}{}
		out = append(out, val)
	}
	return out
}

func xmlEscape(v string) string {
	if v == "" {
		return ""
	}
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		"\"", "&quot;",
		"'", "&apos;",
	)
	return replacer.Replace(v)
}

func (r *Runtime) loadRecentConversation(ctx context.Context, agentID string, limit int) []ConversationTurn {
	if r.Tasks == nil || strings.TrimSpace(agentID) == "" || limit <= 0 {
		return nil
	}
	cutoff := r.compactionCutoff(agentID)
	currentGeneration := r.currentHistoryGeneration(ctx, agentID)
	tasksList, err := r.Tasks.List(ctx, tasks.ListFilter{
		Type:  "llm",
		Owner: agentID,
		Limit: limit * 4,
	})
	if err != nil || len(tasksList) == 0 {
		return nil
	}
	sort.Slice(tasksList, func(i, j int) bool {
		if tasksList[i].CreatedAt.Equal(tasksList[j].CreatedAt) {
			return tasksList[i].ID < tasksList[j].ID
		}
		return tasksList[i].CreatedAt.Before(tasksList[j].CreatedAt)
	})
	out := make([]ConversationTurn, 0, limit)
	for _, task := range tasksList {
		if !cutoff.IsZero() && task.CreatedAt.Before(cutoff) {
			continue
		}
		if taskGen := anyToInt64(task.Metadata["history_generation"]); taskGen > 0 && taskGen != currentGeneration {
			continue
		}
		source := strings.TrimSpace(getMetaString(task.Metadata, "source"))
		if source == "" {
			source = "external"
		}
		priority := eventPriority(task.Metadata)
		if priority == "low" {
			continue
		}
		input := ""
		updates, err := r.Tasks.ListUpdates(ctx, task.ID, 12)
		if err == nil {
			for _, upd := range updates {
				if upd.Kind != "input" || upd.Payload == nil {
					continue
				}
				if msg, ok := upd.Payload["message"].(string); ok {
					input = msg
					break
				}
			}
		}
		output := ""
		if task.Result != nil {
			if msg, ok := task.Result["output"].(string); ok {
				output = msg
			}
		}
		if strings.TrimSpace(input) == "" && strings.TrimSpace(output) == "" {
			continue
		}
		out = append(out, ConversationTurn{
			Source:    source,
			Input:     clipText(strings.TrimSpace(input), maxHistoryInputChars),
			Output:    clipText(strings.TrimSpace(output), maxHistoryOutputChars),
			Priority:  priority,
			CreatedAt: task.CreatedAt,
		})
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

func clipText(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return strings.TrimSpace(text[:limit]) + " …"
}
