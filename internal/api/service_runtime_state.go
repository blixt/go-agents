package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const supervisorStateFile = ".service-supervisor-state.json"

type serviceRuntimeState struct {
	Name               string   `json:"name"`
	ServiceID          string   `json:"service_id,omitempty"`
	State              string   `json:"state"`
	Reason             string   `json:"reason,omitempty"`
	LastError          string   `json:"last_error,omitempty"`
	LastExitCode       *int     `json:"last_exit_code,omitempty"`
	RestartCount       int      `json:"restart_count"`
	BackoffMS          int      `json:"backoff_ms"`
	NextRestartAt      string   `json:"next_restart_at,omitempty"`
	PID                *int     `json:"pid,omitempty"`
	StartedAt          string   `json:"started_at,omitempty"`
	RunTsMtime         float64  `json:"run_ts_mtime,omitempty"`
	ManifestMtime      float64  `json:"manifest_mtime,omitempty"`
	PackageMtime       float64  `json:"package_mtime,omitempty"`
	Singleton          bool     `json:"singleton"`
	RequiredEnv        []string `json:"required_env,omitempty"`
	EnvironmentKeys    []string `json:"environment_keys,omitempty"`
	MissingEnv         []string `json:"missing_env,omitempty"`
	PendingConfigError string   `json:"pending_config_error,omitempty"`
	ManifestPath       string   `json:"manifest_path,omitempty"`
	HeartbeatFile      string   `json:"heartbeat_file,omitempty"`
	HeartbeatTTL       int      `json:"heartbeat_ttl_seconds,omitempty"`
}

type serviceRuntimeStateFile struct {
	Services []serviceRuntimeState `json:"services"`
}

func readSupervisorServiceState() []serviceRuntimeState {
	home := strings.TrimSpace(os.Getenv("GO_AGENTS_HOME"))
	if home == "" {
		return nil
	}
	path := filepath.Join(home, "services", supervisorStateFile)
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return nil
	}
	var payload serviceRuntimeStateFile
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil
	}
	if len(payload.Services) == 0 {
		return nil
	}
	sort.Slice(payload.Services, func(i, j int) bool {
		return payload.Services[i].Name < payload.Services[j].Name
	})
	return payload.Services
}
