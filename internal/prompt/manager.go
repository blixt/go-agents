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
	"github.com/flitsinc/go-agents/internal/goagents"
	"github.com/flitsinc/go-llms/content"
)

type Manager struct {
	Home      string
	ToolNames []string
}

const (
	promptUserFile        = "PROMPT.ts"
	promptMemoryFile      = "MEMORY.md"
	maxPromptContextChars = 8000
)

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
		home, err = goagents.EnsureHome()
		if err != nil {
			return "", err
		}
	}
	userPrompt, err := runPromptScript(ctx, home, promptUserFile)
	if err != nil {
		return "", err
	}
	managedPrompt, err := runPromptScript(ctx, home, goagents.ManagedHarnessPromptFile)
	if err != nil {
		return "", err
	}
	mergedPrompt, err := mergePromptSections(userPrompt, managedPrompt)
	if err != nil {
		return "", err
	}
	withContext, err := appendPromptContextFromWorkspace(home, mergedPrompt)
	if err != nil {
		return "", err
	}
	return withContext, nil
}

func runPromptScript(ctx context.Context, home, relPath string) (string, error) {
	promptPath := filepath.Join(home, relPath)
	cmd := exec.CommandContext(ctx, "bun", promptPath)
	cmd.Dir = home
	cmd.Env = append(os.Environ(), fmt.Sprintf("GO_AGENTS_HOME=%s", home))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		details := strings.TrimSpace(stderr.String())
		if details != "" {
			return "", fmt.Errorf("prompt builder %s failed: %s", relPath, details)
		}
		return "", fmt.Errorf("prompt builder %s failed: %w", relPath, err)
	}
	text := strings.TrimSpace(string(out))
	if text == "" {
		return "", fmt.Errorf("prompt builder %s returned empty prompt", relPath)
	}
	return text, nil
}

func mergePromptSections(userPrompt, managedPrompt string) (string, error) {
	userPrompt = strings.TrimSpace(userPrompt)
	managedPrompt = strings.TrimSpace(managedPrompt)

	parts := make([]string, 0, 2)
	if userPrompt != "" {
		parts = append(parts, userPrompt)
	}
	if managedPrompt != "" {
		parts = append(parts, strings.TrimSpace(
			"## Managed Harness API Context\n"+
				"The following section is managed by the runtime and is authoritative for harness API behavior.\n\n"+
				managedPrompt,
		))
	}

	prompt := strings.TrimSpace(strings.Join(parts, "\n\n"))
	if prompt == "" {
		return "", fmt.Errorf("prompt builder returned empty prompt")
	}
	return prompt, nil
}

func appendPromptContextFromWorkspace(home, prompt string) (string, error) {
	target := filepath.Join(home, promptMemoryFile)
	data, err := os.ReadFile(target)
	if err != nil {
		if os.IsNotExist(err) {
			return prompt, nil
		}
		return "", fmt.Errorf("read prompt context file %s: %w", target, err)
	}
	raw := strings.TrimSpace(string(data))
	if raw == "" {
		return prompt, nil
	}

	trimmed, wasTrimmed := trimPromptContext(raw, maxPromptContextChars)
	section := fmt.Sprintf("### %s\n%s", promptMemoryFile, trimmed)
	if wasTrimmed {
		section += fmt.Sprintf("\n\n[...truncated; read %s in ~/.go-agents for full content...]", promptMemoryFile)
	}

	var b strings.Builder
	b.WriteString(prompt)
	b.WriteString("\n\n## Workspace Context\n")
	b.WriteString("The following workspace files were loaded from ~/.go-agents:\n\n")
	b.WriteString(section)
	return strings.TrimSpace(b.String()), nil
}

func trimPromptContext(text string, limit int) (string, bool) {
	if limit <= 0 {
		return text, false
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text, false
	}
	head := int(float64(limit) * 0.7)
	tail := int(float64(limit) * 0.2)
	if head < 1 {
		head = 1
	}
	if tail < 1 {
		tail = 1
	}
	if head+tail >= limit {
		tail = limit - head
		if tail < 1 {
			tail = 1
			head = limit - tail
			if head < 1 {
				head = 1
			}
		}
	}
	out := string(runes[:head]) + "\n\n[...truncated...]\n\n" + string(runes[len(runes)-tail:])
	return out, true
}
