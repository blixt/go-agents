package agenttools

import (
	"encoding/json"
	"encoding/xml"
	"strings"
	"testing"

	"github.com/flitsinc/go-llms/content"
	llmtools "github.com/flitsinc/go-llms/tools"
)

func decodeToolPayload(t *testing.T, result llmtools.Result) map[string]any {
	t.Helper()
	if result.Error() != nil {
		t.Fatalf("tool error: %v", result.Error())
	}

	for _, item := range result.Content() {
		if jsonItem, ok := item.(*content.JSON); ok {
			var payload map[string]any
			if err := json.Unmarshal(jsonItem.Data, &payload); err == nil && payload != nil {
				return payload
			}
		}
	}
	for _, item := range result.Content() {
		textItem, ok := item.(*content.Text)
		if !ok {
			continue
		}
		if payload, ok := decodeToolXMLPayload(textItem.Text); ok {
			return payload
		}
	}
	t.Fatalf("expected tool payload in JSON or XML form")
	return nil
}

func decodeToolXMLPayload(raw string) (map[string]any, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, false
	}

	var envelope struct {
		Value string `xml:"value"`
	}
	if err := xml.Unmarshal([]byte(raw), &envelope); err != nil {
		return nil, false
	}
	value := strings.TrimSpace(envelope.Value)
	if value == "" {
		return nil, false
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(value), &payload); err != nil {
		return nil, false
	}
	return payload, payload != nil
}
