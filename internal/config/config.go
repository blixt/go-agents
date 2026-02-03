package config

import (
	"bufio"
	"encoding/json"
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
	cfg := defaultConfig()
	fileCfg, _ := loadFileConfig([]string{"config.json", filepath.Join(cfg.DataDir, "config.json")})
	cfg = mergeConfig(cfg, fileCfg)
	cfg = applyDefaults(cfg)
	cfg.LLMAPIKey = strings.TrimSpace(providerAPIKey(cfg.LLMProvider))
	return cfg
}

type fileConfig struct {
	HTTPAddr     string `json:"http_addr"`
	DataDir      string `json:"data_dir"`
	DBPath       string `json:"db_path"`
	SnapshotDir  string `json:"snapshot_dir"`
	WebDir       string `json:"web_dir"`
	LLMProvider  string `json:"llm_provider"`
	LLMModel     string `json:"llm_model"`
	RestartToken string `json:"restart_token"`
}

func defaultConfig() Config {
	return Config{
		HTTPAddr:    ":8080",
		DataDir:     "data",
		WebDir:      "web",
		LLMProvider: "anthropic",
		LLMModel:    "claude-sonnet-4-5",
	}
}

func applyDefaults(cfg Config) Config {
	if cfg.DataDir == "" {
		cfg.DataDir = "data"
	}
	if cfg.DBPath == "" {
		cfg.DBPath = filepath.Join(cfg.DataDir, "go-agents.db")
	}
	if cfg.SnapshotDir == "" {
		cfg.SnapshotDir = filepath.Join(cfg.DataDir, "exec-snapshots")
	}
	if cfg.WebDir == "" {
		cfg.WebDir = "web"
	}
	return cfg
}

func mergeConfig(base Config, fileCfg fileConfig) Config {
	if fileCfg.HTTPAddr != "" {
		base.HTTPAddr = fileCfg.HTTPAddr
	}
	if fileCfg.DataDir != "" {
		base.DataDir = fileCfg.DataDir
	}
	if fileCfg.DBPath != "" {
		base.DBPath = fileCfg.DBPath
	}
	if fileCfg.SnapshotDir != "" {
		base.SnapshotDir = fileCfg.SnapshotDir
	}
	if fileCfg.WebDir != "" {
		base.WebDir = fileCfg.WebDir
	}
	if fileCfg.LLMProvider != "" {
		base.LLMProvider = fileCfg.LLMProvider
	}
	if fileCfg.LLMModel != "" {
		base.LLMModel = fileCfg.LLMModel
	}
	if fileCfg.RestartToken != "" {
		base.RestartToken = fileCfg.RestartToken
	}
	return base
}

func loadFileConfig(paths []string) (fileConfig, bool) {
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var cfg fileConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			continue
		}
		return cfg, true
	}
	return fileConfig{}, false
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
		if key == "" || !strings.HasSuffix(key, "API_KEY") {
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

func providerAPIKey(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "anthropic":
		return os.Getenv("GO_AGENTS_ANTHROPIC_API_KEY")
	case "openai", "openai-responses", "openai-chat":
		return os.Getenv("GO_AGENTS_OPENAI_API_KEY")
	case "google":
		return os.Getenv("GO_AGENTS_GOOGLE_API_KEY")
	default:
		return ""
	}
}
