package tasks

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/flitsinc/go-agents/internal/eventbus"
	"github.com/flitsinc/go-agents/internal/testutil"
)

func TestManagerRecordUpdateWithWriteContention(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := NewManager(db, bus)
	ctx := context.Background()

	task, err := mgr.Spawn(ctx, Spec{Type: "exec"})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	_, err = tx.Exec(`
		UPDATE tasks SET updated_at = ? WHERE id = ?
	`, time.Now().UTC().Format(time.RFC3339Nano), task.ID)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("lock task row: %v", err)
	}

	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = tx.Commit()
	}()

	err = mgr.RecordUpdate(ctx, task.ID, "progress", map[string]any{
		"pct":   50,
		"nonce": ulid.Make().String(),
	})
	if err != nil {
		t.Fatalf("record update: %v", err)
	}
}

func TestClaimQueuedIsAtomicUnderContention(t *testing.T) {
	db, closeFn := testutil.OpenTestDB(t)
	defer closeFn()

	bus := eventbus.NewBus(db)
	mgr := NewManager(db, bus)
	ctx := context.Background()

	task, err := mgr.Spawn(ctx, Spec{Type: "exec"})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)

	results := make(chan []Task, 2)
	errs := make(chan error, 2)

	claim := func() {
		defer wg.Done()
		<-start
		items, err := mgr.ClaimQueued(ctx, "exec", 1)
		if err != nil {
			errs <- err
			return
		}
		results <- items
	}

	go claim()
	go claim()
	close(start)
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		t.Fatalf("claim queued: %v", err)
	}

	claimedIDs := map[string]struct{}{}
	for batch := range results {
		for _, item := range batch {
			claimedIDs[item.ID] = struct{}{}
		}
	}
	if len(claimedIDs) != 1 {
		t.Fatalf("expected one unique claim, got %d", len(claimedIDs))
	}
	if _, ok := claimedIDs[task.ID]; !ok {
		t.Fatalf("expected claimed task id %s", task.ID)
	}
}
