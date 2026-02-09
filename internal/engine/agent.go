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
	"github.com/flitsinc/go-agents/internal/schema"
	"github.com/flitsinc/go-agents/internal/tasks"
	"github.com/flitsinc/go-llms/content"
	"github.com/flitsinc/go-llms/llms"
	llmtools "github.com/flitsinc/go-llms/tools"
)

type Session struct {
	TaskID     string    `json:"task_id"`
	LLMTaskID  string    `json:"llm_task_id,omitempty"`
	Prompt     string    `json:"prompt"`
	LastInput  string    `json:"last_input"`
	LastOutput string    `json:"last_output"`
	LastError  string    `json:"last_error,omitempty"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type taskConfig struct {
	System string
	Model  string
	mu     sync.Mutex
}

type TurnContext struct {
	Now         time.Time
	Previous    time.Time
	TimePassed  bool
	Elapsed     time.Duration
	DateChanged bool
}

type ContextUpdateFrame struct {
	Events      []eventbus.Event
	FromEventID string
	ToEventID   string
	Scanned     int
	Emitted     int
	Superseded  int
}

type Runtime struct {
	Bus         *eventbus.Bus
	Tasks       *tasks.Manager
	LLM         *ai.Client
	Context     *agentctx.Manager
	LLMFactory  func() (*llms.LLM, error)
	LLMDebugDir string

	baseCtx context.Context
	loopMu  sync.Mutex
	loops   map[string]context.CancelFunc

	mu       sync.RWMutex
	sessions map[string]Session

	configMu    sync.RWMutex
	taskConfigs map[string]*taskConfig

	inflightMu sync.Mutex
	inflight   map[string]context.CancelFunc

	wakeMu   sync.Mutex
	lastWake map[string]time.Time

	turnMu        sync.Mutex
	lastTurnStart map[string]time.Time

	contextCursorMu        sync.Mutex
	lastContextCursorByTask map[string]string

	historyMu               sync.Mutex
	historyGenerationByTask map[string]int64
	historyPreambleByTask   map[string]int64

	nowFn func() time.Time
}

const (
	maxToolContentChars = 1200

	maxContextEventsPerTurn = 24
	maxContextEventBodyWake = 500
	maxContextEventBodyBase = 200
	maxContextEventData     = 900
	minTimePassedDelta      = 60 * time.Second
)

type Option func(*Runtime)

func WithClock(nowFn func() time.Time) Option {
	return func(r *Runtime) {
		if nowFn != nil {
			r.nowFn = nowFn
		}
	}
}

func NewRuntime(bus *eventbus.Bus, tasksMgr *tasks.Manager, client *ai.Client, opts ...Option) *Runtime {
	home, err := goagents.EnsureHome()
	if err != nil {
		home = ""
	}
	ctxMgr := &agentctx.Manager{Home: home}

	r := &Runtime{
		Bus:                     bus,
		Tasks:                   tasksMgr,
		LLM:                     client,
		Context:                 ctxMgr,
		loops:                   map[string]context.CancelFunc{},
		sessions:                map[string]Session{},
		taskConfigs:             map[string]*taskConfig{},
		inflight:                map[string]context.CancelFunc{},
		lastWake:                map[string]time.Time{},
		lastTurnStart:           map[string]time.Time{},
		lastContextCursorByTask: map[string]string{},
		historyGenerationByTask: map[string]int64{},
		historyPreambleByTask:   map[string]int64{},
		nowFn:                   func() time.Time { return time.Now().UTC() },
	}
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}
	return r
}

func (r *Runtime) now() time.Time {
	if r.nowFn == nil {
		return time.Now().UTC()
	}
	return r.nowFn().UTC()
}

func (r *Runtime) SetLLMDebugDir(dir string) {
	if r == nil {
		return
	}
	r.LLMDebugDir = strings.TrimSpace(dir)
}

func (r *Runtime) contextCursor(agentID string) string {
	if strings.TrimSpace(agentID) == "" {
		return ""
	}
	r.contextCursorMu.Lock()
	defer r.contextCursorMu.Unlock()
	return strings.TrimSpace(r.lastContextCursorByTask[agentID])
}

func (r *Runtime) advanceContextCursor(agentID string, events []eventbus.Event) {
	if strings.TrimSpace(agentID) == "" || len(events) == 0 {
		return
	}
	maxID := ""
	for _, evt := range events {
		id := strings.TrimSpace(evt.ID)
		if id == "" {
			continue
		}
		if maxID == "" || id > maxID {
			maxID = id
		}
	}
	if maxID == "" {
		return
	}
	r.contextCursorMu.Lock()
	defer r.contextCursorMu.Unlock()
	if existing := strings.TrimSpace(r.lastContextCursorByTask[agentID]); existing != "" && existing >= maxID {
		return
	}
	r.lastContextCursorByTask[agentID] = maxID
}

func (r *Runtime) Start(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	r.baseCtx = ctx
	if r.Tasks != nil {
		r.recoverStaleTasks(ctx)
	}
	if r.Tasks != nil && r.Bus != nil {
		go r.monitorTaskHealth(ctx)
	}
}

// recoverStaleTasks fails any tasks left in "running" state from a previous
// process. After a restart, no goroutine owns these tasks, so they would
// remain stuck forever.
func (r *Runtime) recoverStaleTasks(ctx context.Context) {
	for _, taskType := range []string{"llm", "exec"} {
		stale, err := r.Tasks.List(ctx, tasks.ListFilter{
			Status: tasks.StatusRunning,
			Type:   taskType,
			Limit:  500,
		})
		if err != nil {
			continue
		}
		for _, t := range stale {
			_ = r.Tasks.Fail(ctx, t.ID, "recovered: task was still running when the runtime restarted")
		}
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

	now := r.now()
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
		scopeType := "task"
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
			Stream:    schema.StreamTaskInput,
			ScopeType: "task",
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

func (r *Runtime) EnsureAgentLoop(taskID string) {
	if taskID == "" {
		return
	}
	r.loopMu.Lock()
	if _, ok := r.loops[taskID]; ok {
		r.loopMu.Unlock()
		return
	}
	base := r.baseCtx
	if base == nil {
		base = context.Background()
	}
	loopCtx, cancel := context.WithCancel(base)
	r.loops[taskID] = cancel
	r.loopMu.Unlock()

	go func() {
		_ = r.Run(loopCtx, taskID)
		r.loopMu.Lock()
		delete(r.loops, taskID)
		r.loopMu.Unlock()
	}()
}

func (r *Runtime) ensureTaskConfig(taskID string) *taskConfig {
	if taskID == "" {
		return nil
	}
	r.configMu.Lock()
	cfg, ok := r.taskConfigs[taskID]
	if !ok {
		cfg = &taskConfig{}
		r.taskConfigs[taskID] = cfg
	}
	r.configMu.Unlock()
	return cfg
}

func (r *Runtime) ensureAgentLLM(cfg *taskConfig) (*llms.LLM, error) {
	if r.LLMFactory != nil {
		return r.LLMFactory()
	}
	if r.LLM != nil {
		if cfg != nil && cfg.Model != "" {
			if llm, err := r.LLM.NewSessionWithModel(cfg.Model); err == nil {
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

func (r *Runtime) SetPromptTools(toolNames []string) {
	normalized := normalizePromptToolNames(toolNames)
	if r.Context != nil {
		r.Context.ToolNames = append([]string{}, normalized...)
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

func (r *Runtime) SetAgentSystem(taskID, system string) {
	if taskID == "" {
		return
	}
	cfg := r.ensureTaskConfig(taskID)
	if cfg == nil {
		return
	}
	cfg.mu.Lock()
	cfg.System = strings.TrimSpace(system)
	cfg.mu.Unlock()
}

func (r *Runtime) SetAgentModel(taskID, model string) {
	if taskID == "" {
		return
	}
	cfg := r.ensureTaskConfig(taskID)
	if cfg == nil {
		return
	}
	cfg.mu.Lock()
	cfg.Model = strings.TrimSpace(model)
	cfg.mu.Unlock()
}

func (r *Runtime) EnsureRootTask(ctx context.Context, taskID string) (tasks.Task, error) {
	if r.Tasks == nil {
		return tasks.Task{}, fmt.Errorf("task manager unavailable")
	}
	if taskID == "" {
		return tasks.Task{}, fmt.Errorf("task_id is required")
	}
	return r.ensureRootTask(ctx, taskID)
}

func (r *Runtime) RunOnce(ctx context.Context, taskID, message string) (Session, error) {
	return r.HandleMessage(ctx, taskID, "", message, nil)
}

func (r *Runtime) ensureRootTask(ctx context.Context, taskID string) (tasks.Task, error) {
	if r.Tasks == nil {
		return tasks.Task{}, fmt.Errorf("task manager unavailable")
	}
	if strings.TrimSpace(taskID) == "" {
		return tasks.Task{}, fmt.Errorf("task_id is required")
	}
	// The agent IS the task — look up by ID directly.
	// Tasks must be created explicitly via createAgent / handleCreateTask.
	task, err := r.Tasks.Get(ctx, taskID)
	if err == nil && task.ID != "" {
		return task, nil
	}
	return tasks.Task{}, fmt.Errorf("agent task %q not found — create it first", taskID)
}

func (r *Runtime) HandleMessage(ctx context.Context, agentID, source, message string, messageMeta map[string]any) (Session, error) {
	if agentID == "" {
		return Session{}, fmt.Errorf("task_id is required")
	}
	ctx = agentcontext.WithTaskID(ctx, agentID)
	bgCtx := agentcontext.WithTaskID(context.Background(), agentID)
	cfg := r.ensureTaskConfig(agentID)
	currentGeneration := r.historyGeneration(ctx, agentID)

	var promptContent content.Content
	var promptText string
	if r.Context != nil {
		prompt, text, err := r.Context.BuildSystemPrompt(ctx, r.Bus)
		if err != nil {
			return Session{}, err
		}
		promptContent = prompt
		promptText = text
	} else {
		return Session{}, fmt.Errorf("prompt unavailable")
	}
	if cfg != nil && cfg.System != "" {
		promptText = fmt.Sprintf("%s\n\n%s", promptText, cfg.System)
		promptContent = content.FromText(promptText)
	}

	// Load prior conversation messages and the stored system prompt for
	// this generation. Using the stored prompt across all turns of a
	// generation keeps the provider's prompt-cache key stable.
	storedPrompt, priorMessages, _ := r.loadConversationMessages(ctx, agentID, currentGeneration)
	if storedPrompt != "" {
		promptText = storedPrompt
		promptContent = content.FromText(storedPrompt)
	}

	var rootTask tasks.Task
	var llmTask tasks.Task
	taskID := agentID
	if r.Tasks != nil {
		rootTask, _ = r.ensureRootTask(ctx, agentID)
		if rootTask.ID != "" {
			taskID = rootTask.ID
		}

		llmTask, _ = r.Tasks.Spawn(ctx, tasks.Spec{
			Type:     "llm",
			Owner:    taskID,
			ParentID: rootTask.ID,
			Mode:     "sync",
			Metadata: map[string]any{
				"input_target":       taskID,
				"notify_target":      taskID,
				"source":             source,
				"priority":           eventPriority(messageMeta),
				"request_id":         schema.GetMetaString(messageMeta, "request_id"),
				"event_id":           schema.GetMetaString(messageMeta, "event_id"),
				"history_generation": currentGeneration,
			},
		})
		_ = r.Tasks.MarkRunning(ctx, llmTask.ID)
		_ = r.Tasks.Send(ctx, llmTask.ID, map[string]any{"message": message})
	}

	session := Session{
		TaskID:    taskID,
		LLMTaskID: llmTask.ID,
		Prompt:    promptText,
		LastInput: message,
		UpdatedAt: r.now(),
	}
	turnCtx := r.nextTurnContext(agentID, session.UpdatedAt)
	rawContextEvents, _ := r.collectUnreadContextEvents(ctx, agentID, maxContextEventsPerTurn*2)
	contextEvents, initialSuperseded := projectContextEventsForPrompt(rawContextEvents, maxContextEventsPerTurn)
	currentContextCursor := r.contextCursor(agentID)
	initialFrame := ContextUpdateFrame{
		Events:      contextEvents,
		FromEventID: currentContextCursor,
		ToEventID:   maxEventID(rawContextEvents),
		Scanned:     len(rawContextEvents),
		Emitted:     len(contextEvents),
		Superseded:  initialSuperseded,
	}
	if initialFrame.ToEventID != "" {
		currentContextCursor = initialFrame.ToEventID
	}
	toolsSnapshot := []string{}
	if r.Context != nil && len(r.Context.ToolNames) > 0 {
		toolsSnapshot = append(toolsSnapshot, r.Context.ToolNames...)
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
			"request_id": schema.GetMetaString(messageMeta, "request_id"),
			"event_id":   schema.GetMetaString(messageMeta, "event_id"),
		})
	} else if strings.EqualFold(schema.GetMetaString(messageMeta, "kind"), "wake") {
		r.appendHistory(ctx, agentID, "wake", "system", "wake turn", llmTask.ID, currentGeneration, map[string]any{
			"stream":   schema.GetMetaString(messageMeta, "stream"),
			"event_id": schema.GetMetaString(messageMeta, "event_id"),
			"priority": eventPriority(messageMeta),
		})
	}
	r.appendContextUpdateHistory(ctx, agentID, llmTask.ID, currentGeneration, turnCtx, contextEvents)

	if r.Bus != nil {
		_, _ = r.Bus.Push(ctx, eventbus.EventInput{
			Stream:    "signals",
			Subject:   "agent_run_start",
			Body:      "agent run started",
			ScopeType: "task",
			ScopeID:   agentID,
			Metadata: map[string]any{
				"agent_id": agentID,
			},
			SourceID: agentID,
		})
	}

	llmClient, err := r.ensureAgentLLM(cfg)
	if err != nil || llmClient == nil {
		session.LastError = "LLM not configured. Set the provider API key (e.g. GO_AGENTS_ANTHROPIC_API_KEY) and configure llm_provider/llm_model in config.json."
		session.LastOutput = session.LastError
		r.SetSession(session)
		r.appendHistory(ctx, agentID, "assistant_message", "assistant", session.LastOutput, llmTask.ID, currentGeneration, map[string]any{
			"error": true,
		})
		if r.Tasks != nil {
			if llmTask.ID != "" {
				_ = r.Tasks.Fail(bgCtx, llmTask.ID, session.LastError)
			}
		}
		if source != "" && source != agentID {
			_, _ = r.SendMessageWithMeta(ctx, source, session.LastOutput, agentID, nil)
		}
		r.ackContextEvents(bgCtx, agentID, rawContextEvents)
		return session, nil
	}
	r.attachDebugger(llmClient, agentID, llmTask.ID)

	var output string
	trackedContextEvents := make([]eventbus.Event, 0, len(rawContextEvents))
	trackedContextEventKeys := map[string]struct{}{}
	contextEventKey := func(evt eventbus.Event) string {
		stream := strings.TrimSpace(evt.Stream)
		id := strings.TrimSpace(evt.ID)
		if stream == "" || id == "" {
			return ""
		}
		return stream + ":" + id
	}
	markTrackedContextEvents := func(events []eventbus.Event) {
		for _, evt := range events {
			key := contextEventKey(evt)
			if key == "" {
				continue
			}
			if _, ok := trackedContextEventKeys[key]; ok {
				continue
			}
			trackedContextEventKeys[key] = struct{}{}
			trackedContextEvents = append(trackedContextEvents, evt)
		}
	}
	untrackedContextEvents := func(events []eventbus.Event) []eventbus.Event {
		if len(events) == 0 {
			return nil
		}
		out := make([]eventbus.Event, 0, len(events))
		for _, evt := range events {
			key := contextEventKey(evt)
			if key == "" {
				continue
			}
			if _, ok := trackedContextEventKeys[key]; ok {
				continue
			}
			out = append(out, evt)
		}
		return out
	}
	markTrackedContextEvents(rawContextEvents)

	{
		llmCtx := tasks.WithParentTaskID(ctx, llmTask.ID)
		llmCtx = tasks.WithIgnoredWakeEventIDs(llmCtx, ignoredWakeEventIDsForTurn(messageMeta, rawContextEvents))
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

		input := buildInputWithHistory(source, message, messageMeta, turnCtx, initialFrame)
		runSource := source
		if strings.TrimSpace(runSource) == "" {
			runSource = "external"
		}
		runPriority := eventPriority(messageMeta)
		lastContextSnapshotAt := turnCtx.Now
		lastLLMTurn := 1
		publishedAssistantTurns := map[int]struct{}{}
		publishedAssistantPrefix := ""
		publishAssistantTurn := func(turn int, text string, partial bool) {
			if turn <= 0 || strings.TrimSpace(text) == "" {
				return
			}
			if _, ok := publishedAssistantTurns[turn]; ok {
				return
			}
			data := map[string]any{
				"turn": turn,
			}
			if partial {
				data["partial"] = true
			}
			r.appendHistory(llmCtx, agentID, "assistant_message", "assistant", text, llmTask.ID, currentGeneration, data)
			publishedAssistantTurns[turn] = struct{}{}
			publishedAssistantPrefix += text
		}
		prevBeforeResponse := llmClient.BeforeResponse
		llmClient.BeforeResponse = func(hookCtx context.Context, before llms.BeforeResponseState) error {
			if prevBeforeResponse != nil {
				if err := prevBeforeResponse(hookCtx, before); err != nil {
					return err
				}
			}

			turnNumber := before.Turn()
			if turnNumber <= 0 {
				turnNumber = 1
			}
			lastLLMTurn = turnNumber
			if turnNumber > 1 {
				// Skip prior conversation history so we only capture
				// assistant text from the current HandleMessage call.
				currentMsgs := before.Messages()
				if n := len(priorMessages); n > 0 && len(currentMsgs) > n {
					currentMsgs = currentMsgs[n:]
				}
				publishAssistantTurn(turnNumber-1, latestAssistantText(currentMsgs), false)
			}

			turnInput := ""
			turnSource := runSource
			turnPriority := runPriority
			turnFromEventID := ""
			turnToEventID := ""
			turnScanned := 0
			turnEmitted := 0
			turnSuperseded := 0
			if turnNumber == 1 {
				turnInput = input
				turnFromEventID = initialFrame.FromEventID
				turnToEventID = initialFrame.ToEventID
				turnScanned = initialFrame.Scanned
				turnEmitted = initialFrame.Emitted
				turnSuperseded = initialFrame.Superseded
			} else {
				now := r.now()
				snapshot := TurnContext{
					Now:      now,
					Previous: lastContextSnapshotAt,
				}
				if !lastContextSnapshotAt.IsZero() {
					snapshot.Elapsed = now.Sub(lastContextSnapshotAt)
					snapshot.TimePassed = snapshot.Elapsed >= minTimePassedDelta
					snapshot.DateChanged = lastContextSnapshotAt.UTC().Format("2006-01-02") != now.Format("2006-01-02")
				}
				lastContextSnapshotAt = now

				pending, err := r.collectUnreadContextEvents(hookCtx, agentID, maxContextEventsPerTurn*2)
				if err != nil {
					return err
				}
				freshRaw := untrackedContextEvents(pending)
				fresh, superseded := projectContextEventsForPrompt(freshRaw, maxContextEventsPerTurn)
				frame := ContextUpdateFrame{
					Events:      fresh,
					FromEventID: currentContextCursor,
					ToEventID:   maxEventID(freshRaw),
					Scanned:     len(freshRaw),
					Emitted:     len(fresh),
					Superseded:  superseded,
				}
				if len(freshRaw) > 0 {
					markTrackedContextEvents(freshRaw)
				}
				if frame.ToEventID != "" {
					currentContextCursor = frame.ToEventID
				}
				if len(fresh) > 0 || snapshot.DateChanged {
					r.appendContextUpdateHistory(hookCtx, agentID, llmTask.ID, currentGeneration, snapshot, fresh)
					turnInput = buildInputWithHistory("runtime", "", map[string]any{"priority": "normal"}, snapshot, frame)
					before.Append(llms.Message{Role: "user", Content: content.FromText(turnInput)})
					turnSource = "runtime"
					turnPriority = "normal"
					turnFromEventID = frame.FromEventID
					turnToEventID = frame.ToEventID
					turnScanned = frame.Scanned
					turnEmitted = frame.Emitted
					turnSuperseded = frame.Superseded
				}
			}

			if strings.TrimSpace(turnInput) != "" {
				data := map[string]any{
					"source":   turnSource,
					"priority": turnPriority,
					"turn":     turnNumber,
				}
				if turnFromEventID != "" {
					data["from_event_id"] = turnFromEventID
				}
				if turnToEventID != "" {
					data["to_event_id"] = turnToEventID
				}
				if turnScanned > 0 {
					data["scanned"] = turnScanned
				}
				if turnEmitted > 0 {
					data["emitted"] = turnEmitted
				}
				if turnSuperseded > 0 {
					data["superseded"] = turnSuperseded
				}
				r.appendHistory(hookCtx, agentID, "llm_input", "system", turnInput, llmTask.ID, currentGeneration, data)
			}
			return nil
		}
		defer func() {
			llmClient.BeforeResponse = prevBeforeResponse
		}()

		allMessages := make([]llms.Message, 0, len(priorMessages)+1)
		allMessages = append(allMessages, priorMessages...)
		allMessages = append(allMessages, llms.Message{
			Role:    "user",
			Content: content.FromText(input),
		})
		updates := llmClient.ChatUsingMessages(llmCtx, allMessages)
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
				reasoning := u.Text
				if strings.TrimSpace(reasoning) != "" || strings.TrimSpace(u.ID) != "" {
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
			remainder := output
			remainder = strings.TrimPrefix(remainder, publishedAssistantPrefix)
			publishAssistantTurn(lastLLMTurn, remainder, true)
			r.appendHistory(llmCtx, agentID, "error", "system", err.Error(), llmTask.ID, currentGeneration, nil)
			if r.Tasks != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					if llmTask.ID != "" {
						_ = r.Tasks.Cancel(bgCtx, llmTask.ID, "interrupted")
					}
				} else {
					if llmTask.ID != "" {
						_ = r.Tasks.Fail(bgCtx, llmTask.ID, err.Error())
					}
					if rootTask.ID != "" {
						_ = r.Tasks.Fail(bgCtx, rootTask.ID, err.Error())
					}
				}
			}
			if r.Bus != nil {
				_, _ = r.Bus.Push(ctx, eventbus.EventInput{
					Stream:    "errors",
					Subject:   "agent_run_error",
					Body:      session.LastError,
					ScopeType: "task",
					ScopeID:   agentID,
					Metadata: map[string]any{
						"kind": "error",
					},
					SourceID: agentID,
				})
			}
			if source != "" && source != agentID {
				reply := output
				if reply == "" {
					reply = fmt.Sprintf("[error] %s", session.LastError)
				} else {
					reply = fmt.Sprintf("%s\n\n[error] %s", reply, session.LastError)
				}
				_, _ = r.SendMessageWithMeta(ctx, source, reply, agentID, nil)
			}
			return session, err
		}
		remainder := output
		remainder = strings.TrimPrefix(remainder, publishedAssistantPrefix)
		publishAssistantTurn(lastLLMTurn, remainder, false)
	}

	r.ackContextEvents(context.Background(), agentID, trackedContextEvents)
	session.LastOutput = output
	r.SetSession(session)
	if r.Tasks != nil {
		if llmTask.ID != "" {
			_ = r.Tasks.Complete(bgCtx, llmTask.ID, map[string]any{"output": output})
		}
		// Complete the root agent task so that await_task callers see
		// a terminal status and receive the output.  For long-running
		// agents this is a no-op after the first turn (already completed).
		if rootTask.ID != "" {
			_ = r.Tasks.Complete(bgCtx, rootTask.ID, map[string]any{"output": output})
		}
	}
	if r.Bus != nil {
		_, _ = r.Bus.Push(ctx, eventbus.EventInput{
			Stream:    "signals",
			Subject:   "agent_run_complete",
			Body:      "agent run complete",
			ScopeType: "task",
			ScopeID:   agentID,
			Metadata: map[string]any{
				"agent_id": agentID,
			},
			SourceID: agentID,
		})
	}
	if source != "" && source != agentID && strings.TrimSpace(output) != "" {
		_, _ = r.SendMessageWithMeta(ctx, source, output, agentID, nil)
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

func latestAssistantText(messages []llms.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if strings.TrimSpace(messages[i].Role) != "assistant" {
			continue
		}
		return textFromContent(messages[i].Content)
	}
	return ""
}

func textFromContent(items content.Content) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	for _, item := range items {
		if txt, ok := item.(*content.Text); ok {
			b.WriteString(txt.Text)
		}
	}
	return b.String()
}

func (r *Runtime) clearInflight(taskID string) {
	if taskID == "" {
		return
	}
	r.inflightMu.Lock()
	delete(r.inflight, taskID)
	r.inflightMu.Unlock()
}

func (r *Runtime) SendMessageWithMeta(ctx context.Context, target, body, source string, metadata map[string]any) (eventbus.Event, error) {
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
		Stream:    schema.StreamTaskInput,
		ScopeType: "task",
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
	sub := r.Bus.Subscribe(ctx, schema.AgentStreams)
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-sub:
			if !ok {
				return
			}
			if !eventTargetsTask(evt, agentID) {
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
	sub := r.Bus.Subscribe(ctx, []string{schema.StreamSignals})
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

func eventTargetsTask(evt eventbus.Event, agentID string) bool {
	if agentID == "" {
		return true
	}
	if evt.ScopeType == "task" {
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
	if session.TaskID == "" {
		return
	}
	r.mu.Lock()
	r.sessions[session.TaskID] = session
	r.mu.Unlock()
}

func (r *Runtime) SessionCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.sessions)
}

func (r *Runtime) Run(ctx context.Context, agentID string) error {
	if r.Bus == nil {
		return nil
	}
	if agentID == "" {
		return fmt.Errorf("agent_id is required")
	}
	sub := r.Bus.Subscribe(ctx, schema.AgentStreams)
	replayTicker := time.NewTicker(500 * time.Millisecond)
	defer replayTicker.Stop()

	for {
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
			if !eventTargetsTask(evt, agentID) {
				continue
			}
			if _, err := r.replayUnreadWakeEvents(ctx, agentID, maxContextEventsPerTurn*2); err != nil && ctx.Err() != nil {
				return ctx.Err()
			}
		}
	}
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
		pi := eventPriorityForEvent(events[i])
		pj := eventPriorityForEvent(events[j])
		if schema.ParsePriority(pi).Rank() != schema.ParsePriority(pj).Rank() {
			return schema.ParsePriority(pi).Rank() < schema.ParsePriority(pj).Rank()
		}
		if events[i].CreatedAt.Equal(events[j].CreatedAt) {
			return events[i].ID < events[j].ID
		}
		return events[i].CreatedAt.Before(events[j].CreatedAt)
	})
	for _, evt := range events {
		priority := eventPriorityForEvent(evt)
		if priority != "wake" && priority != "interrupt" {
			continue
		}
		meta := map[string]any{
			"priority": priority,
			"stream":   evt.Stream,
			"event_id": evt.ID,
		}
		for k, v := range evt.Metadata {
			meta[k] = v
		}
		if _, ok := meta["kind"]; !ok {
			meta["kind"] = "event"
		}
		source := schema.GetMetaString(evt.Metadata, "source")
		if source == "" {
			source = "runtime"
		}
		if _, err := r.HandleMessage(ctx, agentID, source, evt.Body, meta); err != nil {
			return 0, nil
		}
		return 1, nil
	}
	return 0, nil
}

func eventPriority(metadata map[string]any) string {
	return string(schema.ParsePriority(schema.GetMetaString(metadata, "priority")))
}

func eventPriorityForEvent(evt eventbus.Event) string {
	priority := eventPriority(evt.Metadata)
	if priority == "normal" && schema.GetMetaString(evt.Metadata, "kind") == "message" {
		if schema.GetMetaString(evt.Metadata, "priority") == "" {
			return "wake"
		}
	}
	return priority
}

func (r *Runtime) nextTurnContext(agentID string, now time.Time) TurnContext {
	if now.IsZero() {
		now = r.now()
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
			"previous_date": turnCtx.Previous.UTC().Format("Monday, 2006-01-02"),
			"current_date":  turnCtx.Now.UTC().Format("Monday, 2006-01-02"),
		})
	}
	for _, evt := range events {
		priority := eventPriorityForEvent(evt)
		subject := clipText(strings.TrimSpace(evt.Subject), contextEventBodyLimit(priority))
		body, _ := buildContextEventBody(evt, priority)
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
	for _, stream := range schema.AgentStreams {
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
			if !eventTargetsTask(evt, agentID) {
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

func selectContextEventsForPrompt(events []eventbus.Event, limit int) []eventbus.Event {
	if len(events) == 0 {
		return nil
	}
	orderForPrompt := func(a, b eventbus.Event) bool {
		pa := eventPriorityForEvent(a)
		pb := eventPriorityForEvent(b)
		if schema.ParsePriority(pa).Rank() != schema.ParsePriority(pb).Rank() {
			return schema.ParsePriority(pa).Rank() < schema.ParsePriority(pb).Rank()
		}
		if a.CreatedAt.Equal(b.CreatedAt) {
			return a.ID < b.ID
		}
		return a.CreatedAt.Before(b.CreatedAt)
	}
	orderForSelection := func(a, b eventbus.Event) bool {
		pa := eventPriorityForEvent(a)
		pb := eventPriorityForEvent(b)
		if schema.ParsePriority(pa).Rank() != schema.ParsePriority(pb).Rank() {
			return schema.ParsePriority(pa).Rank() < schema.ParsePriority(pb).Rank()
		}
		if a.CreatedAt.Equal(b.CreatedAt) {
			return a.ID < b.ID
		}
		return a.CreatedAt.After(b.CreatedAt)
	}
	if limit <= 0 || len(events) <= limit {
		out := append([]eventbus.Event{}, events...)
		sort.SliceStable(out, func(i, j int) bool { return orderForPrompt(out[i], out[j]) })
		return out
	}

	scored := append([]eventbus.Event{}, events...)
	sort.SliceStable(scored, func(i, j int) bool { return orderForSelection(scored[i], scored[j]) })
	scored = scored[:limit]
	sort.SliceStable(scored, func(i, j int) bool { return orderForPrompt(scored[i], scored[j]) })
	return scored
}

func maxEventID(events []eventbus.Event) string {
	maxID := ""
	for _, evt := range events {
		id := strings.TrimSpace(evt.ID)
		if id == "" {
			continue
		}
		if maxID == "" || id > maxID {
			maxID = id
		}
	}
	return maxID
}

func copyMapAny(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func isActionablePromptEvent(evt eventbus.Event) bool {
	priority := eventPriorityForEvent(evt)
	if priority == "wake" || priority == "interrupt" {
		return true
	}
	if evt.Stream == "errors" || schema.GetMetaString(evt.Metadata, "kind") == "message" {
		return true
	}
	if evt.Stream == "task_output" {
		switch strings.ToLower(strings.TrimSpace(schema.GetMetaString(evt.Metadata, "task_kind"))) {
		case "completed", "failed", "cancelled", "killed":
			return true
		}
	}
	return false
}

func aggregateTaskOutputEvents(events []eventbus.Event) (eventbus.Event, int) {
	if len(events) == 0 {
		return eventbus.Event{}, 0
	}
	if len(events) == 1 {
		return events[0], 0
	}
	latest := events[len(events)-1]
	taskID := schema.GetMetaString(latest.Metadata, "task_id")
	kinds := make([]string, 0, len(events))
	seen := map[string]struct{}{}
	for _, evt := range events {
		kind := strings.ToLower(strings.TrimSpace(schema.GetMetaString(evt.Metadata, "task_kind")))
		if kind == "" {
			kind = strings.ToLower(strings.TrimSpace(evt.Body))
		}
		if kind == "" {
			continue
		}
		if _, ok := seen[kind]; ok {
			continue
		}
		seen[kind] = struct{}{}
		kinds = append(kinds, kind)
	}
	metadata := copyMapAny(latest.Metadata)
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["kind"] = "task_update_summary"
	metadata["task_kind"] = "summary"
	metadata["supersedes_count"] = len(events) - 1
	payload := map[string]any{
		"count":       len(events),
		"kinds":       kinds,
		"latest_kind": strings.ToLower(strings.TrimSpace(schema.GetMetaString(latest.Metadata, "task_kind"))),
	}
	if latest.Payload != nil {
		payload["latest"] = latest.Payload
	}
	summary := latest
	summary.Metadata = metadata
	summary.Payload = payload
	summary.Body = "summary"
	if taskID != "" {
		summary.Subject = fmt.Sprintf("Task %s summary", taskID)
	}
	return summary, len(events) - 1
}

func projectContextEventsForPrompt(events []eventbus.Event, limit int) ([]eventbus.Event, int) {
	if len(events) == 0 {
		return nil, 0
	}
	ordered := append([]eventbus.Event{}, events...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].CreatedAt.Equal(ordered[j].CreatedAt) {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].CreatedAt.Before(ordered[j].CreatedAt)
	})

	out := make([]eventbus.Event, 0, len(ordered))
	grouped := map[string][]eventbus.Event{}
	groupOrder := make([]string, 0, len(ordered))
	superseded := 0
	for _, evt := range ordered {
		if eventPriorityForEvent(evt) == "low" && !isActionablePromptEvent(evt) {
			superseded++
			continue
		}
		if isActionablePromptEvent(evt) {
			out = append(out, evt)
			continue
		}
		if evt.Stream == "task_output" {
			taskID := schema.GetMetaString(evt.Metadata, "task_id")
			if taskID != "" {
				if _, ok := grouped[taskID]; !ok {
					groupOrder = append(groupOrder, taskID)
				}
				grouped[taskID] = append(grouped[taskID], evt)
				continue
			}
		}
		out = append(out, evt)
	}
	for _, taskID := range groupOrder {
		summary, reduced := aggregateTaskOutputEvents(grouped[taskID])
		out = append(out, summary)
		superseded += reduced
	}
	return selectContextEventsForPrompt(out, limit), superseded
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
	r.advanceContextCursor(agentID, events)
}

func buildInputWithHistory(source, message string, metadata map[string]any, turnCtx TurnContext, frame ContextUpdateFrame) string {
	if source == "" {
		source = "external"
	}
	priority := eventPriority(metadata)
	message = strings.TrimSpace(message)
	if hasMessageEvent(frame.Events, message) {
		message = ""
	}

	var b strings.Builder
	b.Grow(3500)
	b.WriteString("<system_updates source=\"")
	b.WriteString(xmlEscape(source))
	b.WriteString("\" priority=\"")
	b.WriteString(xmlEscape(priority))
	b.WriteString("\">\n")
	if message != "" {
		b.WriteString("  <message>")
		b.WriteString(xmlEscape(message))
		b.WriteString("</message>\n")
	}

	b.WriteString(indentXML(renderContextUpdatesXML(turnCtx, frame), "  "))
	b.WriteString("\n")
	b.WriteString("</system_updates>")
	return b.String()
}

func indentXML(raw, prefix string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	lines := strings.Split(raw, "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

func renderContextUpdatesXML(turnCtx TurnContext, frame ContextUpdateFrame) string {
	var b strings.Builder
	b.WriteString("<context_updates>\n")

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
		b.WriteString(turnCtx.Previous.UTC().Format("Monday, 2006-01-02"))
		b.WriteString("\" current_date=\"")
		b.WriteString(turnCtx.Now.UTC().Format("Monday, 2006-01-02"))
		b.WriteString("\" />\n")
	}

	for _, evt := range frame.Events {
		priority := eventPriorityForEvent(evt)
		taskID := schema.GetMetaString(evt.Metadata, "task_id")
		taskKind := schema.GetMetaString(evt.Metadata, "task_kind")
		subject := clipText(strings.TrimSpace(evt.Subject), contextEventBodyLimit(priority))
		body, bodyTruncated := buildContextEventBody(evt, priority)
		if taskID != "" && subject == fmt.Sprintf("Task %s update", taskID) {
			subject = ""
		}
		if taskKind != "" && strings.EqualFold(strings.TrimSpace(body), taskKind) {
			body = ""
			bodyTruncated = false
		}
		b.WriteString("  <event stream=\"")
		b.WriteString(xmlEscape(evt.Stream))
		b.WriteString("\"")
		if priority != "" && priority != "normal" {
			b.WriteString(" priority=\"")
			b.WriteString(xmlEscape(priority))
			b.WriteString("\"")
		}
		if taskID != "" {
			b.WriteString(" task_id=\"")
			b.WriteString(xmlEscape(taskID))
			b.WriteString("\"")
		}
		if taskKind != "" {
			b.WriteString(" task_kind=\"")
			b.WriteString(xmlEscape(taskKind))
			b.WriteString("\"")
		}
		b.WriteString(" created_at=\"")
		b.WriteString(evt.CreatedAt.UTC().Format(time.RFC3339))
		b.WriteString("\">\n")
		if subject != "" {
			b.WriteString("    <subject>")
			b.WriteString(xmlEscape(subject))
			b.WriteString("</subject>\n")
		}
		if body != "" {
			b.WriteString("    <body")
			if bodyTruncated {
				b.WriteString(" truncated=\"true\"")
			}
			b.WriteString(">")
			b.WriteString(xmlEscape(body))
			b.WriteString("</body>\n")
		}
		if metadata := compactEventMetadataForPrompt(evt.Metadata, evt.Stream, priority, taskID, taskKind); metadata != "" {
			b.WriteString("    <metadata>")
			b.WriteString(xmlEscape(metadata))
			b.WriteString("</metadata>\n")
		}
		b.WriteString("  </event>\n")
	}
	b.WriteString("</context_updates>")
	return b.String()
}

func hasMessageEvent(events []eventbus.Event, message string) bool {
	msg := strings.TrimSpace(message)
	if msg == "" {
		return false
	}
	for _, evt := range events {
		if schema.GetMetaString(evt.Metadata, "kind") != "message" {
			continue
		}
		if strings.TrimSpace(evt.Body) == msg {
			return true
		}
	}
	return false
}

func contextEventBodyLimit(priority string) int {
	switch strings.ToLower(strings.TrimSpace(priority)) {
	case "interrupt", "wake":
		return maxContextEventBodyWake
	default:
		return maxContextEventBodyBase
	}
}

func buildContextEventBody(evt eventbus.Event, priority string) (string, bool) {
	limit := contextEventBodyLimit(priority)
	body := strings.TrimSpace(evt.Body)
	payload := previewJSON(evt.Payload, maxContextEventData)
	combined := body
	if payload != "" {
		switch {
		case combined == "":
			combined = payload
		case shouldAppendPayloadToContextBody(evt, priority, combined):
			combined = combined + "\n" + payload
		}
	}
	return clipTextWithMeta(combined, limit)
}

func shouldAppendPayloadToContextBody(evt eventbus.Event, priority, body string) bool {
	switch strings.ToLower(strings.TrimSpace(priority)) {
	case "interrupt", "wake":
		return true
	}
	if strings.TrimSpace(evt.Stream) == "errors" {
		return true
	}
	taskKind := strings.ToLower(strings.TrimSpace(schema.GetMetaString(evt.Metadata, "task_kind")))
	switch taskKind {
	case "completed", "failed", "cancelled", "killed":
		return true
	}
	switch strings.ToLower(strings.TrimSpace(body)) {
	case "summary", "completed", "failed", "cancelled", "killed":
		return true
	default:
		return false
	}
}

func compactEventMetadataForPrompt(metadata map[string]any, stream, priority, taskID, taskKind string) string {
	if len(metadata) == 0 {
		return ""
	}
	clean := make(map[string]any, len(metadata))
	for k, v := range metadata {
		if strings.TrimSpace(k) == "" || v == nil {
			continue
		}
		if value, ok := v.(string); ok && strings.TrimSpace(value) == "" {
			continue
		}
		clean[k] = v
	}
	if len(clean) == 0 {
		return ""
	}
	// Remove fields that duplicate event XML attributes.
	if p := schema.GetMetaString(clean, "priority"); p != "" && p == priority {
		delete(clean, "priority")
	}
	if taskID != "" && schema.GetMetaString(clean, "task_id") == taskID {
		delete(clean, "task_id")
	}
	if taskKind != "" && schema.GetMetaString(clean, "task_kind") == taskKind {
		delete(clean, "task_kind")
	}
	// Remove kind when it's implied by the stream or other attributes.
	kind := strings.ToLower(strings.TrimSpace(schema.GetMetaString(clean, "kind")))
	switch {
	case kind == "task_update" || kind == "task_update_summary":
		delete(clean, "kind")
	case kind == "command" && schema.GetMetaString(clean, "action") != "":
		// action is more specific than kind="command"
		delete(clean, "kind")
	}
	// Remove internal/redundant fields.
	delete(clean, "target")
	delete(clean, "agent_id")
	delete(clean, "task_type")
	delete(clean, "supersedes_count")
	if len(clean) == 0 {
		return ""
	}
	return previewJSON(clean, maxContextEventData)
}

func previewJSON(v any, limit int) string {
	if v == nil {
		return ""
	}
	switch typed := v.(type) {
	case map[string]any:
		if len(typed) == 0 {
			return ""
		}
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

func clipText(text string, limit int) string {
	clipped, _ := clipTextWithMeta(text, limit)
	return clipped
}

func clipTextWithMeta(text string, limit int) (string, bool) {
	if limit <= 0 || len(text) <= limit {
		return text, false
	}
	return strings.TrimSpace(text[:limit]) + " …", true
}

func ignoredWakeEventIDsForTurn(messageMeta map[string]any, contextEvents []eventbus.Event) []string {
	var ids []string
	if id := schema.GetMetaString(messageMeta, "event_id"); id != "" {
		ids = append(ids, id)
	}
	for _, evt := range contextEvents {
		if id := strings.TrimSpace(evt.ID); id != "" {
			ids = append(ids, id)
		}
	}
	return uniqueStrings(ids)
}
