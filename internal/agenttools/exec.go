package agenttools

import (
	"fmt"
	"strings"

	"github.com/flitsinc/go-llms/llms"
	llmtools "github.com/flitsinc/go-llms/tools"

	"github.com/flitsinc/go-agents/internal/agentcontext"
	"github.com/flitsinc/go-agents/internal/tasks"
)

type ExecParams struct {
	Code string `json:"code" description:"TypeScript code to run in Bun"`
	ID   string `json:"id,omitempty" description:"Optional session id for snapshot reuse"`
}

func ExecTool(manager *tasks.Manager) llmtools.Tool {
	return llmtools.Func(
		"Exec",
		"Run TypeScript code in an isolated Bun runtime and return a task id",
		"exec",
		func(r llmtools.Runner, p ExecParams) llmtools.Result {
			if manager == nil {
				return llmtools.Errorf("task manager unavailable")
			}
			code := strings.TrimSpace(p.Code)
			if code == "" {
				return llmtools.Errorf("code is required")
			}
			metadata := map[string]any{}
			if tc, ok := llms.GetToolCall(r.Context()); ok {
				metadata["tool_call_id"] = tc.ID
				metadata["tool_name"] = tc.Name
			}
			if agentID := agentcontext.AgentIDFromContext(r.Context()); agentID != "" {
				metadata["notify_target"] = agentID
			}

			parentID := tasks.ParentTaskIDFromContext(r.Context())
			spec := tasks.Spec{
				Type:     "exec",
				Owner:    "llm",
				ParentID: parentID,
				Metadata: metadata,
				Payload: map[string]any{
					"code": code,
					"id":   strings.TrimSpace(p.ID),
				},
			}
			task, err := manager.Spawn(r.Context(), spec)
			if err != nil {
				return llmtools.ErrorWithLabel("Exec failed", fmt.Errorf("spawn exec task: %w", err))
			}
			return llmtools.Success(map[string]any{
				"task_id": task.ID,
				"status":  task.Status,
			})
		},
	)
}
