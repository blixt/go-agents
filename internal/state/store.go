package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flitsinc/go-agents/internal/idgen"
)

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

type Agent struct {
	ID        string    `json:"id"`
	Profile   string    `json:"profile"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Action struct {
	ID        string         `json:"id"`
	AgentID   string         `json:"agent_id"`
	Content   string         `json:"content"`
	Status    string         `json:"status"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

func (s *Store) CreateAgent(ctx context.Context, profile, status string) (Agent, error) {
	id := idgen.New()
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `INSERT INTO agents (id, profile, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		id, profile, status, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return Agent{}, fmt.Errorf("insert agent: %w", err)
	}
	return Agent{ID: id, Profile: profile, Status: status, CreatedAt: now, UpdatedAt: now}, nil
}

func (s *Store) ListAgents(ctx context.Context, limit int) ([]Agent, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, profile, status, created_at, updated_at FROM agents ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	defer rows.Close()

	var out []Agent
	for rows.Next() {
		var agent Agent
		var createdAtStr, updatedAtStr string
		if err := rows.Scan(&agent.ID, &agent.Profile, &agent.Status, &createdAtStr, &updatedAtStr); err != nil {
			return nil, fmt.Errorf("scan agent: %w", err)
		}
		agent.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAtStr)
		agent.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAtStr)
		out = append(out, agent)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate agents: %w", err)
	}
	return out, nil
}

func (s *Store) CreateAction(ctx context.Context, agentID, content, status string, metadata map[string]any) (Action, error) {
	id := idgen.New()
	now := time.Now().UTC()
	metadataJSON, err := encodeJSON(metadata)
	if err != nil {
		return Action{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO actions (id, agent_id, content, status, metadata, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, nullString(agentID), content, status, metadataJSON, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return Action{}, fmt.Errorf("insert action: %w", err)
	}
	return Action{ID: id, AgentID: agentID, Content: content, Status: status, Metadata: metadata, CreatedAt: now, UpdatedAt: now}, nil
}

func (s *Store) ListActions(ctx context.Context, limit int) ([]Action, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, agent_id, content, status, metadata, created_at, updated_at FROM actions ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list actions: %w", err)
	}
	defer rows.Close()

	var out []Action
	for rows.Next() {
		var action Action
		var metadataStr sql.NullString
		var agentIDStr sql.NullString
		var createdAtStr, updatedAtStr string
		if err := rows.Scan(&action.ID, &agentIDStr, &action.Content, &action.Status, &metadataStr, &createdAtStr, &updatedAtStr); err != nil {
			return nil, fmt.Errorf("scan action: %w", err)
		}
		if agentIDStr.Valid {
			action.AgentID = agentIDStr.String
		}
		action.Metadata = decodeJSONMap(metadataStr.String)
		action.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAtStr)
		action.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAtStr)
		out = append(out, action)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate actions: %w", err)
	}
	return out, nil
}

func encodeJSON(v any) (string, error) {
	if v == nil {
		return "", nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
