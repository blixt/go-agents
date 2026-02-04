package engine

import (
	"context"
	"os"
	"regexp"
	"strings"

	"github.com/flitsinc/go-agents/internal/eventbus"
	"github.com/flitsinc/go-llms/llms"
)

type busDebugger struct {
	bus        *eventbus.Bus
	agentID    string
	taskID     string
	redactions []string
	keyPattern *regexp.Regexp
}

func newBusDebugger(bus *eventbus.Bus, agentID, taskID string) *busDebugger {
	return &busDebugger{
		bus:        bus,
		agentID:    agentID,
		taskID:     taskID,
		redactions: loadAPIKeySecrets(),
		keyPattern: regexp.MustCompile(`([&?]key)=([^&]+)`),
	}
}

func loadAPIKeySecrets() []string {
	out := []string{}
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if !strings.HasSuffix(key, "API_KEY") || value == "" {
			continue
		}
		out = append(out, value)
	}
	return out
}

func (d *busDebugger) redact(text string) string {
	if text == "" {
		return text
	}
	if d.keyPattern != nil {
		text = d.keyPattern.ReplaceAllString(text, "$1=…")
	}
	for _, secret := range d.redactions {
		if secret == "" {
			continue
		}
		text = strings.ReplaceAll(text, secret, "…")
	}
	return text
}

func (d *busDebugger) push(subject string, payload map[string]any) {
	if d.bus == nil {
		return
	}
	_, _ = d.bus.Push(context.Background(), eventbus.EventInput{
		Stream:    "signals",
		ScopeType: "agent",
		ScopeID:   d.agentID,
		Subject:   subject,
		Body:      subject,
		Metadata: map[string]any{
			"kind":      "llm_debug",
			"agent_id":  d.agentID,
			"task_id":   d.taskID,
			"direction": subject,
		},
		Payload: payload,
	})
}

func (d *busDebugger) RawRequest(endpoint string, data []byte) {
	d.push("llm_debug_request", map[string]any{
		"endpoint": d.redact(endpoint),
		"data":     d.redact(string(data)),
	})
}

func (d *busDebugger) RawEvent(data []byte) {
	d.push("llm_debug_event", map[string]any{
		"data": d.redact(string(data)),
	})
}

func (r *Runtime) attachDebugger(llm *llms.LLM, agentID, taskID string) {
	if llm == nil || r.Bus == nil {
		return
	}
	llm.WithDebugger(newBusDebugger(r.Bus, agentID, taskID))
}
