package testutil

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/flitsinc/go-agents/internal/state"
)

func OpenTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	db, err := state.Open(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	return db, func() {
		_ = db.Close()
	}
}
