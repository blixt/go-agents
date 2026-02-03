package tasks

import "context"

type contextKey string

const parentTaskIDKey contextKey = "parent_task_id"

func WithParentTaskID(ctx context.Context, taskID string) context.Context {
	if taskID == "" {
		return ctx
	}
	return context.WithValue(ctx, parentTaskIDKey, taskID)
}

func ParentTaskIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if val, ok := ctx.Value(parentTaskIDKey).(string); ok {
		return val
	}
	return ""
}
