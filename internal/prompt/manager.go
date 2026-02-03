package prompt

import (
	"context"
	"fmt"
	"strings"

	"github.com/flitsinc/go-agents/internal/eventbus"
	"github.com/flitsinc/go-llms/content"
)

type Compactor interface {
	Summarize(ctx context.Context, input string) (string, error)
}

type Manager struct {
	Policy     StreamPolicy
	Compactor  Compactor
	MaxUpdates int
	Summary    string
	LastState  State

	CacheHint string
}

func (m *Manager) BuildSystemPrompt(ctx context.Context, bus *eventbus.Bus) (content.Content, string, error) {
	updates, err := CollectUpdates(ctx, bus, m.Policy)
	if err != nil {
		return nil, "", err
	}
	updatesText := RenderUpdates(updates)

	if m.MaxUpdates > 0 && len(updates.Events) > m.MaxUpdates && m.Compactor != nil {
		summary, err := m.Compactor.Summarize(ctx, updatesText)
		if err != nil {
			return nil, "", err
		}
		m.Summary = summary
		updatesText = ""
	}

	builder := NewBuilder()
	builder.Add(Block{ID: "system", Priority: 100, Content: DefaultSystemPrompt})

	if m.Summary != "" {
		builder.Add(Block{ID: "summary", Priority: 80, Content: fmt.Sprintf("Summary:\n%s", m.Summary)})
	}
	if updatesText != "" {
		builder.Add(Block{ID: "updates", Priority: 60, Content: fmt.Sprintf("Updates:\n%s", updatesText)})
	}

	text := strings.TrimSpace(builder.Build())
	prompt := content.FromText(text)
	if m.CacheHint != "" {
		prompt = append(prompt, &content.CacheHint{Duration: m.CacheHint})
	}
	return prompt, text, nil
}

func (m *Manager) Snapshot(state State) {
	m.LastState = state
}

func (m *Manager) Diff(state State) State {
	return Diff(m.LastState, state)
}
