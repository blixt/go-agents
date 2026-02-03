package agenttools

import (
	"fmt"
	"strings"

	"github.com/oklog/ulid/v2"

	"github.com/flitsinc/go-agents/internal/agentcontext"
	"github.com/flitsinc/go-agents/internal/eventbus"
	"github.com/flitsinc/go-llms/tools"
)

type SendMessageParams struct {
	AgentID string `json:"agent_id" description:"Target agent id (empty to spawn a new subagent)"`
	Message string `json:"message" description:"Message to send"`
}

func SendMessageTool(bus *eventbus.Bus, ensureAgent func(string)) tools.Tool {
	return tools.Func(
		"SendMessage",
		"Send a message to another agent (or spawn a new subagent if no agent_id is provided)",
		"send_message",
		func(r tools.Runner, p SendMessageParams) tools.Result {
			if bus == nil {
				return tools.Errorf("event bus unavailable")
			}
			message := strings.TrimSpace(p.Message)
			if message == "" {
				return tools.Errorf("message is required")
			}

			agentID := strings.TrimSpace(p.AgentID)
			if agentID == "" {
				agentID = fmt.Sprintf("subagent-%s", ulid.Make().String())
			}

			source := agentcontext.AgentIDFromContext(r.Context())
			if source == "" {
				source = "system"
			}

			if agentID == "human" && source != "operator" {
				return tools.Errorf("only the operator may message the human")
			}

			if ensureAgent != nil && agentID != "human" {
				ensureAgent(agentID)
			}

			evt, err := bus.Push(r.Context(), eventbus.EventInput{
				Stream:    "messages",
				ScopeType: "agent",
				ScopeID:   agentID,
				Subject:   fmt.Sprintf("Message from %s", source),
				Body:      message,
				Metadata: map[string]any{
					"kind":   "message",
					"source": source,
					"target": agentID,
				},
			})
			if err != nil {
				return tools.ErrorWithLabel("SendMessage failed", err)
			}

			return tools.Success(map[string]any{
				"status":    "sent",
				"agent_id":  agentID,
				"event_id":  evt.ID,
				"recipient": agentID,
			})
		},
	)
}
