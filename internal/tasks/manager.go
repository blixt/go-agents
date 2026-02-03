package tasks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/flitsinc/go-agents/internal/eventbus"
)

type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

type Task struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	Status    Status         `json:"status"`
	Owner     string         `json:"owner"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	Payload   map[string]any `json:"payload,omitempty"`
	Result    map[string]any `json:"result,omitempty"`
	Error     string         `json:"error,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

type Update struct {
	ID        string         `json:"id"`
	TaskID    string         `json:"task_id"`
	Kind      string         `json:"kind"`
	Payload   map[string]any `json:"payload,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

type Spec struct {
	Type     string         `json:"type"`
	Owner    string         `json:"owner"`
	Metadata map[string]any `json:"metadata,omitempty"`
	Payload  map[string]any `json:"payload,omitempty"`
}

type ListFilter struct {
	Type   string
	Status Status
	Owner  string
	Limit  int
}

type Manager struct {
	db  *sql.DB
	bus *eventbus.Bus
}

func NewManager(db *sql.DB, bus *eventbus.Bus) *Manager {
	return &Manager{db: db, bus: bus}
}

func (m *Manager) Spawn(ctx context.Context, spec Spec) (Task, error) {
	if strings.TrimSpace(spec.Type) == "" {
		return Task{}, fmt.Errorf("task type is required")
	}
	id := ulid.Make().String()
	createdAt := time.Now().UTC()
	metadataJSON, err := encodeJSON(spec.Metadata)
	if err != nil {
		return Task{}, fmt.Errorf("encode metadata: %w", err)
	}
	payloadJSON, err := encodeJSON(spec.Payload)
	if err != nil {
		return Task{}, fmt.Errorf("encode payload: %w", err)
	}

	_, err = m.db.ExecContext(ctx, `
		INSERT INTO tasks (id, type, status, owner, created_at, updated_at, metadata, payload)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, id, spec.Type, StatusQueued, nullString(spec.Owner), createdAt.Format(time.RFC3339Nano), createdAt.Format(time.RFC3339Nano), metadataJSON, payloadJSON)
	if err != nil {
		return Task{}, fmt.Errorf("insert task: %w", err)
	}

	task := Task{
		ID:        id,
		Type:      spec.Type,
		Status:    StatusQueued,
		Owner:     spec.Owner,
		Metadata:  spec.Metadata,
		Payload:   spec.Payload,
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	}

	if m.bus != nil {
		_, _ = m.bus.Push(ctx, eventbus.EventInput{
			Stream:  "task_input",
			Subject: fmt.Sprintf("Task request %s", id),
			Body:    fmt.Sprintf("Spawn task %s (%s)", id, spec.Type),
			Metadata: map[string]any{
				"kind":      "command",
				"action":    "spawn",
				"task_id":   id,
				"task_type": spec.Type,
			},
			Payload: spec.Payload,
		})
	}

	_ = m.RecordUpdate(ctx, id, "spawn", map[string]any{"status": StatusQueued})
	return task, nil
}

func (m *Manager) Get(ctx context.Context, taskID string) (Task, error) {
	var task Task
	var createdAtStr, updatedAtStr, metadataStr, payloadStr, resultStr, errorStr sql.NullString
	var ownerStr sql.NullString
	row := m.db.QueryRowContext(ctx, `
		SELECT id, type, status, owner, created_at, updated_at, metadata, payload, result, error
		FROM tasks WHERE id = ?
	`, taskID)
	if err := row.Scan(&task.ID, &task.Type, &task.Status, &ownerStr, &createdAtStr, &updatedAtStr, &metadataStr, &payloadStr, &resultStr, &errorStr); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Task{}, fmt.Errorf("task not found")
		}
		return Task{}, fmt.Errorf("load task: %w", err)
	}

	task.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAtStr.String)
	task.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAtStr.String)
	task.Metadata = decodeJSONMap(metadataStr.String)
	task.Payload = decodeJSONMap(payloadStr.String)
	task.Result = decodeJSONMap(resultStr.String)
	if ownerStr.Valid {
		task.Owner = ownerStr.String
	}
	if errorStr.Valid {
		task.Error = errorStr.String
	}

	return task, nil
}

