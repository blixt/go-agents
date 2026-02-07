package tasks

import (
	"context"
	"strings"
)

type contextKey string

const parentTaskIDKey contextKey = "parent_task_id"
const ignoredWakeEventIDsKey contextKey = "ignored_wake_event_ids"

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

func WithIgnoredWakeEventIDs(ctx context.Context, eventIDs []string) context.Context {
	if len(eventIDs) == 0 {
		return ctx
	}
	ids := uniqueContextStrings(eventIDs)
	if len(ids) == 0 {
		return ctx
	}
	return context.WithValue(ctx, ignoredWakeEventIDsKey, ids)
}

func IgnoredWakeEventIDsFromContext(ctx context.Context) map[string]struct{} {
	out := map[string]struct{}{}
	if ctx == nil {
		return out
	}
	val, ok := ctx.Value(ignoredWakeEventIDsKey).([]string)
	if !ok || len(val) == 0 {
		return out
	}
	for _, raw := range val {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		out[id] = struct{}{}
	}
	return out
}

func uniqueContextStrings(values []string) []string {
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
