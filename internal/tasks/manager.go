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
	ParentID  string         `json:"parent_id,omitempty"`
	Mode      string         `json:"mode,omitempty"`
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
	ParentID string         `json:"parent_id,omitempty"`
	Mode     string         `json:"mode,omitempty"`
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

var ErrAwaitTimeout = errors.New("await timeout")
var WakeGrace = 200 * time.Millisecond

type WakeError struct {
	Event    eventbus.Event
	Priority string
}

func (e *WakeError) Error() string {
	priority := e.Priority
	if priority == "" {
		priority = "wake"
	}
	return fmt.Sprintf("awoken by %s event", priority)
}

func IsAwaitTimeout(err error) bool {
	return errors.Is(err, ErrAwaitTimeout)
}

func AsWakeError(err error) (*WakeError, bool) {
	var wakeErr *WakeError
	if errors.As(err, &wakeErr) {
		return wakeErr, true
	}
	return nil, false
}

func IsInterrupt(err error) bool {
	if wakeErr, ok := AsWakeError(err); ok {
		return strings.EqualFold(wakeErr.Priority, "interrupt")
	}
	return false
}

var defaultWakeStreams = []string{"signals", "errors", "external", "messages"}

const defaultAwaitReader = "operator"

func NewManager(db *sql.DB, bus *eventbus.Bus) *Manager {
	return &Manager{db: db, bus: bus}
}