func (m *Manager) List(ctx context.Context, filter ListFilter) ([]Task, error) {
	query := `SELECT id, type, status, owner, created_at, updated_at, metadata, payload, result, error FROM tasks`
	var clauses []string
	var args []any

	if filter.Type != "" {
		clauses = append(clauses, "type = ?")
		args = append(args, filter.Type)
	}
	if filter.Status != "" {
		clauses = append(clauses, "status = ?")
		args = append(args, filter.Status)
	}
	if filter.Owner != "" {
		clauses = append(clauses, "owner = ?")
		args = append(args, filter.Owner)
	}

	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY created_at DESC"
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	query += fmt.Sprintf(" LIMIT %d", limit)

	rows, err := m.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()

	var out []Task
	for rows.Next() {
		var task Task
		var createdAtStr, updatedAtStr, metadataStr, payloadStr, resultStr, errorStr sql.NullString
		var ownerStr sql.NullString
		if err := rows.Scan(&task.ID, &task.Type, &task.Status, &ownerStr, &createdAtStr, &updatedAtStr, &metadataStr, &payloadStr, &resultStr, &errorStr); err != nil {
			return nil, fmt.Errorf("scan task: %w", err)
		}
		task.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAtStr.String)
		task.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAtStr.String)
		task.Metadata = decodeJSONMap(metadataStr.String)
		task.Payload = decodeJSONMap(payloadStr.String)
		task.Result = decodeJSONMap(resultStr.String)
		if ownerStr.Valid {
			task.Owner = ownerStr.String
		}
		if errorStr.Valid {
			task.Error = errorStr.String
		}
		out = append(out, task)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tasks: %w", err)
	}
	return out, nil
}

func (m *Manager) RecordUpdate(ctx context.Context, taskID, kind string, payload map[string]any) error {
	if taskID == "" {
		return fmt.Errorf("task_id is required")
	}
	if kind == "" {
		return fmt.Errorf("kind is required")
	}
	id := ulid.Make().String()
	createdAt := time.Now().UTC()
	payloadJSON, err := encodeJSON(payload)
	if err != nil {
		return fmt.Errorf("encode payload: %w", err)
	}

	_, err = m.db.ExecContext(ctx, `
		INSERT INTO task_updates (id, task_id, kind, payload, created_at)
		VALUES (?, ?, ?, ?, ?)
	`, id, taskID, kind, payloadJSON, createdAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("insert task update: %w", err)
	}

	_, err = m.db.ExecContext(ctx, `UPDATE tasks SET updated_at = ? WHERE id = ?`, createdAt.Format(time.RFC3339Nano), taskID)
	if err != nil {
		return fmt.Errorf("update task timestamp: %w", err)
	}

	if m.bus != nil {
		_, _ = m.bus.Push(ctx, eventbus.EventInput{
			Stream:  "task_output",
			Subject: fmt.Sprintf("Task %s update", taskID),
			Body:    kind,
			Metadata: map[string]any{
				"kind":      "task_update",
				"task_id":   taskID,
				"task_kind": kind,
			},
			Payload: payload,
		})
	}
	return nil
}

func (m *Manager) Complete(ctx context.Context, taskID string, result map[string]any) error {
	return m.updateStatus(ctx, taskID, StatusCompleted, result, "completed")
}

func (m *Manager) Fail(ctx context.Context, taskID string, reason string) error {
	payload := map[string]any{"error": reason}
	return m.updateStatus(ctx, taskID, StatusFailed, payload, "failed")
}

func (m *Manager) Cancel(ctx context.Context, taskID string, reason string) error {
	payload := map[string]any{"reason": reason}
	if m.bus != nil {
		_, _ = m.bus.Push(ctx, eventbus.EventInput{
			Stream:  "task_input",
			Subject: fmt.Sprintf("Task cancel %s", taskID),
			Body:    fmt.Sprintf("Cancel task %s", taskID),
			Metadata: map[string]any{
				"kind":    "command",
				"action":  "cancel",
				"task_id": taskID,
			},
			Payload: payload,
		})
	}
	return m.updateStatus(ctx, taskID, StatusCancelled, payload, "cancelled")
}

func (m *Manager) Kill(ctx context.Context, taskID string, reason string) error {
	payload := map[string]any{"reason": reason, "killed": true}
	if m.bus != nil {
		_, _ = m.bus.Push(ctx, eventbus.EventInput{
			Stream:  "task_input",
			Subject: fmt.Sprintf("Task kill %s", taskID),
			Body:    fmt.Sprintf("Kill task %s", taskID),
			Metadata: map[string]any{
				"kind":    "command",
				"action":  "kill",
				"task_id": taskID,
			},
			Payload: payload,
		})
	}
	return m.updateStatus(ctx, taskID, StatusCancelled, payload, "killed")
}

func (m *Manager) Send(ctx context.Context, taskID string, input map[string]any) error {
	if input == nil {
		input = map[string]any{}
	}
	if m.bus != nil {
		_, _ = m.bus.Push(ctx, eventbus.EventInput{
			Stream:  "task_input",
			Subject: fmt.Sprintf("Task input %s", taskID),
			Body:    fmt.Sprintf("Send input to task %s", taskID),
			Metadata: map[string]any{
				"kind":    "command",
				"action":  "send",
				"task_id": taskID,
			},
			Payload: input,
		})
	}
	return m.RecordUpdate(ctx, taskID, "input", input)
}

