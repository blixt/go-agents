package agentcontext

import "context"

type contextKey string

const agentIDKey contextKey = "agent_id"

func WithAgentID(ctx context.Context, agentID string) context.Context {
	if agentID == "" {
		return ctx
	}
	return context.WithValue(ctx, agentIDKey, agentID)
}

func AgentIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if val, ok := ctx.Value(agentIDKey).(string); ok {
		return val
	}
	return ""
}
