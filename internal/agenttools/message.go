package agenttools

import (
	"fmt"
	"strings"

	"github.com/flitsinc/go-agents/internal/agentcontext"
	"github.com/flitsinc/go-agents/internal/eventbus"
	"github.com/flitsinc/go-llms/tools"
)

type SendMessageParams struct {
	AgentID  string `json:"agent_id" description:"Target agent id"`
	Message  string `json:"message" description:"Message to send"`
	Priority string `json:"priority,omitempty" description:"Message priority: interrupt|wake|normal|low"`
}

func SendMessageTool(bus *eventbus.Bus, ensureAgent func(string)) tools.Tool {
	return tools.Func(
		"SendMessage",
		"Send a message to another agent",
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
				return tools.Errorf("agent_id is required")
			}

			source := agentcontext.AgentIDFromContext(r.Context())
			if source == "" {
				source = "system"
			}

			priority := normalizePriority(p.Priority)
			if ensureAgent != nil {
				ensureAgent(agentID)
			}

			evt, err := bus.Push(r.Context(), eventbus.EventInput{
				Stream:    "messages",
				ScopeType: "agent",
				ScopeID:   agentID,
				Subject:   fmt.Sprintf("Message from %s", source),
				Body:      message,
				Metadata: map[string]any{
					"kind":     "message",
					"source":   source,
					"target":   agentID,
					"priority": priority,
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
				"priority":  priority,
			})
		},
	)
}

func normalizePriority(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "interrupt", "wake", "normal", "low":
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return "wake"
	}
}
