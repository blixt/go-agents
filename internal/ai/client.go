package ai

import (
	"context"
	"fmt"

	"github.com/flitsinc/go-llms/anthropic"
	"github.com/flitsinc/go-llms/google"
	"github.com/flitsinc/go-llms/llms"
	"github.com/flitsinc/go-llms/openai"
	llmtools "github.com/flitsinc/go-llms/tools"
)

type Config struct {
	Provider string
	Model    string
	APIKey   string
}

type Client struct {
	LLM *llms.LLM
}

func NewClient(cfg Config, tool llmtools.Tool) (*Client, error) {
	if cfg.Provider == "" {
		return nil, fmt.Errorf("llm provider is required")
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("llm model is required")
	}
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("llm api key is required")
	}

	var provider llms.Provider
	switch cfg.Provider {
	case "openai-responses":
		provider = openai.NewResponsesAPI(cfg.APIKey, cfg.Model)
	case "openai-chat":
		provider = openai.NewChatCompletionsAPI(cfg.APIKey, cfg.Model)
	case "anthropic":
		provider = anthropic.New(cfg.APIKey, cfg.Model)
	case "google":
		provider = google.New(cfg.Model).WithGeminiAPI(cfg.APIKey)
	default:
		return nil, fmt.Errorf("unsupported provider: %s", cfg.Provider)
	}

	if tool != nil {
		return &Client{LLM: llms.New(provider, tool)}, nil
	}
	return &Client{LLM: llms.New(provider)}, nil
}

func (c *Client) Chat(ctx context.Context, messages []llms.Message) <-chan llms.Update {
	if c == nil || c.LLM == nil {
		ch := make(chan llms.Update)
		close(ch)
		return ch
	}
	return c.LLM.ChatUsingMessages(ctx, messages)
}
