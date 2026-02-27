package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/flitsinc/go-agents/internal/schema"
)

// EmitAssistantOutput records an assistant_output update and appends a matching
// assistant_message history entry for the given agent task.
func (r *Runtime) EmitAssistantOutput(ctx context.Context, taskID, text string, metadata map[string]any) error {
	if r == nil || r.Tasks == nil {
		return fmt.Errorf("runtime unavailable")
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return fmt.Errorf("task_id is required")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("text is required")
	}

	source := strings.TrimSpace(schema.GetMetaString(metadata, "source"))
	requestID := strings.TrimSpace(schema.GetMetaString(metadata, "request_id"))
	serviceID := strings.TrimSpace(schema.GetMetaString(metadata, "service_id"))
	fromTaskID := strings.TrimSpace(schema.GetMetaString(metadata, "from_task_id"))

	var routeContext map[string]any
	if raw := metadata["context"]; raw != nil {
		if typed, ok := raw.(map[string]any); ok && len(typed) > 0 {
			routeContext = cloneAnyMap(typed)
		}
	}

	routing := turnRouting{}
	if requestID != "" || serviceID != "" || source != "" || len(routeContext) > 0 {
		route := routingCandidate{
			RequestID: requestID,
			ServiceID: serviceID,
			Source:    source,
			Context:   routeContext,
		}
		routing.HasPrimary = true
		routing.Primary = route
		if requestID != "" || len(routeContext) > 0 {
			routing.Routes = []routingCandidate{route}
		}
	}

	payload := assistantOutputPayload(text, routing)
	opts := assistantOutputUpdateOptions(source, routing)
	if err := r.Tasks.RecordUpdateWithOptions(ctx, taskID, "assistant_output", payload, opts); err != nil {
		return err
	}

	historyData := map[string]any{
		"origin": "send_to_user",
	}
	if source != "" {
		historyData["source"] = source
	}
	if fromTaskID != "" {
		historyData["from_task_id"] = fromTaskID
	}
	if requestID != "" {
		historyData["request_id"] = requestID
	}
	if serviceID != "" {
		historyData["service_id"] = serviceID
	}
	if len(routeContext) > 0 {
		historyData["context"] = cloneAnyMap(routeContext)
	}
	r.appendHistory(ctx, taskID, "assistant_message", "assistant", text, "", 0, historyData)

	session, ok := r.GetSession(taskID)
	if !ok {
		session = Session{TaskID: taskID}
	}
	session.LastOutput = text
	session.LastError = ""
	session.UpdatedAt = r.now()
	r.SetSession(session)

	return nil
}
