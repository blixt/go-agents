package ai

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
	LLM    *llms.LLM
	config Config
	tools  []llmtools.Tool
}

func NewClient(cfg Config, tools ...llmtools.Tool) (*Client, error) {
	llm, err := newLLM(cfg, tools...)
	if err != nil {
		return nil, err
	}
	return &Client{LLM: llm, config: cfg, tools: tools}, nil
}

func (c *Client) NewSession() (*llms.LLM, error) {
	if c == nil {
		return nil, errors.New("client is nil")
	}
	if c.config.Provider == "" {
		return nil, errors.New("client config missing provider")
	}
	return newLLM(c.config, c.tools...)
}

func (c *Client) NewSessionWithModel(model string) (*llms.LLM, error) {
	if c == nil {
		return nil, errors.New("client is nil")
	}
	if c.config.Provider == "" {
		return nil, errors.New("client config missing provider")
	}
	cfg := c.config
	if strings.TrimSpace(model) != "" {
		cfg.Model = resolveModelAlias(cfg.Provider, model)
	}
	return newLLM(cfg, c.tools...)
}

func newLLM(cfg Config, tools ...llmtools.Tool) (*llms.LLM, error) {
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
		model := anthropic.New(cfg.APIKey, cfg.Model)
		model.WithMaxTokens(62976)
		model.WithThinking(1024)
		provider = model
	case "google":
		provider = google.New(cfg.Model).WithGeminiAPI(cfg.APIKey)
	default:
		return nil, fmt.Errorf("unsupported provider: %s", cfg.Provider)
	}

	if len(tools) > 0 {
		return llms.New(provider, tools...), nil
	}
	return llms.New(provider), nil
}

func resolveModelAlias(provider, model string) string {
	alias := strings.ToLower(strings.TrimSpace(model))
	if alias == "" {
		return model
	}
	if provider == "anthropic" {
		switch alias {
		case "fast":
			return "claude-3-5-haiku-latest"
		case "balanced":
			return "claude-3-5-sonnet-latest"
		case "smart":
			return "claude-3-opus-latest"
		}
	}
	return model
}

func (c *Client) Chat(ctx context.Context, messages []llms.Message) <-chan llms.Update {
	if c == nil || c.LLM == nil {
		ch := make(chan llms.Update)
		close(ch)
		return ch
	}
	return c.LLM.ChatUsingMessages(ctx, messages)
}
