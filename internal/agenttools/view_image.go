package agenttools

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/flitsinc/go-llms/content"
	llmtools "github.com/flitsinc/go-llms/tools"
)

const defaultViewImageMaxBytes = 4 * 1024 * 1024

type ViewImageParams struct {
	Path     string `json:"path,omitempty" description:"Local image file path"`
	URL      string `json:"url,omitempty" description:"Image URL"`
	Fidelity string `json:"fidelity,omitempty" description:"Image fidelity: low | medium | high (default low)"`
}

func ViewImageTool() llmtools.Tool {
	return llmtools.Func(
		"ViewImage",
		"Load an image from a local path or URL and add it to model context",
		"view_image",
		func(r llmtools.Runner, p ViewImageParams) llmtools.Result {
			maxBytes := int64(defaultViewImageMaxBytes)
			fidelity := normalizeImageFidelity(p.Fidelity)

			path := strings.TrimSpace(p.Path)
			source := map[string]any{}
			cleanup := func() {}
			if path == "" {
				rawURL := strings.TrimSpace(p.URL)
				if rawURL == "" {
					return llmtools.Errorf("either path or url is required")
				}
				downloaded, err := downloadImage(rawURL, maxBytes)
				if err != nil {
					return llmtools.ErrorWithLabel("view_image failed", err)
				}
				path = downloaded
				source["url"] = rawURL
				cleanup = func() { _ = os.Remove(downloaded) }
			} else {
				source["path"] = path
			}
			defer cleanup()

			absPath, err := filepath.Abs(path)
			if err != nil {
				return llmtools.ErrorWithLabel("view_image failed", fmt.Errorf("resolve path: %w", err))
			}
			info, err := os.Stat(absPath)
			if err != nil {
				return llmtools.ErrorWithLabel("view_image failed", fmt.Errorf("stat image: %w", err))
			}
			if info.Size() > maxBytes {
				return llmtools.Errorf("image too large (%d > %d bytes)", info.Size(), maxBytes)
			}

			highQuality := fidelity != "low"
			name, dataURI, err := content.ImageToDataURI(absPath, highQuality)
			if err != nil {
				return llmtools.ErrorWithLabel("view_image failed", err)
			}

			result := map[string]any{
				"source":   source,
				"fidelity": fidelity,
				"name":     name,
				"path":     absPath,
			}
			payload, err := content.FromAny(result)
			if err != nil {
				return llmtools.ErrorWithLabel("view_image failed", err)
			}
			payload.AddImage(dataURI)
			return llmtools.SuccessWithContent("Loaded image", payload)
		},
	)
}

func normalizeImageFidelity(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "high":
		return "high"
	case "medium":
		return "medium"
	default:
		return "low"
	}
}

func downloadImage(rawURL string, maxBytes int64) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("unsupported url scheme %q", parsed.Scheme)
	}

	resp, err := http.Get(rawURL) // #nosec G107 -- agent-requested URL fetch for explicit tool usage
	if err != nil {
		return "", fmt.Errorf("download image: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("download image: http %d", resp.StatusCode)
	}

	f, err := os.CreateTemp("", "go-agents-view-image-*")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	defer func() {
		_ = f.Close()
	}()

	written, err := io.Copy(f, io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("write temp image: %w", err)
	}
	if written > maxBytes {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("image exceeds max_bytes")
	}
	return f.Name(), nil
}