func (m *Manager) Spawn(ctx context.Context, spec Spec) (Task, error) {
	if strings.TrimSpace(spec.Type) == "" {
		return Task{}, fmt.Errorf("task type is required")
	}
	id := ulid.Make().String()
	createdAt := time.Now().UTC()
	metadata := map[string]any{}
	for k, v := range spec.Metadata {
		metadata[k] = v
	}
	if spec.ParentID != "" {
		if _, ok := metadata["parent_id"]; !ok {
			metadata["parent_id"] = spec.ParentID
		}
	}
	if spec.Mode != "" {
		if _, ok := metadata["mode"]; !ok {
			metadata["mode"] = spec.Mode
		}
	}
	metadataJSON, err := encodeJSON(metadata)
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
		ParentID:  spec.ParentID,
		Mode:      spec.Mode,
		Metadata:  metadata,
		Payload:   spec.Payload,
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	}

	if m.bus != nil {
		scopeType, scopeID := scopeForTarget(getMetaString(metadata, "input_target"))
		_, _ = m.bus.Push(ctx, eventbus.EventInput{
			Stream:    "task_input",
			ScopeType: scopeType,
			ScopeID:   scopeID,
			Subject:   fmt.Sprintf("Task request %s", id),
			Body:      fmt.Sprintf("Spawn task %s (%s)", id, spec.Type),
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
	task.ParentID = getMetaString(task.Metadata, "parent_id")
	task.Mode = getMetaString(task.Metadata, "mode")
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
		task.ParentID = getMetaString(task.Metadata, "parent_id")
		task.Mode = getMetaString(task.Metadata, "mode")
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
		scopeType, scopeID := scopeForTarget(m.taskTarget(ctx, taskID, "notify_target"))
		_, _ = m.bus.Push(ctx, eventbus.EventInput{
			Stream:    "task_output",
			ScopeType: scopeType,
			ScopeID:   scopeID,
			Subject:   fmt.Sprintf("Task %s update", taskID),
			Body:      kind,
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

func (m *Manager) MarkRunning(ctx context.Context, taskID string) error {
	if taskID == "" {
		return fmt.Errorf("task_id is required")
	}
	updatedAt := time.Now().UTC()
	_, err := m.db.ExecContext(ctx, `UPDATE tasks SET status = ?, updated_at = ? WHERE id = ?`, StatusRunning, updatedAt.Format(time.RFC3339Nano), taskID)
	if err != nil {
		return fmt.Errorf("update task status: %w", err)
	}
	return m.RecordUpdate(ctx, taskID, "started", map[string]any{"status": StatusRunning})
}

func (m *Manager) Complete(ctx context.Context, taskID string, result map[string]any) error {
	return m.updateStatus(ctx, taskID, StatusCompleted, result, "completed")
}

func (m *Manager) Fail(ctx context.Context, taskID string, reason string) error {
	payload := map[string]any{"error": reason}
	return m.updateStatus(ctx, taskID, StatusFailed, payload, "failed")
}

func (m *Manager) Cancel(ctx context.Context, taskID string, reason string) error {
	return m.cancelWithChildren(ctx, taskID, reason, false, map[string]struct{}{})
}

func (m *Manager) Kill(ctx context.Context, taskID string, reason string) error {
	return m.cancelWithChildren(ctx, taskID, reason, true, map[string]struct{}{})
}

func (m *Manager) Send(ctx context.Context, taskID string, input map[string]any) error {
	if input == nil {
		input = map[string]any{}
	}
	if m.bus != nil {
		scopeType, scopeID := scopeForTarget(m.taskTarget(ctx, taskID, "input_target"))
		_, _ = m.bus.Push(ctx, eventbus.EventInput{
			Stream:    "task_input",
			ScopeType: scopeType,
			ScopeID:   scopeID,
			Subject:   fmt.Sprintf("Task input %s", taskID),
			Body:      fmt.Sprintf("Send input to task %s", taskID),
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
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					_ = m.RecordUpdate(context.Background(), taskID, "await_timeout", map[string]any{"timeout_ms": timeout.Milliseconds()})
					return task, ErrAwaitTimeout
				}
				return task, ctx.Err()
			}
			continue
		}

		streams := append([]string{"task_output"}, defaultWakeStreams...)
		sub := m.bus.Subscribe(ctx, streams)
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				_ = m.RecordUpdate(context.Background(), taskID, "await_timeout", map[string]any{"timeout_ms": timeout.Milliseconds()})
				return task, ErrAwaitTimeout
			}
			return task, ctx.Err()
		case evt, ok := <-sub:
			if !ok {
				return task, ctx.Err()
			}
			if evt.Stream == "task_output" {
				if evt.Metadata != nil {
					if id, ok := evt.Metadata["task_id"].(string); ok && id == taskID {
						continue
					}
				}
				continue
			}
			if wake, priority := wakeInfo(evt); wake {
				if err := applyWakeGrace(ctx, evt); err != nil {
					return task, err
				}
				_ = m.bus.Ack(ctx, evt.Stream, []string{evt.ID}, defaultAwaitReader)
				current, _ := m.Get(ctx, taskID)
				return current, &WakeError{Event: evt, Priority: priority}
			}
		}
	}
}

type AwaitAnyResult struct {
	TaskID       string
	Task         Task
	PendingIDs   []string
	WakeEvent    *eventbus.Event
	WakePriority string
}

func (m *Manager) AwaitAny(ctx context.Context, taskIDs []string, timeout time.Duration) (AwaitAnyResult, error) {
	if len(taskIDs) == 0 {
		return AwaitAnyResult{}, fmt.Errorf("task_ids is required")
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	for {
		pending := make([]string, 0, len(taskIDs))
		for _, id := range taskIDs {
			task, err := m.Get(ctx, id)
			if err != nil {
				return AwaitAnyResult{}, err
			}
			if task.Status == StatusCompleted || task.Status == StatusFailed || task.Status == StatusCancelled {
				return AwaitAnyResult{
					TaskID:     id,
					Task:       task,
					PendingIDs: removeID(taskIDs, id),
				}, nil
			}
			pending = append(pending, id)
		}

		if m.bus == nil {
			select {
			case <-time.After(500 * time.Millisecond):
			case <-ctx.Done():
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					recordAwaitTimeouts(taskIDs, timeout, m)
					return AwaitAnyResult{PendingIDs: pending}, ErrAwaitTimeout
				}
				return AwaitAnyResult{PendingIDs: pending}, ctx.Err()
			}
			continue
		}

		streams := append([]string{"task_output"}, defaultWakeStreams...)
		sub := m.bus.Subscribe(ctx, streams)
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				recordAwaitTimeouts(taskIDs, timeout, m)
				return AwaitAnyResult{PendingIDs: pending}, ErrAwaitTimeout
			}
			return AwaitAnyResult{PendingIDs: pending}, ctx.Err()
		case evt, ok := <-sub:
			if !ok {
				return AwaitAnyResult{PendingIDs: pending}, ctx.Err()
			}
			if evt.Stream == "task_output" {
				if evt.Metadata != nil {
					if id, ok := evt.Metadata["task_id"].(string); ok && containsID(taskIDs, id) {
						continue
					}
				}
				continue
			}
			if wake, priority := wakeInfo(evt); wake {
				if err := applyWakeGrace(ctx, evt); err != nil {
					return AwaitAnyResult{PendingIDs: pending}, err
				}
				_ = m.bus.Ack(ctx, evt.Stream, []string{evt.ID}, defaultAwaitReader)
				return AwaitAnyResult{
					PendingIDs:   pending,
					WakeEvent:    &evt,
					WakePriority: priority,
				}, &WakeError{Event: evt, Priority: priority}
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
		task.ParentID = getMetaString(task.Metadata, "parent_id")
		task.Mode = getMetaString(task.Metadata, "mode")
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

func (m *Manager) cancelWithChildren(ctx context.Context, taskID string, reason string, kill bool, visited map[string]struct{}) error {
	if taskID == "" {
		return fmt.Errorf("task_id is required")
	}
	if _, ok := visited[taskID]; ok {
		return nil
	}
	visited[taskID] = struct{}{}

	payload := map[string]any{"reason": reason}
	kind := "cancelled"
	action := "cancel"
	if kill {
		payload["killed"] = true
		kind = "killed"
		action = "kill"
	}
	if m.bus != nil {
		scopeType, scopeID := scopeForTarget(m.taskTarget(ctx, taskID, "input_target"))
		verb := action
		if len(verb) > 0 {
			verb = strings.ToUpper(verb[:1]) + verb[1:]
		}
		_, _ = m.bus.Push(ctx, eventbus.EventInput{
			Stream:    "task_input",
			ScopeType: scopeType,
			ScopeID:   scopeID,
			Subject:   fmt.Sprintf("Task %s %s", action, taskID),
			Body:      fmt.Sprintf("%s task %s", verb, taskID),
			Metadata: map[string]any{
				"kind":    "command",
				"action":  action,
				"task_id": taskID,
			},
			Payload: payload,
		})
	}
	if err := m.updateStatus(ctx, taskID, StatusCancelled, payload, kind); err != nil {
		return err
	}
	children, err := m.childTaskIDs(ctx, taskID)
	if err != nil {
		return err
	}
	for _, child := range children {
		if err := m.cancelWithChildren(ctx, child, reason, kill, visited); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) childTaskIDs(ctx context.Context, parentID string) ([]string, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT id, metadata FROM tasks`)
	if err != nil {
		return nil, fmt.Errorf("list tasks for children: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var id, metadataStr string
		if err := rows.Scan(&id, &metadataStr); err != nil {
			return nil, fmt.Errorf("scan child task: %w", err)
		}
		meta := decodeJSONMap(metadataStr)
		if getMetaString(meta, "parent_id") == parentID {
			out = append(out, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate child tasks: %w", err)
	}
	return out, nil
}

func getMetaString(meta map[string]any, key string) string {
	if meta == nil {
		return ""
	}
	if val, ok := meta[key]; ok {
		if str, ok := val.(string); ok {
			return str
		}
	}
	return ""
}

func (m *Manager) taskTarget(ctx context.Context, taskID, key string) string {
	meta, err := m.taskMetadata(ctx, taskID)
	if err != nil {
		return ""
	}
	return getMetaString(meta, key)
}

func (m *Manager) taskMetadata(ctx context.Context, taskID string) (map[string]any, error) {
	if taskID == "" {
		return nil, fmt.Errorf("task_id is required")
	}
	var metadataStr string
	err := m.db.QueryRowContext(ctx, `SELECT metadata FROM tasks WHERE id = ?`, taskID).Scan(&metadataStr)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("task not found")
		}
		return nil, err
	}
	return decodeJSONMap(metadataStr), nil
}

func scopeForTarget(target string) (string, string) {
	if strings.TrimSpace(target) == "" {
		return "", ""
	}
	return "agent", target
}

func wakeInfo(evt eventbus.Event) (bool, string) {
	if evt.Metadata == nil {
		return false, ""
	}
	if val, ok := evt.Metadata["priority"].(string); ok {
		switch strings.ToLower(val) {
		case "wake", "interrupt":
			return true, strings.ToLower(val)
		}
	}
	if val, ok := evt.Metadata["wake"].(bool); ok && val {
		return true, "wake"
	}
	return false, ""
}

func containsID(list []string, id string) bool {
	for _, item := range list {
		if item == id {
			return true
		}
	}
	return false
}

func removeID(list []string, id string) []string {
	out := make([]string, 0, len(list))
	for _, item := range list {
		if item == id {
			continue
		}
		out = append(out, item)
	}
	return out
}

func recordAwaitTimeouts(taskIDs []string, timeout time.Duration, m *Manager) {
	for _, id := range taskIDs {
		_ = m.RecordUpdate(context.Background(), id, "await_timeout", map[string]any{"timeout_ms": timeout.Milliseconds()})
	}
}

func applyWakeGrace(ctx context.Context, evt eventbus.Event) error {
	if WakeGrace <= 0 {
		return nil
	}
	elapsed := time.Since(evt.CreatedAt)
	remaining := WakeGrace - elapsed
	if remaining <= 0 {
		return nil
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
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
