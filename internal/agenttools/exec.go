package agenttools

import (
	"fmt"
	"strings"
	"time"

	"github.com/flitsinc/go-llms/llms"
	llmtools "github.com/flitsinc/go-llms/tools"

	"github.com/flitsinc/go-agents/internal/agentcontext"
	"github.com/flitsinc/go-agents/internal/tasks"
)

type ExecParams struct {
	Code        string `json:"code" description:"TypeScript code to run in Bun"`
	WaitSeconds *int   `json:"wait_seconds" description:"Required seconds to wait before returning; use 0 to return immediately"`
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
			owner := strings.TrimSpace(agentcontext.TaskIDFromContext(r.Context()))
			if owner == "" {
				owner = "agent"
			}
			metadata["notify_target"] = owner

			parentID := tasks.ParentTaskIDFromContext(r.Context())
			spec := tasks.Spec{
				Type:     "exec",
				Owner:    owner,
				ParentID: parentID,
				Metadata: metadata,
				Payload: map[string]any{
					"code": code,
				},
			}
			task, err := manager.Spawn(r.Context(), spec)
			if err != nil {
				return llmtools.ErrorWithLabel("Exec failed", fmt.Errorf("spawn exec task: %w", err))
			}

			if p.WaitSeconds == nil {
				return llmtools.Errorf("wait_seconds is required (set 0 to return immediately)")
			}
			waitSeconds := *p.WaitSeconds
			if waitSeconds < 0 {
				return llmtools.Errorf("wait_seconds must be >= 0")
			}
			if waitSeconds == 0 {
				return llmtools.Success(map[string]any{
					"task_id":    task.ID,
					"status":     task.Status,
					"pending":    true,
					"background": true,
				})
			}

			timeout := time.Duration(waitSeconds) * time.Second
			r.Report("running")
			awaited, awaitErr := manager.Await(r.Context(), task.ID, timeout)
			if tasks.IsTerminalStatus(awaited.Status) {
				manager.AckTaskOutput(r.Context(), task.ID, owner)
			}
			resp := map[string]any{
				"task_id": task.ID,
				"status":  awaited.Status,
			}
			if awaited.Status == tasks.StatusCompleted {
				resp["result"] = awaited.Result
			}
			if awaited.Status == tasks.StatusFailed || awaited.Status == tasks.StatusCancelled {
				resp["error"] = awaited.Error
				if awaited.Result != nil {
					resp["result"] = awaited.Result
				}
			}
			if awaitErr != nil {
				if tasks.IsAwaitTimeout(awaitErr) {
					resp["await_error"] = tasks.ErrAwaitTimeout.Error()
					resp["pending"] = true
					resp["background"] = true
				} else if wakeErr, ok := tasks.AsWakeError(awaitErr); ok {
					priority := strings.TrimSpace(wakeErr.Priority)
					if priority == "" {
						priority = "wake"
					}
					wakeEventID := strings.TrimSpace(wakeErr.Event.ID)
					if wakeEventID != "" {
						resp["wake_event_id"] = wakeEventID
					}
					if wakeStream := strings.TrimSpace(wakeErr.Event.Stream); wakeStream != "" {
						resp["wake_stream"] = wakeStream
					}
					wakeMsg := fmt.Sprintf("awoken by %s event", priority)
					if wakeEventID != "" {
						wakeMsg = fmt.Sprintf("%s %s", wakeMsg, wakeEventID)
					}
					resp["await_error"] = wakeMsg
					if awaited.Status == tasks.StatusQueued || awaited.Status == tasks.StatusRunning {
						resp["pending"] = true
					}
					resp["background"] = true
				} else {
					resp["await_error"] = awaitErr.Error()
				}
			}
			return llmtools.Success(resp)
		},
	)
}
