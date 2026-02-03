package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	HTTPAddr    string
	DataDir     string
	DBPath      string
	SnapshotDir string
	WebDir      string

	LLMProvider  string
	LLMModel     string
	LLMAPIKey    string
	RestartToken string
}

func Load() Config {
	loadDotEnv(".env")
	dataDir := getEnv("GO_AGENTS_DATA_DIR", "data")
	return Config{
		HTTPAddr:    getEnv("GO_AGENTS_HTTP_ADDR", ":8080"),
		DataDir:     dataDir,
		DBPath:      getEnv("GO_AGENTS_DB_PATH", filepath.Join(dataDir, "go-agents.db")),
		SnapshotDir: getEnv("GO_AGENTS_SNAPSHOT_DIR", filepath.Join(dataDir, "exec-snapshots")),
		WebDir:      getEnv("GO_AGENTS_WEB_DIR", "web"),

		LLMProvider:  getEnv("GO_AGENTS_LLM_PROVIDER", "openai-responses"),
		LLMModel:     getEnv("GO_AGENTS_LLM_MODEL", ""),
		LLMAPIKey:    getEnv("GO_AGENTS_LLM_API_KEY", ""),
		RestartToken: getEnv("GO_AGENTS_RESTART_TOKEN", ""),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func loadDotEnv(path string) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		_ = os.Setenv(key, value)
	}
}
