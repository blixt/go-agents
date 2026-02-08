package agentcontext

import "context"

type contextKey string

const taskIDKey contextKey = "task_id"

func WithTaskID(ctx context.Context, taskID string) context.Context {
	if taskID == "" {
		return ctx
	}
	return context.WithValue(ctx, taskIDKey, taskID)
}

func TaskIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if val, ok := ctx.Value(taskIDKey).(string); ok {
		return val
	}
	return ""
}
