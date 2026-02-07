package api

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
			ExecToolFactory(),
		},
	})

	completeDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		completeDone <- waitForSpawnedTask(ctx, fixture.Bus, "exec", func(taskID string) error {
			_ = fixture.Manager.MarkRunning(context.Background(), taskID)
			_ = fixture.Manager.RecordUpdate(context.Background(), taskID, "stdout", map[string]any{
				"text": "Amsterdam now: 5°C, partly cloudy.",
			})
			if err := fixture.Manager.Complete(context.Background(), taskID, map[string]any{
				"location":    "Amsterdam, Netherlands",
				"temperature": "5°C (41°F)",
				"condition":   "Partly Cloudy",
				"humidity":    "75%",
				"wind":        "19 km/h SW",
				"pressure":    "1019 mb",
			}); err != nil {
				return fmt.Errorf("complete task: %w", err)
			}
			return nil
		})
	}()

	session, err := fixture.Runtime.RunOnce(context.Background(), "operator", "what's the weather in amsterdam")
	if err != nil {
		t.Fatalf("run once: %v", err)
	}
	select {
	case completeErr := <-completeDone:
		if completeErr != nil {
			t.Fatalf("complete weather exec task: %v", completeErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for weather exec completion helper")
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
