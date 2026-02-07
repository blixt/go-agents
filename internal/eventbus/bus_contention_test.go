package eventbus

import (
	"context"
	"testing"
	"time"

	"github.com/flitsinc/go-agents/internal/idgen"
	"github.com/flitsinc/go-agents/internal/testutil"
)

func TestBusPushWithWriteContention(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := NewBus(db)
	ctx := context.Background()

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	createdAt := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = tx.Exec(`
		INSERT INTO events (id, stream, scope_type, scope_id, subject, body, metadata, payload, created_at, read_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, idgen.New(), "signals", "global", "*", "hold", "hold", "{}", "{}", createdAt, "[]")
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("seed event: %v", err)
	}

	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = tx.Commit()
	}()

	start := time.Now()
	_, err = bus.Push(ctx, EventInput{
		Stream:  "signals",
		Subject: "contention",
		Body:    "contention test",
	})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	_ = start
}
