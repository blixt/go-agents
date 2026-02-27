package agenttools

import (
	"strings"

	"github.com/flitsinc/go-agents/internal/toolresult"
	llmtools "github.com/flitsinc/go-llms/tools"
)

type NoopParams struct {
	Comment string `json:"comment,omitempty" description:"Optional note about why the agent is idling"`
}

func NoopTool() llmtools.Tool {
	return llmtools.Func(
		"Noop",
		"Explicitly do nothing and leave a short optional comment",
		"noop",
		func(r llmtools.Runner, p NoopParams) llmtools.Result {
			return toolresult.Success("noop", map[string]any{
				"status":  "idle",
				"comment": strings.TrimSpace(p.Comment),
			})
		},
	)
}
