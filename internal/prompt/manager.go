package prompt

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/flitsinc/go-agents/internal/eventbus"
	"github.com/flitsinc/go-agents/internal/karna"
	"github.com/flitsinc/go-llms/content"
)

type Manager struct {
	Home string
}

func (m *Manager) BuildSystemPrompt(ctx context.Context, _ *eventbus.Bus) (content.Content, string, error) {
	text, err := BuildPrompt(ctx, m.Home)
	if err != nil {
		return nil, "", err
	}
	return content.FromText(text), text, nil
}

func BuildPrompt(ctx context.Context, home string) (string, error) {
	if home == "" {
		var err error
		home, err = karna.EnsureHome()
		if err != nil {
			return "", err
		}
	}
	promptPath := filepath.Join(home, "PROMPT.ts")
	cmd := exec.CommandContext(ctx, "bun", promptPath)
	cmd.Dir = home
	cmd.Env = append(os.Environ(), fmt.Sprintf("KARNA_HOME=%s", home))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		details := strings.TrimSpace(stderr.String())
		if details != "" {
			return "", fmt.Errorf("prompt builder failed: %s", details)
		}
		return "", fmt.Errorf("prompt builder failed: %w", err)
	}
	text := strings.TrimSpace(string(out))
	if text == "" {
		return "", fmt.Errorf("prompt builder returned empty prompt")
	}
	return text, nil
}
