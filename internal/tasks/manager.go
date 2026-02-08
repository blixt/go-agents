package tasks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/flitsinc/go-agents/internal/agentcontext"
	"github.com/flitsinc/go-agents/internal/eventbus"
	"github.com/flitsinc/go-agents/internal/idgen"
	"github.com/flitsinc/go-agents/internal/schema"
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
	ID       string         `json:"id,omitempty"`
	Type     string         `json:"type"`
	Name     string         `json:"name,omitempty"`
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

	nowFn   func() time.Time
	newIDFn func(string) string
}

var ErrAwaitTimeout = errors.New("await timeout")
var ErrInvalidStatusTransition = errors.New("invalid task status transition")
var WakeGrace = 200 * time.Millisecond

type StatusTransitionError struct {
	TaskID string
	From   Status
	To     Status
}

func (e *StatusTransitionError) Error() string {
	return fmt.Sprintf("invalid task status transition for %s: %s -> %s", e.TaskID, e.From, e.To)
}

func (e *StatusTransitionError) Unwrap() error {
	return ErrInvalidStatusTransition
}

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

var wakeStreams = schema.AgentStreams

type Option func(*Manager)

func WithClock(nowFn func() time.Time) Option {
	return func(m *Manager) {
		if nowFn != nil {
			m.nowFn = nowFn
		}
	}
}

func WithIDGenerator(newIDFn func(string) string) Option {
	return func(m *Manager) {
		if newIDFn != nil {
			m.newIDFn = newIDFn
		}
	}
}

func NewManager(db *sql.DB, bus *eventbus.Bus, opts ...Option) *Manager {
	m := &Manager{
		db:      db,
		bus:     bus,
		nowFn:   func() time.Time { return time.Now().UTC() },
		newIDFn: func(prefix string) string {
			if prefix != "" {
				return idgen.TaskID(db, prefix)
			}
			return idgen.New()
		},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(m)
		}
	}
	return m
}

func (m *Manager) now() time.Time {
	if m.nowFn == nil {
		return time.Now().UTC()
	}
	return m.nowFn().UTC()
}

func (m *Manager) newID(prefix string) string {
	if m.newIDFn == nil {
		if prefix != "" {
			return idgen.TaskID(m.db, prefix)
		}
		return idgen.New()
	}
	return m.newIDFn(prefix)
}

