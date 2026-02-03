package prompt

import (
	"context"
	"strings"

	"github.com/flitsinc/go-agents/internal/ai"
	"github.com/flitsinc/go-llms/content"
	"github.com/flitsinc/go-llms/llms"
)

type LLMCompactor struct {
	Client *ai.Client
}

func NewLLMCompactor(client *ai.Client) *LLMCompactor {
	return &LLMCompactor{Client: client}
}

func (c *LLMCompactor) Summarize(ctx context.Context, input string) (string, error) {
	if c == nil || c.Client == nil || c.Client.LLM == nil {
		return "", nil
	}

	llmClient := c.Client.LLM
	prev := llmClient.SystemPrompt
	llmClient.SystemPrompt = func() content.Content {
		return content.FromText("Summarize the following updates for future context. Be concise and factual.")
	}
	defer func() {
		llmClient.SystemPrompt = prev
	}()

	updates := llmClient.ChatUsingMessages(ctx, []llms.Message{
		{Role: "user", Content: content.FromText(input)},
	})

	var sb strings.Builder
	for update := range updates {
		if textUpdate, ok := update.(llms.TextUpdate); ok {
			sb.WriteString(textUpdate.Text)
		}
	}
	if err := llmClient.Err(); err != nil {
		return "", err
	}
	return strings.TrimSpace(sb.String()), nil
}