func (m *Manager) Await(ctx context.Context, taskID string, timeout time.Duration) (Task, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	for {
		task, err := m.Get(ctx, taskID)
		if err != nil {
			return Task{}, err
		}
		if task.Status == StatusCompleted || task.Status == StatusFailed || task.Status == StatusCancelled {
			return task, nil
		}

		if m.bus == nil {
			select {
			case <-time.After(500 * time.Millisecond):
			case <-ctx.Done():
				return Task{}, ctx.Err()
			}
			continue
		}

		sub := m.bus.Subscribe(ctx, []string{"task_output"})
		select {
		case <-ctx.Done():
			return Task{}, ctx.Err()
		case evt, ok := <-sub:
			if !ok {
				return Task{}, ctx.Err()
			}
			if evt.Metadata != nil {
				if id, ok := evt.Metadata["task_id"].(string); ok && id == taskID {
					// Loop to re-check status.
					continue
				}
			}
		}
	}
}

func (m *Manager) ListUpdates(ctx context.Context, taskID string, limit int) ([]Update, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT id, task_id, kind, payload, created_at
		FROM task_updates
		WHERE task_id = ?
		ORDER BY created_at ASC
		LIMIT ?
	`, taskID, limit)
	if err != nil {
		return nil, fmt.Errorf("list updates: %w", err)
	}
	defer rows.Close()

	var out []Update
	for rows.Next() {
		var upd Update
		var payloadStr, createdAtStr string
		if err := rows.Scan(&upd.ID, &upd.TaskID, &upd.Kind, &payloadStr, &createdAtStr); err != nil {
			return nil, fmt.Errorf("scan update: %w", err)
		}
		upd.Payload = decodeJSONMap(payloadStr)
		upd.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAtStr)
		out = append(out, upd)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate updates: %w", err)
	}
	return out, nil
}

func (m *Manager) ClaimQueued(ctx context.Context, taskType string, limit int) ([]Task, error) {
	if taskType == "" {
		return nil, fmt.Errorf("task type is required")
	}
	if limit <= 0 {
		limit = 1
	}

	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin claim tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	rows, err := tx.QueryContext(ctx, `
		SELECT id, type, status, owner, created_at, updated_at, metadata, payload, result, error
		FROM tasks
		WHERE status = ? AND type = ?
		ORDER BY created_at ASC
		LIMIT ?
	`, StatusQueued, taskType, limit)
	if err != nil {
		return nil, fmt.Errorf("query queued tasks: %w", err)
	}
	defer rows.Close()

	var tasks []Task
	for rows.Next() {
		var task Task
		var createdAtStr, updatedAtStr, metadataStr, payloadStr, resultStr, errorStr sql.NullString
		var ownerStr sql.NullString
		if err := rows.Scan(&task.ID, &task.Type, &task.Status, &ownerStr, &createdAtStr, &updatedAtStr, &metadataStr, &payloadStr, &resultStr, &errorStr); err != nil {
			return nil, fmt.Errorf("scan queued task: %w", err)
		}
		task.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAtStr.String)
		task.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAtStr.String)
		task.Metadata = decodeJSONMap(metadataStr.String)
		task.Payload = decodeJSONMap(payloadStr.String)
		task.Result = decodeJSONMap(resultStr.String)
		if ownerStr.Valid {
			task.Owner = ownerStr.String
		}
		if errorStr.Valid {
			task.Error = errorStr.String
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate queued tasks: %w", err)
	}

	if len(tasks) == 0 {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit empty claim: %w", err)
		}
		return nil, nil
	}

	updatedAt := time.Now().UTC().Format(time.RFC3339Nano)
	for _, task := range tasks {
		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET status = ?, updated_at = ? WHERE id = ?`, StatusRunning, updatedAt, task.ID); err != nil {
			return nil, fmt.Errorf("mark running: %w", err)
		}
		_ = m.RecordUpdate(ctx, task.ID, "started", map[string]any{"status": StatusRunning})
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit claim: %w", err)
	}
	return tasks, nil
}

func (m *Manager) updateStatus(ctx context.Context, taskID string, status Status, payload map[string]any, kind string) error {
	resultJSON, err := encodeJSON(payload)
	if err != nil {
		return fmt.Errorf("encode result: %w", err)
	}
	updatedAt := time.Now().UTC()

	_, err = m.db.ExecContext(ctx, `
		UPDATE tasks SET status = ?, updated_at = ?, result = ?, error = ? WHERE id = ?
	`, status, updatedAt.Format(time.RFC3339Nano), resultJSON, extractError(payload), taskID)
	if err != nil {
		return fmt.Errorf("update task status: %w", err)
	}

	return m.RecordUpdate(ctx, taskID, kind, payload)
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

func decodeJSONMap(v string) map[string]any {
	if v == "" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(v), &out); err != nil {
		return nil
	}
	return out
}

func nullString(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func extractError(payload map[string]any) any {
	if payload == nil {
		return nil
	}
	if errVal, ok := payload["error"]; ok {
		return errVal
	}
	if reason, ok := payload["reason"]; ok {
		return reason
	}
	return nil
}
