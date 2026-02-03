package engine

import (
	"context"
	"sync"
	"time"

	"github.com/flitsinc/go-agents/internal/ai"
	"github.com/flitsinc/go-agents/internal/eventbus"
	agentctx "github.com/flitsinc/go-agents/internal/prompt"
	"github.com/flitsinc/go-agents/internal/tasks"
	"github.com/flitsinc/go-llms/content"
	"github.com/flitsinc/go-llms/llms"
)

type Session struct {
	AgentID    string    `json:"agent_id"`
	Prompt     string    `json:"prompt"`
	LastOutput string    `json:"last_output"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type Runtime struct {
	Bus     *eventbus.Bus
	Tasks   *tasks.Manager
	LLM     *ai.Client
	Context *agentctx.Manager

	mu       sync.RWMutex
	sessions map[string]Session
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
	}
	if client != nil {
		ctxMgr.Compactor = agentctx.NewLLMCompactor(client)
	}

	return &Runtime{
		Bus:      bus,
		Tasks:    tasksMgr,
		LLM:      client,
		Context:  ctxMgr,
		sessions: map[string]Session{},
	}
}

func (r *Runtime) RunOnce(ctx context.Context, agentID, message string) (Session, error) {
	if agentID == "" {
		agentID = "operator"
	}
	promptContent, promptText, err := r.Context.BuildSystemPrompt(ctx, r.Bus)
	if err != nil {
		return Session{}, err
	}

	var output string
	if r.LLM != nil && r.LLM.LLM != nil {
		llmClient := r.LLM.LLM
		prev := llmClient.SystemPrompt
		llmClient.SystemPrompt = func() content.Content { return promptContent }
		defer func() {
			llmClient.SystemPrompt = prev
		}()

		updates := llmClient.ChatUsingMessages(ctx, []llms.Message{
			{Role: "user", Content: content.FromText(message)},
		})
		for update := range updates {
			if textUpdate, ok := update.(llms.TextUpdate); ok {
				output += textUpdate.Text
			}
		}
		if err := llmClient.Err(); err != nil {
			return Session{}, err
		}
	}

	session := Session{AgentID: agentID, Prompt: promptText, LastOutput: output, UpdatedAt: time.Now().UTC()}
	r.SetSession(session)
	return session, nil
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

func (r *Runtime) BuildSession(ctx context.Context, agentID string) (Session, error) {
	if agentID == "" {
		agentID = "operator"
	}
	if r.Context == nil || r.Bus == nil {
		session := Session{AgentID: agentID, UpdatedAt: time.Now().UTC()}
		r.SetSession(session)
		return session, nil
	}
	_, promptText, err := r.Context.BuildSystemPrompt(ctx, r.Bus)
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
			_, _ = r.RunOnce(ctx, agentID, evt.Body)
			_ = r.Bus.Ack(ctx, "messages", []string{evt.ID}, agentID)
		}
	}
}
