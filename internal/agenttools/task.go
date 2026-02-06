package agenttools

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/flitsinc/go-agents/internal/agentcontext"
	"github.com/flitsinc/go-agents/internal/eventbus"
	"github.com/flitsinc/go-agents/internal/tasks"
	llmtools "github.com/flitsinc/go-llms/tools"
)

type AwaitTaskParams struct {
	TaskID      string `json:"task_id" description:"Task id to wait for"`
	WaitSeconds int    `json:"wait_seconds,omitempty" description:"Seconds to wait before returning pending"`
}

type SendTaskParams struct {
	TaskID  string `json:"task_id" description:"Task id to send input to"`
	Message string `json:"message,omitempty" description:"Message to send to an agent task"`
	Text    string `json:"text,omitempty" description:"Text to send to a task (exec stdin)"`
	JSON    string `json:"json,omitempty" description:"Raw JSON object string to send as input"`
}

type CancelTaskParams struct {
	TaskID string `json:"task_id" description:"Task id to cancel"`
	Reason string `json:"reason,omitempty" description:"Optional reason for cancellation"`
}

type KillTaskParams struct {
	TaskID string `json:"task_id" description:"Task id to kill"`
	Reason string `json:"reason,omitempty" description:"Optional reason for kill"`
}

func AwaitTaskTool(manager *tasks.Manager) llmtools.Tool {
	return llmtools.Func(
		"AwaitTask",
		"Wait for a task to complete or return pending on timeout",
		"await_task",
		func(r llmtools.Runner, p AwaitTaskParams) llmtools.Result {
			if manager == nil {
				return llmtools.Errorf("task manager unavailable")
			}
			if p.TaskID == "" {
				return llmtools.Errorf("task_id is required")
			}
			timeout := time.Duration(p.WaitSeconds) * time.Second
			awaited, awaitErr := manager.Await(r.Context(), p.TaskID, timeout)
			resp := map[string]any{
				"task_id": p.TaskID,
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
				resp["await_error"] = awaitErr.Error()
				if tasks.IsAwaitTimeout(awaitErr) {
					resp["pending"] = true
				}
				if wakeErr, ok := tasks.AsWakeError(awaitErr); ok {
					resp["wake"] = map[string]any{
						"priority": wakeErr.Priority,
						"event":    wakeErr.Event.Subject,
					}
				}
			}
			return llmtools.Success(resp)
		},
	)
}

func SendTaskTool(manager *tasks.Manager, bus *eventbus.Bus) llmtools.Tool {
	return llmtools.Func(
		"SendTask",
		"Send input to a running task",
		"send_task",
		func(r llmtools.Runner, p SendTaskParams) llmtools.Result {
			if manager == nil {
				return llmtools.Errorf("task manager unavailable")
			}
			if p.TaskID == "" {
				return llmtools.Errorf("task_id is required")
			}
			task, err := manager.Get(r.Context(), p.TaskID)
			if err != nil {
				return llmtools.ErrorWithLabel("send_task failed", err)
			}

			input, err := buildTaskInput(p)
			if err != nil {
				return llmtools.ErrorWithLabel("send_task failed", err)
			}

			if task.Type == "agent" || task.Type == "llm" {
				if bus == nil {
					return llmtools.Errorf("event bus unavailable")
				}
				message := extractMessage(input)
				if message == "" {
					return llmtools.Errorf("message or text is required for agent tasks")
				}
				target := ""
				if task.Metadata != nil {
					if val, ok := task.Metadata["agent_id"].(string); ok {
						target = val
					}
				}
				if target == "" {
					target = task.Owner
				}
				if target == "" {
					return llmtools.Errorf("target agent unavailable")
				}
				source := agentcontext.AgentIDFromContext(r.Context())
				if source == "" {
					source = "system"
				}
				evt, err := bus.Push(r.Context(), eventbus.EventInput{
					Stream:    "messages",
					ScopeType: "agent",
					ScopeID:   target,
					Subject:   fmt.Sprintf("Message from %s", source),
					Body:      message,
					Metadata: map[string]any{
						"kind":     "message",
						"source":   source,
						"target":   target,
						"via_task": p.TaskID,
					},
				})
				if err != nil {
					return llmtools.ErrorWithLabel("send_task failed", err)
				}
				return llmtools.Success(map[string]any{"ok": true, "event_id": evt.ID, "agent_id": target})
			}

			if err := manager.Send(r.Context(), p.TaskID, input); err != nil {
				return llmtools.ErrorWithLabel("send_task failed", err)
			}
			return llmtools.Success(map[string]any{"ok": true})
		},
	)
}

func CancelTaskTool(manager *tasks.Manager) llmtools.Tool {
	return llmtools.Func(
		"CancelTask",
		"Cancel a task (and its children)",
		"cancel_task",
		func(r llmtools.Runner, p CancelTaskParams) llmtools.Result {
			if manager == nil {
				return llmtools.Errorf("task manager unavailable")
			}
			if p.TaskID == "" {
				return llmtools.Errorf("task_id is required")
			}
			if err := manager.Cancel(r.Context(), p.TaskID, p.Reason); err != nil {
				return llmtools.ErrorWithLabel("cancel_task failed", err)
			}
			return llmtools.Success(map[string]any{"ok": true})
		},
	)
}

func KillTaskTool(manager *tasks.Manager) llmtools.Tool {
	return llmtools.Func(
		"KillTask",
		"Kill a task (and its children)",
		"kill_task",
		func(r llmtools.Runner, p KillTaskParams) llmtools.Result {
			if manager == nil {
				return llmtools.Errorf("task manager unavailable")
			}
			if p.TaskID == "" {
				return llmtools.Errorf("task_id is required")
			}
			if err := manager.Kill(r.Context(), p.TaskID, p.Reason); err != nil {
				return llmtools.ErrorWithLabel("kill_task failed", err)
			}
			return llmtools.Success(map[string]any{"ok": true})
		},
	)
}

func extractMessage(input map[string]any) string {
	if input == nil {
		return ""
	}
	if val, ok := input["message"].(string); ok {
		return val
	}
	if val, ok := input["text"].(string); ok {
		return val
	}
	data, err := json.Marshal(input)
	if err != nil {
		return ""
	}
	return string(data)
}

func buildTaskInput(p SendTaskParams) (map[string]any, error) {
	if strings.TrimSpace(p.JSON) != "" {
		var payload map[string]any
		if err := json.Unmarshal([]byte(p.JSON), &payload); err != nil {
			return nil, fmt.Errorf("invalid json: %w", err)
		}
		if payload == nil {
			return nil, fmt.Errorf("json payload is empty")
		}
		return payload, nil
	}
	payload := map[string]any{}
	if strings.TrimSpace(p.Message) != "" {
		payload["message"] = p.Message
	}
	if strings.TrimSpace(p.Text) != "" {
		payload["text"] = p.Text
	}
	if len(payload) == 0 {
		return nil, fmt.Errorf("message, text, or json is required")
	}
	return payload, nil
}
