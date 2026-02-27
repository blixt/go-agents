package ai

import (
	"context"
	"encoding/json"

	"github.com/flitsinc/go-agents/internal/toolresult"
	"github.com/flitsinc/go-llms/llms"
	llmtools "github.com/flitsinc/go-llms/tools"
)

type ExternalHandler func(ctx context.Context, name string, params json.RawMessage) (any, error)

func AddExternalTools(client *Client, schemas []llmtools.FunctionSchema, handler ExternalHandler) {
	if client == nil || client.LLM == nil {
		return
	}
	client.LLM.AddExternalTools(schemas, func(r llmtools.Runner, params json.RawMessage) llmtools.Result {
		toolCall, ok := llms.GetToolCall(r.Context())
		if !ok {
			return toolresult.Errorf("external_tool", "missing tool call")
		}
		result, err := handler(r.Context(), toolCall.Name, params)
		if err != nil {
			return toolresult.Error(toolCall.Name, err)
		}
		return toolresult.Success(toolCall.Name, result)
	})
}