func (m *Manager) Spawn(ctx context.Context, spec Spec) (Task, error) {
	if strings.TrimSpace(spec.Type) == "" {
		return Task{}, fmt.Errorf("task type is required")
	}
	var id string
	if spec.ID != "" {
		id = spec.ID
	} else {
		prefix := spec.Type
		if spec.Name != "" {
			prefix = spec.Name
		}
		id = m.newID(prefix)
	}
	createdAt := m.now()
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
		scopeType, scopeID := scopeForTarget(schema.GetMetaString(metadata, "input_target"))
		_, _ = m.bus.Push(ctx, eventbus.EventInput{
			Stream:    schema.StreamSignals,
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
	task.ParentID = schema.GetMetaString(task.Metadata, "parent_id")
	task.Mode = schema.GetMetaString(task.Metadata, "mode")
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
		task.ParentID = schema.GetMetaString(task.Metadata, "parent_id")
		task.Mode = schema.GetMetaString(task.Metadata, "mode")
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
	id := m.newID("")
	createdAt := m.now()
	payloadJSON, err := encodeJSON(payload)
	if err != nil {
		return fmt.Errorf("encode payload: %w", err)
	}

	if err := execWithRetry(ctx, m.db, `
		INSERT INTO task_updates (id, task_id, kind, payload, created_at)
		VALUES (?, ?, ?, ?, ?)
	`, id, taskID, kind, payloadJSON, createdAt.Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("insert task update: %w", err)
	}

	if err := execWithRetry(ctx, m.db, `UPDATE tasks SET updated_at = ? WHERE id = ?`, createdAt.Format(time.RFC3339Nano), taskID); err != nil {
		return fmt.Errorf("update task timestamp: %w", err)
	}

	if m.bus != nil {
		scopeType, scopeID := scopeForTarget(m.taskTarget(ctx, taskID, "notify_target"))
		priority := taskUpdatePriority(kind, payload)
		sourceID := strings.TrimSpace(agentcontext.TaskIDFromContext(ctx))
		_, _ = m.bus.Push(ctx, eventbus.EventInput{
			Stream:    schema.StreamTaskOutput,
			ScopeType: scopeType,
			ScopeID:   scopeID,
			Subject:   fmt.Sprintf("Task %s update", taskID),
			Body:      kind,
			Metadata: map[string]any{
				"kind":      "task_update",
				"task_id":   taskID,
				"task_kind": kind,
				"priority":  priority,
			},
			Payload:  payload,
			SourceID: sourceID,
		})
	}
	return nil
}

func (m *Manager) MarkRunning(ctx context.Context, taskID string) error {
	if taskID == "" {
		return fmt.Errorf("task_id is required")
	}
	updatedAt := m.now()
	res, err := m.db.ExecContext(ctx, `UPDATE tasks SET status = ?, updated_at = ? WHERE id = ? AND status = ?`, StatusRunning, updatedAt.Format(time.RFC3339Nano), taskID, StatusQueued)
	if err != nil {
		return fmt.Errorf("update task status: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update task status rows affected: %w", err)
	}
	if affected == 0 {
		current, err := m.currentStatus(ctx, taskID)
		if err != nil {
			return err
		}
		if current == StatusRunning {
			return nil
		}
		return &StatusTransitionError{TaskID: taskID, From: current, To: StatusRunning}
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
			Stream:    schema.StreamTaskInput,
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

// AckTaskOutput marks all unread task_output events for the given task as read
// from the perspective of the reader. This prevents collectUnreadContextEvents
// from re-delivering results that were already returned via tool_result.
func (m *Manager) AckTaskOutput(ctx context.Context, taskID, readerID string) {
	if m.bus == nil || taskID == "" || readerID == "" {
		return
	}
	summaries, err := m.bus.List(ctx, "task_output", eventbus.ListOptions{
		ScopeType: "task",
		ScopeID:   readerID,
		Reader:    readerID,
		Limit:     50,
	})
	if err != nil || len(summaries) == 0 {
		return
	}
	var unreadIDs []string
	for _, s := range summaries {
		if !s.Read {
			unreadIDs = append(unreadIDs, s.ID)
		}
	}
	if len(unreadIDs) == 0 {
		return
	}
	events, err := m.bus.Read(ctx, "task_output", unreadIDs, readerID)
	if err != nil {
		return
	}
	var ackIDs []string
	for _, evt := range events {
		if schema.GetMetaString(evt.Metadata, "task_id") == taskID {
			ackIDs = append(ackIDs, evt.ID)
		}
	}
	if len(ackIDs) > 0 {
		_ = m.bus.Ack(ctx, "task_output", ackIDs, readerID)
	}
}

func (m *Manager) Await(ctx context.Context, taskID string, timeout time.Duration) (Task, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	pollTicker := time.NewTicker(500 * time.Millisecond)
	defer pollTicker.Stop()

	var sub <-chan eventbus.Event
	if m.bus != nil {
		streams := append([]string{}, wakeStreams...)
		sub = m.bus.Subscribe(ctx, streams)
	}
	ignoredWakeEventIDs := IgnoredWakeEventIDsFromContext(ctx)

	for {
		task, err := m.Get(ctx, taskID)
		if err != nil {
			return Task{}, err
		}
		if IsTerminalStatus(task.Status) {
			return task, nil
		}

		if m.bus == nil {
			select {
			case <-pollTicker.C:
			case <-ctx.Done():
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					_ = m.RecordUpdate(context.Background(), taskID, "await_timeout", map[string]any{"timeout_ms": timeout.Milliseconds()})
					return task, ErrAwaitTimeout
				}
				return task, ctx.Err()
			}
			continue
		}

		targets := awaitTargetsForTask(task)
		reader := awaitReaderForTargets(targets)
		if evt, priority, ok, err := m.nextUnreadWakeEvent(ctx, targets, reader, 25); err != nil {
			return task, err
		} else if ok {
			if shouldIgnoreWakeEvent(evt, ignoredWakeEventIDs) {
				_ = m.bus.Ack(ctx, evt.Stream, []string{evt.ID}, reader)
				continue
			}
			if err := applyWakeGrace(ctx, evt); err != nil {
				return task, err
			}
			current, _ := m.Get(ctx, taskID)
			if IsTerminalStatus(current.Status) {
				if !preserveTerminalTaskWakeEvent(evt, taskID) {
					_ = m.bus.Ack(ctx, evt.Stream, []string{evt.ID}, reader)
				}
				return current, nil
			}
			_ = m.bus.Ack(ctx, evt.Stream, []string{evt.ID}, reader)
			return current, &WakeError{Event: evt, Priority: priority}
		}

		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				_ = m.RecordUpdate(context.Background(), taskID, "await_timeout", map[string]any{"timeout_ms": timeout.Milliseconds()})
				return task, ErrAwaitTimeout
			}
			return task, ctx.Err()
		case <-pollTicker.C:
			continue
		case evt, ok := <-sub:
			if !ok {
				return task, ctx.Err()
			}
			if wake, priority := wakeInfo(evt); wake {
				if !eventMatchesAwaitTargets(evt, targets) {
					continue
				}
				if shouldIgnoreWakeEvent(evt, ignoredWakeEventIDs) {
					_ = m.bus.Ack(ctx, evt.Stream, []string{evt.ID}, reader)
					continue
				}
				if err := applyWakeGrace(ctx, evt); err != nil {
					return task, err
				}
				current, _ := m.Get(ctx, taskID)
				if IsTerminalStatus(current.Status) {
					if !preserveTerminalTaskWakeEvent(evt, taskID) {
						_ = m.bus.Ack(ctx, evt.Stream, []string{evt.ID}, reader)
					}
					return current, nil
				}
				_ = m.bus.Ack(ctx, evt.Stream, []string{evt.ID}, reader)
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

	pollTicker := time.NewTicker(500 * time.Millisecond)
	defer pollTicker.Stop()

	var sub <-chan eventbus.Event
	if m.bus != nil {
		streams := append([]string{}, wakeStreams...)
		sub = m.bus.Subscribe(ctx, streams)
	}
	ignoredWakeEventIDs := IgnoredWakeEventIDsFromContext(ctx)

	for {
		pending := make([]string, 0, len(taskIDs))
		targets := map[string]struct{}{}
		for _, id := range taskIDs {
			task, err := m.Get(ctx, id)
			if err != nil {
				return AwaitAnyResult{}, err
			}
			if IsTerminalStatus(task.Status) {
				return AwaitAnyResult{
					TaskID:     id,
					Task:       task,
					PendingIDs: removeID(taskIDs, id),
				}, nil
			}
			pending = append(pending, id)
			for target := range awaitTargetsForTask(task) {
				targets[target] = struct{}{}
			}
		}
		reader := awaitReaderForTargets(targets)
		if evt, priority, ok, err := m.nextUnreadWakeEvent(ctx, targets, reader, 25); err != nil {
			return AwaitAnyResult{}, err
		} else if ok {
			if shouldIgnoreWakeEvent(evt, ignoredWakeEventIDs) {
				_ = m.bus.Ack(ctx, evt.Stream, []string{evt.ID}, reader)
				continue
			}
			if err := applyWakeGrace(ctx, evt); err != nil {
				return AwaitAnyResult{PendingIDs: pending}, err
			}
			if completed, done, pendingIDs, err := m.firstCompletedTask(ctx, taskIDs); err != nil {
				return AwaitAnyResult{PendingIDs: pending}, err
			} else if done {
				if !preserveTerminalTaskWakeEvent(evt, completed.ID) {
					_ = m.bus.Ack(ctx, evt.Stream, []string{evt.ID}, reader)
				}
				return AwaitAnyResult{
					TaskID:     completed.ID,
					Task:       completed,
					PendingIDs: pendingIDs,
				}, nil
			}
			_ = m.bus.Ack(ctx, evt.Stream, []string{evt.ID}, reader)
			return AwaitAnyResult{
				PendingIDs:   pending,
				WakeEvent:    &evt,
				WakePriority: priority,
			}, &WakeError{Event: evt, Priority: priority}
		}

		if m.bus == nil {
			select {
			case <-pollTicker.C:
			case <-ctx.Done():
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					recordAwaitTimeouts(taskIDs, timeout, m)
					return AwaitAnyResult{PendingIDs: pending}, ErrAwaitTimeout
				}
				return AwaitAnyResult{PendingIDs: pending}, ctx.Err()
			}
			continue
		}

		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				recordAwaitTimeouts(taskIDs, timeout, m)
				return AwaitAnyResult{PendingIDs: pending}, ErrAwaitTimeout
			}
			return AwaitAnyResult{PendingIDs: pending}, ctx.Err()
		case <-pollTicker.C:
			continue
		case evt, ok := <-sub:
			if !ok {
				return AwaitAnyResult{PendingIDs: pending}, ctx.Err()
			}
			if wake, priority := wakeInfo(evt); wake {
				if !eventMatchesAwaitTargets(evt, targets) {
					continue
				}
				if shouldIgnoreWakeEvent(evt, ignoredWakeEventIDs) {
					_ = m.bus.Ack(ctx, evt.Stream, []string{evt.ID}, reader)
					continue
				}
				if err := applyWakeGrace(ctx, evt); err != nil {
					return AwaitAnyResult{PendingIDs: pending}, err
				}
				if completed, done, pendingIDs, err := m.firstCompletedTask(ctx, taskIDs); err != nil {
					return AwaitAnyResult{PendingIDs: pending}, err
				} else if done {
					if !preserveTerminalTaskWakeEvent(evt, completed.ID) {
						_ = m.bus.Ack(ctx, evt.Stream, []string{evt.ID}, reader)
					}
					return AwaitAnyResult{
						TaskID:     completed.ID,
						Task:       completed,
						PendingIDs: pendingIDs,
					}, nil
				}
				_ = m.bus.Ack(ctx, evt.Stream, []string{evt.ID}, reader)
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

func (m *Manager) ListUpdatesSince(ctx context.Context, taskID, afterID, kind string, limit int) ([]Update, error) {
	if taskID == "" {
		return nil, fmt.Errorf("task_id is required")
	}
	if limit <= 0 {
		limit = 200
	}

	query := `
		SELECT id, task_id, kind, payload, created_at
		FROM task_updates
		WHERE task_id = ?
	`
	args := []any{taskID}
	if kind != "" {
		query += " AND kind = ?"
		args = append(args, kind)
	}
	if afterID != "" {
		query += " AND id > ?"
		args = append(args, afterID)
	}
	query += " ORDER BY id ASC LIMIT ?"
	args = append(args, limit)

	rows, err := m.db.QueryContext(ctx, query, args...)
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
		task.ParentID = schema.GetMetaString(task.Metadata, "parent_id")
		task.Mode = schema.GetMetaString(task.Metadata, "mode")
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

	updatedAt := m.now().Format(time.RFC3339Nano)
	claimed := make([]Task, 0, len(tasks))
	for _, task := range tasks {
		res, err := tx.ExecContext(ctx, `UPDATE tasks SET status = ?, updated_at = ? WHERE id = ? AND status = ?`, StatusRunning, updatedAt, task.ID, StatusQueued)
		if err != nil {
			return nil, fmt.Errorf("mark running: %w", err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return nil, fmt.Errorf("mark running rows affected: %w", err)
		}
		if affected == 0 {
			continue
		}
		task.Status = StatusRunning
		if parsed, err := time.Parse(time.RFC3339Nano, updatedAt); err == nil {
			task.UpdatedAt = parsed
		}
		claimed = append(claimed, task)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit claim: %w", err)
	}
	if len(claimed) == 0 {
		return nil, nil
	}
	for _, task := range claimed {
		_ = m.RecordUpdate(context.Background(), task.ID, "started", map[string]any{"status": StatusRunning})
	}
	return claimed, nil
}

func (m *Manager) updateStatus(ctx context.Context, taskID string, status Status, payload map[string]any, kind string) error {
	current, err := m.currentStatus(ctx, taskID)
	if err != nil {
		return err
	}
	if current == status {
		return nil
	}
	if !canTransition(current, status) {
		return &StatusTransitionError{TaskID: taskID, From: current, To: status}
	}

	resultJSON, err := encodeJSON(payload)
	if err != nil {
		return fmt.Errorf("encode result: %w", err)
	}
	updatedAt := m.now()

	res, err := m.db.ExecContext(ctx, `
		UPDATE tasks SET status = ?, updated_at = ?, result = ?, error = ? WHERE id = ? AND status = ?
	`, status, updatedAt.Format(time.RFC3339Nano), resultJSON, extractError(payload), taskID, current)
	if err != nil {
		return fmt.Errorf("update task status: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update task status rows affected: %w", err)
	}
	if affected == 0 {
		latest, err := m.currentStatus(ctx, taskID)
		if err != nil {
			return err
		}
		if latest == status {
			return nil
		}
		return &StatusTransitionError{TaskID: taskID, From: latest, To: status}
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
			Stream:    schema.StreamSignals,
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
		if schema.GetMetaString(meta, "parent_id") == parentID {
			out = append(out, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate child tasks: %w", err)
	}
	return out, nil
}

func (m *Manager) currentStatus(ctx context.Context, taskID string) (Status, error) {
	if taskID == "" {
		return "", fmt.Errorf("task_id is required")
	}
	var status Status
	err := m.db.QueryRowContext(ctx, `SELECT status FROM tasks WHERE id = ?`, taskID).Scan(&status)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("task not found")
		}
		return "", fmt.Errorf("load task status: %w", err)
	}
	return status, nil
}

func canTransition(from, to Status) bool {
	if from == to {
		return true
	}
	switch from {
	case StatusQueued:
		return to == StatusRunning || to == StatusCompleted || to == StatusFailed || to == StatusCancelled
	case StatusRunning:
		return to == StatusCompleted || to == StatusFailed || to == StatusCancelled
	case StatusCompleted, StatusFailed, StatusCancelled:
		return false
	default:
		return false
	}
}

type wakeEventRef struct {
	Stream    string
	ID        string
	CreatedAt time.Time
}

func (m *Manager) nextUnreadWakeEvent(ctx context.Context, targets map[string]struct{}, reader string, limit int) (eventbus.Event, string, bool, error) {
	if m.bus == nil {
		return eventbus.Event{}, "", false, nil
	}
	if limit <= 0 {
		limit = 25
	}

	var scopes []eventbus.ListOptions
	scopes = append(scopes, eventbus.ListOptions{
		ScopeType: "global",
		ScopeID:   "*",
		Limit:     limit,
		Order:     "fifo",
	})
	for target := range targets {
		target = strings.TrimSpace(target)
		if target == "" {
			continue
		}
		scopes = append(scopes, eventbus.ListOptions{
			ScopeType: "task",
			ScopeID:   target,
			Limit:     limit,
			Order:     "fifo",
		})
	}

	seen := map[string]struct{}{}
	refs := make([]wakeEventRef, 0, len(wakeStreams)*len(scopes))
	for _, stream := range wakeStreams {
		for _, scope := range scopes {
			summaries, err := m.bus.List(ctx, stream, scope)
			if err != nil {
				return eventbus.Event{}, "", false, err
			}
			for _, summary := range summaries {
				key := stream + ":" + summary.ID
				if _, ok := seen[key]; ok {
					continue
				}
				seen[key] = struct{}{}
				refs = append(refs, wakeEventRef{
					Stream:    stream,
					ID:        summary.ID,
					CreatedAt: summary.CreatedAt,
				})
			}
		}
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].CreatedAt.Equal(refs[j].CreatedAt) {
			if refs[i].Stream == refs[j].Stream {
				return refs[i].ID < refs[j].ID
			}
			return refs[i].Stream < refs[j].Stream
		}
		return refs[i].CreatedAt.Before(refs[j].CreatedAt)
	})

	for _, ref := range refs {
		events, err := m.bus.Read(ctx, ref.Stream, []string{ref.ID}, reader)
		if err != nil {
			return eventbus.Event{}, "", false, err
		}
		if len(events) == 0 {
			continue
		}
		evt := events[0]
		if evt.Read {
			continue
		}
		if wake, priority := wakeInfo(evt); wake && eventMatchesAwaitTargets(evt, targets) {
			return evt, priority, true, nil
		}
	}
	return eventbus.Event{}, "", false, nil
}

func (m *Manager) taskTarget(ctx context.Context, taskID, key string) string {
	meta, err := m.taskMetadata(ctx, taskID)
	if err != nil {
		return ""
	}
	return schema.GetMetaString(meta, key)
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
	return "task", target
}

func wakeInfo(evt eventbus.Event) (bool, string) {
	if evt.Metadata == nil {
		return false, ""
	}
	if val, ok := evt.Metadata["priority"].(string); ok {
		p := schema.ParsePriority(val)
		if p != schema.PriorityNormal {
			return p.Wakes(), string(p)
		}
	}
	return false, ""
}

func shouldIgnoreWakeEvent(evt eventbus.Event, ignoredIDs map[string]struct{}) bool {
	if len(ignoredIDs) == 0 {
		return false
	}
	_, ok := ignoredIDs[strings.TrimSpace(evt.ID)]
	return ok
}

func preserveTerminalTaskWakeEvent(evt eventbus.Event, taskID string) bool {
	if strings.TrimSpace(taskID) == "" {
		return false
	}
	if strings.TrimSpace(evt.Stream) != "task_output" {
		return false
	}
	if strings.TrimSpace(schema.GetMetaString(evt.Metadata, "task_id")) != strings.TrimSpace(taskID) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(schema.GetMetaString(evt.Metadata, "task_kind"))) {
	case "completed", "failed", "cancelled", "killed":
		return true
	default:
		return false
	}
}

func awaitReaderForTargets(targets map[string]struct{}) string {
	if len(targets) == 0 {
		return "runtime"
	}
	names := make([]string, 0, len(targets))
	for target := range targets {
		target = strings.TrimSpace(target)
		if target == "" {
			continue
		}
		names = append(names, target)
	}
	if len(names) == 0 {
		return "runtime"
	}
	sort.Strings(names)
	return names[0]
}

func awaitTargetsForTask(task Task) map[string]struct{} {
	out := map[string]struct{}{}
	if target := schema.GetMetaString(task.Metadata, "notify_target"); target != "" {
		out[target] = struct{}{}
	}
	if task.Owner != "" {
		out[task.Owner] = struct{}{}
	}
	return out
}

func eventMatchesAwaitTargets(evt eventbus.Event, targets map[string]struct{}) bool {
	if evt.ScopeType == "task" {
		if evt.ScopeID == "" {
			return false
		}
		_, ok := targets[evt.ScopeID]
		return ok
	}
	if evt.ScopeType == "global" || evt.ScopeType == "" {
		if evt.ScopeID == "" || evt.ScopeID == "*" {
			return true
		}
		_, ok := targets[evt.ScopeID]
		return ok
	}
	if evt.ScopeID == "" || evt.ScopeID == "*" {
		return true
	}
	_, ok := targets[evt.ScopeID]
	return ok
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

func IsTerminalStatus(status Status) bool {
	switch status {
	case StatusCompleted, StatusFailed, StatusCancelled:
		return true
	default:
		return false
	}
}

func taskUpdatePriority(kind string, payload map[string]any) string {
	if payload != nil {
		if raw, ok := payload["priority"].(string); ok {
			p := schema.ParsePriority(raw)
			return string(p)
		}
	}
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "completed", "failed", "cancelled", "killed":
		return string(schema.PriorityWake)
	default:
		return string(schema.PriorityNormal)
	}
}

func (m *Manager) firstCompletedTask(ctx context.Context, taskIDs []string) (Task, bool, []string, error) {
	for _, id := range taskIDs {
		task, err := m.Get(ctx, id)
		if err != nil {
			return Task{}, false, nil, err
		}
		if IsTerminalStatus(task.Status) {
			return task, true, removeID(taskIDs, id), nil
		}
	}
	return Task{}, false, append([]string{}, taskIDs...), nil
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

func execWithRetry(ctx context.Context, db *sql.DB, query string, args ...any) error {
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		_, err = db.ExecContext(ctx, query, args...)
		if err == nil {
			return nil
		}
		if !isBusyError(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(time.Duration(25*(attempt+1)) * time.Millisecond):
		}
	}
	return err
}

func isBusyError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "database is locked") || strings.Contains(msg, "SQLITE_BUSY")
}
