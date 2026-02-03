package eventbus

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

type Bus struct {
	db *sql.DB

	mu   sync.RWMutex
	subs map[string]*subscriber
}

type subscriber struct {
	streams map[string]struct{}
	ch      chan Event
}

func NewBus(db *sql.DB) *Bus {
	return &Bus{db: db, subs: map[string]*subscriber{}}
}

func (b *Bus) Push(ctx context.Context, input EventInput) (Event, error) {
	if strings.TrimSpace(input.Stream) == "" {
		return Event{}, fmt.Errorf("stream is required")
	}
	if strings.TrimSpace(input.Body) == "" {
		return Event{}, fmt.Errorf("body is required")
	}

	scopeType := input.ScopeType
	scopeID := input.ScopeID
	if scopeType == "" {
		scopeType = "global"
	}
	if scopeID == "" {
		scopeID = "*"
	}

	id := ulid.Make().String()
	createdAt := time.Now().UTC()
	metadataJSON, err := encodeJSON(input.Metadata)
	if err != nil {
		return Event{}, fmt.Errorf("encode metadata: %w", err)
	}
	payloadJSON, err := encodeJSON(input.Payload)
	if err != nil {
		return Event{}, fmt.Errorf("encode payload: %w", err)
	}

	_, err = b.db.ExecContext(ctx, `
		INSERT INTO events (id, stream, scope_type, scope_id, subject, body, metadata, payload, created_at, read_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, id, input.Stream, scopeType, scopeID, nullString(input.Subject), input.Body, metadataJSON, payloadJSON, createdAt.Format(time.RFC3339Nano), "[]")
	if err != nil {
		return Event{}, fmt.Errorf("insert event: %w", err)
	}

	event := Event{
		ID:        id,
		Stream:    input.Stream,
		ScopeType: scopeType,
		ScopeID:   scopeID,
		Subject:   input.Subject,
		Body:      input.Body,
		Metadata:  input.Metadata,
		Payload:   input.Payload,
		CreatedAt: createdAt,
		Read:      false,
		ReadBy:    nil,
	}

	b.broadcast(event)
	return event, nil
}

func (b *Bus) List(ctx context.Context, stream string, opts ListOptions) ([]EventSummary, error) {
	if strings.TrimSpace(stream) == "" {
		return nil, fmt.Errorf("stream is required")
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	order := strings.ToLower(opts.Order)
	if order == "" {
		order = DefaultOrder(stream)
	}
	if order != "fifo" && order != "lifo" {
		order = "lifo"
	}
	orderBy := "created_at DESC"
	if order == "fifo" {
		orderBy = "created_at ASC"
	}

	where, args := buildScopeWhere(stream, opts)
	query := fmt.Sprintf(`SELECT id, stream, subject, created_at, read_by FROM events %s ORDER BY %s LIMIT ?`, where, orderBy)
	args = append(args, limit)

	rows, err := b.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()

	var out []EventSummary
	for rows.Next() {
		var id, streamName, createdAtStr string
		var subject sql.NullString
		var readByStr sql.NullString
		if err := rows.Scan(&id, &streamName, &subject, &createdAtStr, &readByStr); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		createdAt, _ := time.Parse(time.RFC3339Nano, createdAtStr)
		readBy := decodeReadBy(readByStr.String)
		read := readerInList(opts.Reader, readBy)
		out = append(out, EventSummary{
			ID:        id,
			Stream:    streamName,
			Subject:   subject.String,
			CreatedAt: createdAt,
			Read:      read,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events: %w", err)
	}
	return out, nil
}

func (b *Bus) Read(ctx context.Context, stream string, ids []string, reader string) ([]Event, error) {
	ids = filterEmpty(ids)
	if len(ids) == 0 {
		return nil, nil
	}
	if strings.TrimSpace(stream) == "" {
		return nil, fmt.Errorf("stream is required")
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = strings.TrimSuffix(placeholders, ",")
	args := []any{stream}
	for _, id := range ids {
		args = append(args, id)
	}

	query := fmt.Sprintf(`SELECT id, stream, scope_type, scope_id, subject, body, metadata, payload, created_at, read_by FROM events WHERE stream = ? AND id IN (%s)`, placeholders)
	rows, err := b.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("read events: %w", err)
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		var e Event
		var createdAtStr string
		var subject sql.NullString
		var metadataStr, payloadStr, readByStr sql.NullString
		if err := rows.Scan(&e.ID, &e.Stream, &e.ScopeType, &e.ScopeID, &subject, &e.Body, &metadataStr, &payloadStr, &createdAtStr, &readByStr); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		e.Subject = subject.String
		e.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAtStr)
		e.Metadata = decodeJSONMap(metadataStr.String)
		e.Payload = decodeJSONMap(payloadStr.String)
		e.ReadBy = decodeReadBy(readByStr.String)
		e.Read = readerInList(reader, e.ReadBy)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events: %w", err)
	}
	return out, nil
}

func (b *Bus) Ack(ctx context.Context, stream string, ids []string, reader string) error {
	if reader == "" {
		return fmt.Errorf("reader is required")
	}
	ids = filterEmpty(ids)
	if len(ids) == 0 {
		return nil
	}
	if strings.TrimSpace(stream) == "" {
		return fmt.Errorf("stream is required")
	}

	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin ack tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	for _, id := range ids {
		var readByStr string
		err := tx.QueryRowContext(ctx, `SELECT read_by FROM events WHERE stream = ? AND id = ?`, stream, id).Scan(&readByStr)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return fmt.Errorf("load read_by: %w", err)
		}
		readBy := decodeReadBy(readByStr)
		if readerInList(reader, readBy) {
			continue
		}
		readBy = append(readBy, reader)
		updated, err := json.Marshal(readBy)
		if err != nil {
			return fmt.Errorf("encode read_by: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE events SET read_by = ? WHERE stream = ? AND id = ?`, string(updated), stream, id); err != nil {
			return fmt.Errorf("update read_by: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit ack: %w", err)
	}
	return nil
}

