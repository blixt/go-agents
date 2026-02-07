package agenttools

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/flitsinc/go-llms/content"
	llmtools "github.com/flitsinc/go-llms/tools"
)

const tinyPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII="

func TestViewImageToolLoadsLocalImage(t *testing.T) {
	tool := ViewImageTool()
	dir := t.TempDir()
	path := filepath.Join(dir, "pixel.png")
	data, err := base64.StdEncoding.DecodeString(tinyPNGBase64)
	if err != nil {
		t.Fatalf("decode png: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write png: %v", err)
	}

	raw, _ := json.Marshal(ViewImageParams{Path: path, Fidelity: "low"})
	result := tool.Run(llmtools.NopRunner, raw)
	if result.Error() != nil {
		t.Fatalf("unexpected error: %v", result.Error())
	}
	if len(result.Content()) < 2 {
		t.Fatalf("expected json + image content, got %d items", len(result.Content()))
	}

	var payload map[string]any
	foundImage := false
	for _, item := range result.Content() {
		switch v := item.(type) {
		case *content.JSON:
			_ = json.Unmarshal(v.Data, &payload)
		case *content.ImageURL:
			if v.URL != "" {
				foundImage = true
			}
		}
	}
	if !foundImage {
		t.Fatalf("expected image content item")
	}
	if payload == nil {
		t.Fatalf("expected json payload")
	}
	if payload["fidelity"] != "low" {
		t.Fatalf("expected low fidelity, got %v", payload["fidelity"])
	}
}

func TestViewImageToolRequiresPathOrURL(t *testing.T) {
	tool := ViewImageTool()
	raw, _ := json.Marshal(ViewImageParams{})
	result := tool.Run(llmtools.NopRunner, raw)
	if result.Error() == nil {
		t.Fatalf("expected error for missing path/url")
	}
}
