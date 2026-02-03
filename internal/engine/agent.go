package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/flitsinc/go-agents/internal/agentcontext"
	"github.com/flitsinc/go-agents/internal/ai"
	"github.com/flitsinc/go-agents/internal/eventbus"
	agentctx "github.com/flitsinc/go-agents/internal/prompt"
	"github.com/flitsinc/go-agents/internal/tasks"
	"github.com/flitsinc/go-llms/content"
	"github.com/flitsinc/go-llms/llms"
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
	LLM        *llms.LLM
	Prompt     *agentctx.Manager
	RootTaskID string
	mu         sync.Mutex
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
}

func NewRuntime(bus *eventbus.Bus, tasksMgr *tasks.Manager, client *ai.Client) *Runtime {
	ctxMgr := &agentctx.Manager{
		Policy: agentctx.StreamPolicy{
			UpdateStreams: []string{"messages", "task_output", "errors", "signals", "external"},
			Reader:        "operator",
			Limit:         50,
			Order:         "lifo",
			Ack:           true,
		},
		MaxUpdates: 200,
		CacheHint:  "short",
		CodeDir:    "code",
	}
	if client != nil {
		ctxMgr.Compactor = agentctx.NewLLMCompactor(client)
	}

	return &Runtime{
		Bus:      bus,
		Tasks:    tasksMgr,
		LLM:      client,
		Context:  ctxMgr,
		loops:    map[string]context.CancelFunc{},
		sessions: map[string]Session{},
		agents:   map[string]*AgentState{},
		inflight: map[string]context.CancelFunc{},
	}
}

func (r *Runtime) Start(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	r.baseCtx = ctx
}

func (r *Runtime) EnsureAgentLoop(agentID string) {
	if agentID == "" {
		return
	}
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
		agentID = "operator"
	}
	r.agentMu.Lock()
	state, ok := r.agents[agentID]
	if !ok {
		state = &AgentState{
			ID:     agentID,
			Prompt: clonePromptManager(r.Context, agentID),
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
	if state.LLM != nil {
		return state.LLM, nil
	}
	if r.LLMFactory != nil {
		llm, err := r.LLMFactory()
		if err != nil {
			return nil, err
		}
		state.LLM = llm
		return llm, nil
	}
	if r.LLM != nil && r.LLM.LLM != nil {
		state.LLM = r.LLM.LLM
		return state.LLM, nil
	}
	return nil, fmt.Errorf("LLM not configured")
}

func clonePromptManager(src *agentctx.Manager, agentID string) *agentctx.Manager {
	if src == nil {
		return nil
	}
	policy := src.Policy
	policy.Reader = agentID
	return &agentctx.Manager{
		Policy:     policy,
		Compactor:  src.Compactor,
		MaxUpdates: src.MaxUpdates,
		CacheHint:  src.CacheHint,
		CodeDir:    src.CodeDir,
	}
}

func (r *Runtime) RunOnce(ctx context.Context, agentID, message string) (Session, error) {
	return r.HandleMessage(ctx, agentID, "", message)
}

func (r *Runtime) HandleMessage(ctx context.Context, agentID, source, message string) (Session, error) {
	if agentID == "" {
		agentID = "operator"
	}
	state := r.ensureAgentState(agentID)
	if state == nil {
		return Session{}, fmt.Errorf("agent state unavailable")
	}
	state.mu.Lock()
	defer state.mu.Unlock()

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
		promptText = agentctx.DefaultSystemPrompt
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
				"agent_id":      agentID,
				"input_target":  agentID,
				"notify_target": agentID,
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
		if r.Tasks != nil {
			ctx := context.Background()
			if llmTask.ID != "" {
				_ = r.Tasks.Fail(ctx, llmTask.ID, session.LastError)
			}
		}
		if source != "" && source != agentID {
			_, _ = r.SendMessage(ctx, source, session.LastOutput, agentID)
		}
		return session, nil
	}

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

		updates := llmClient.ChatWithContext(llmCtx, message)
		for update := range updates {
			if textUpdate, ok := update.(llms.TextUpdate); ok {
				output += textUpdate.Text
			}
		}
		if err := llmClient.Err(); err != nil {
			session.LastError = err.Error()
			session.LastOutput = output
			r.SetSession(session)
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
				_, _ = r.SendMessage(ctx, source, reply, agentID)
			}
			return session, err
		}
	}

	session.LastOutput = output
	r.SetSession(session)
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
	if source != "" && source != agentID {
		_, _ = r.SendMessage(ctx, source, output, agentID)
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

func (r *Runtime) clearInflight(taskID string) {
	if taskID == "" {
		return
	}
	r.inflightMu.Lock()
	delete(r.inflight, taskID)
	r.inflightMu.Unlock()
}

func (r *Runtime) SendMessage(ctx context.Context, target, body, source string) (eventbus.Event, error) {
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
	return r.Bus.Push(ctx, eventbus.EventInput{
		Stream:    "messages",
		ScopeType: "agent",
		ScopeID:   target,
		Subject:   fmt.Sprintf("Message from %s", source),
		Body:      message,
		Metadata: map[string]any{
			"kind":   "message",
			"source": source,
			"target": target,
		},
	})
}

func (r *Runtime) watchInterrupts(ctx context.Context, agentID, taskID string, cancel context.CancelFunc) {
	if r.Bus == nil {
		return
	}
	sub := r.Bus.Subscribe(ctx, []string{"signals", "errors", "external", "messages"})
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
		agentID = "operator"
	}
	state := r.ensureAgentState(agentID)
	if state == nil || state.Prompt == nil || r.Bus == nil {
		session := Session{AgentID: agentID, UpdatedAt: time.Now().UTC()}
		r.SetSession(session)
		return session, nil
	}
	_, promptText, err := state.Prompt.BuildSystemPrompt(ctx, r.Bus)
	if err != nil {
		return Session{}, err
	}
	session := Session{AgentID: agentID, Prompt: promptText, UpdatedAt: time.Now().UTC()}
	r.SetSession(session)
	return session, nil
}

func (r *Runtime) Run(ctx context.Context, agentID string) error {
	if r.Bus == nil {
		return nil
	}
	if agentID == "" {
		agentID = "operator"
	}
	sub := r.Bus.Subscribe(ctx, []string{"messages"})
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case evt, ok := <-sub:
			if !ok {
				return ctx.Err()
			}
			if evt.Body == "" {
				continue
			}
			if !eventTargetsAgent(evt, agentID) {
				continue
			}
			source := ""
			if evt.Metadata != nil {
				if val, ok := evt.Metadata["source"].(string); ok {
					source = val
				}
			}
			_, _ = r.HandleMessage(ctx, agentID, source, evt.Body)
			_ = r.Bus.Ack(ctx, "messages", []string{evt.ID}, agentID)
		}
	}
}