func (b *Bus) Subscribe(ctx context.Context, streams []string) <-chan Event {
	ch := make(chan Event, 64)
	streamSet := map[string]struct{}{}
	for _, s := range streams {
		if s == "" {
			continue
		}
		streamSet[s] = struct{}{}
	}
	id := ulid.Make().String()

	sub := &subscriber{streams: streamSet, ch: ch}
	b.mu.Lock()
	b.subs[id] = sub
	b.mu.Unlock()

	go func() {
		<-ctx.Done()
		b.mu.Lock()
		delete(b.subs, id)
		b.mu.Unlock()
		close(ch)
	}()

	return ch
}

func (b *Bus) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}

func (b *Bus) broadcast(event Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, sub := range b.subs {
		if len(sub.streams) > 0 {
			if _, ok := sub.streams[event.Stream]; !ok {
				continue
			}
		}
		select {
		case sub.ch <- event:
		default:
			// Drop if subscriber is slow.
		}
	}
}

func buildScopeWhere(stream string, opts ListOptions) (string, []any) {
	args := []any{stream}
	where := "WHERE stream = ?"

	scopeType := opts.ScopeType
	scopeID := opts.ScopeID

	if scopeType != "" {
		where += " AND scope_type = ?"
		args = append(args, scopeType)
		if scopeID != "" {
			where += " AND scope_id = ?"
			args = append(args, scopeID)
		}
		return where, args
	}

	// Default: global scope, plus agent scope if reader provided.
	where += " AND ((scope_type = 'global' AND scope_id = '*')"
	if opts.Reader != "" {
		where += " OR (scope_type = 'agent' AND scope_id = ?)"
		args = append(args, opts.Reader)
	}
	where += ")"
	return where, args
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

func decodeReadBy(v string) []string {
	if v == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(v), &out); err != nil {
		return nil
	}
	return out
}

func readerInList(reader string, list []string) bool {
	if reader == "" {
		return false
	}
	for _, v := range list {
		if v == reader {
			return true
		}
	}
	return false
}

func filterEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func nullString(v string) any {
	if v == "" {
		return nil
	}
	return v
}
