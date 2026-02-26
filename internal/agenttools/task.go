package agenttools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flitsinc/go-agents/internal/agentcontext"
	"github.com/flitsinc/go-agents/internal/eventbus"
	"github.com/flitsinc/go-agents/internal/schema"
	"github.com/flitsinc/go-agents/internal/tasks"
	llmtools "github.com/flitsinc/go-llms/tools"
)

const agentOutputPollInterval = 100 * time.Millisecond

type AwaitTaskParams struct {
	TaskID      string `json:"task_id" description:"Task id to wait for"`
	WaitSeconds *int   `json:"wait_seconds" description:"Seconds to wait before returning (must be > 0)"`
}

type SendTaskParams struct {
	TaskID string `json:"task_id" description:"Task id to send input to"`
	Body   string `json:"body" description:"Content to send to the task"`
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
			if p.WaitSeconds == nil {
				return llmtools.Errorf("wait_seconds is required")
			}
			waitSeconds := *p.WaitSeconds
			if waitSeconds <= 0 {
				return llmtools.Errorf("wait_seconds must be > 0")
			}
			timeout := time.Duration(waitSeconds) * time.Second
			startedAt := time.Now()

			task, err := manager.Get(r.Context(), p.TaskID)
			if err != nil {
				return llmtools.ErrorWithLabel("await_task failed", err)
			}

			isAgentTask := task.Type == "agent" || task.Type == "llm"
			assistantOutputBaseline := ""
			if isAgentTask {
				latest, found, latestErr := manager.LatestUpdate(r.Context(), p.TaskID, "assistant_output")
				if latestErr != nil {
					return llmtools.ErrorWithLabel("await_task failed", latestErr)
				}
				if found {
					assistantOutputBaseline = latest.ID
				}
			}

			r.Report("waiting")
			awaited, awaitErr := manager.Await(r.Context(), p.TaskID, timeout)
			if awaited.ID == "" {
				awaited = task
			}

			if isAgentTask && awaited.Status == tasks.StatusCompleted {
				remaining := timeout - time.Since(startedAt)
				update, found, updateErr := waitForUpdateSince(
					r.Context(),
					manager,
					p.TaskID,
					assistantOutputBaseline,
					"assistant_output",
					remaining,
				)
				if updateErr != nil {
					return llmtools.ErrorWithLabel("await_task failed", updateErr)
				}
				if found {
					awaited.Result = mergeAssistantOutputResult(awaited.Result, update.Payload)
					awaitErr = nil
				} else if awaitErr == nil {
					awaitErr = tasks.ErrAwaitTimeout
				}
			}

			if tasks.IsTerminalStatus(awaited.Status) {
				owner := strings.TrimSpace(agentcontext.TaskIDFromContext(r.Context()))
				if owner != "" {
					manager.AckTaskOutput(r.Context(), p.TaskID, owner)
				}
			}
			resp := map[string]any{
				"task_id": p.TaskID,
				"status":  awaited.Status,
			}
			includeCompletedResult := awaited.Status == tasks.StatusCompleted
			if isAgentTask && tasks.IsAwaitTimeout(awaitErr) {
				includeCompletedResult = false
			}
			if includeCompletedResult {
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

func waitForUpdateSince(
	ctx context.Context,
	manager *tasks.Manager,
	taskID, afterID, kind string,
	timeout time.Duration,
) (tasks.Update, bool, error) {
	if manager == nil || strings.TrimSpace(taskID) == "" {
		return tasks.Update{}, false, nil
	}

	readLatest := func() (tasks.Update, bool, error) {
		updates, err := manager.ListUpdatesSince(ctx, taskID, afterID, kind, 20)
		if err != nil {
			return tasks.Update{}, false, err
		}
		if len(updates) == 0 {
			return tasks.Update{}, false, nil
		}
		return updates[len(updates)-1], true, nil
	}

	if timeout <= 0 {
		return readLatest()
	}

	deadline := time.Now().Add(timeout)
	for {
		update, found, err := readLatest()
		if err != nil {
			return tasks.Update{}, false, err
		}
		if found {
			return update, true, nil
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return tasks.Update{}, false, nil
		}
		sleepFor := agentOutputPollInterval
		if remaining < sleepFor {
			sleepFor = remaining
		}
		timer := time.NewTimer(sleepFor)
		select {
		case <-ctx.Done():
			timer.Stop()
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return tasks.Update{}, false, nil
			}
			return tasks.Update{}, false, ctx.Err()
		case <-timer.C:
		}
	}
}

func mergeAssistantOutputResult(result map[string]any, payload map[string]any) map[string]any {
	merged := cloneAnyMap(result)
	if merged == nil {
		merged = cloneAnyMap(payload)
	}
	text := strings.TrimSpace(schema.GetMetaString(payload, "text"))
	if text != "" {
		merged["output"] = text
	}
	return merged
}

func cloneAnyMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
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
			body := strings.TrimSpace(p.Body)
			if body == "" {
				return llmtools.Errorf("body is required")
			}
			task, err := manager.Get(r.Context(), p.TaskID)
			if err != nil {
				return llmtools.ErrorWithLabel("send_task failed", err)
			}

			if task.Type == "agent" || task.Type == "llm" {
				if bus == nil {
					return llmtools.Errorf("event bus unavailable")
				}
				target := task.Owner
				if target == "" {
					return llmtools.Errorf("target agent unavailable")
				}
				source := agentcontext.TaskIDFromContext(r.Context())
				if source == "" {
					source = "system"
				}
				evt, err := bus.Push(r.Context(), eventbus.EventInput{
					Stream:    schema.StreamTaskInput,
					ScopeType: "task",
					ScopeID:   target,
					Subject:   fmt.Sprintf("Message from %s", source),
					Body:      body,
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
				return llmtools.Success(map[string]any{"ok": true, "event_id": evt.ID})
			}

			input := map[string]any{"text": body}
			if err := manager.Send(r.Context(), p.TaskID, input); err != nil {
				return llmtools.ErrorWithLabel("send_task failed", err)
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
