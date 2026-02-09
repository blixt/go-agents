package api

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flitsinc/go-agents/internal/tasks"
	"github.com/flitsinc/go-llms/content"
	"github.com/flitsinc/go-llms/llms"
)

func TestWeatherSessionStateSnapshot(t *testing.T) {
	planArgs, _ := json.Marshal(map[string]any{
		"code": `// Fetch weather data for Amsterdam.
globalThis.result = {
  location: "Amsterdam, Netherlands",
  temperature: "5°C (41°F)",
  condition: "Partly Cloudy",
};`,
		"wait_seconds": 5,
	})
	provider := newScriptedProvider(
		newScriptedStream(scriptedStreamSpec{
			Message: llms.Message{
				Role: "assistant",
				Content: content.FromText(
					"I'll fetch the current weather in Amsterdam for you.\n\n",
				),
				ToolCalls: []llms.ToolCall{{
					ID:        "toolu_weather_exec_1",
					Name:      "exec",
					Arguments: planArgs,
				}},
			},
			Text: "I'll fetch the current weather in Amsterdam for you.\n\n",
			Thought: content.Thought{
				ID:      "reasoning-weather-1",
				Text:    "I should call exec to gather fresh weather data before answering.",
				Summary: true,
			},
			Statuses: []llms.StreamStatus{
				llms.StreamStatusThinking,
				llms.StreamStatusText,
				llms.StreamStatusToolCallBegin,
				llms.StreamStatusToolCallReady,
			},
		}),
		newScriptedStream(scriptedStreamSpec{
			Message: llms.Message{Role: "assistant", Content: content.FromText(weatherFinalText)},
			Text:    weatherFinalText,
			Statuses: []llms.StreamStatus{
				llms.StreamStatusText,
			},
		}),
	)

	fixture := NewSnapshotFixture(t, SnapshotFixtureOptions{
		StartTime: time.Date(2026, 2, 7, 10, 0, 0, 0, time.UTC),
		Tick:      time.Second,
		Provider:  provider,
		ToolFactories: []ToolFactory{
			MockExecToolFactory(map[string]any{
				"location":    "Amsterdam, Netherlands",
				"temperature": "5°C (41°F)",
				"condition":   "Partly Cloudy",
				"humidity":    "75%",
				"wind":        "19 km/h SW",
				"pressure":    "1019 mb",
			}),
		},
	})

	// Pre-create the agent task — ensureRootTask no longer auto-creates.
	_, err := fixture.Manager.Spawn(context.Background(), tasks.Spec{
		ID:    "operator",
		Type:  "agent",
		Owner: "operator",
		Mode:  "async",
		Metadata: map[string]any{
			"input_target":  "operator",
			"notify_target": "operator",
		},
	})
	if err != nil {
		t.Fatalf("create operator task: %v", err)
	}
	_ = fixture.Manager.MarkRunning(context.Background(), "operator")

	session, err := fixture.Runtime.RunOnce(context.Background(), "operator", "what's the weather in amsterdam")
	if err != nil {
		t.Fatalf("run once: %v", err)
	}
	if !strings.Contains(session.LastOutput, "Perfect! Here's the current weather in Amsterdam:") {
		t.Fatalf("unexpected session output: %q", session.LastOutput)
	}

	state := fixture.FetchState(t)
	got, err := renderSessionSnapshotMarkdown("Weather Session Snapshot", state)
	if err != nil {
		t.Fatalf("render snapshot markdown: %v", err)
	}
	assertSnapshot(t, filepath.Join("testdata", "weather_session_snapshot.md"), got)
}

const weatherFinalText = `Perfect! Here's the current weather in Amsterdam:

🌤️ Amsterdam, Netherlands

Temperature: 5°C (41°F)
Condition: Partly Cloudy
Humidity: 75%
Wind: 19 km/h SW
Pressure: 1019 mb`
