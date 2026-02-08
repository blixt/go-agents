package idgen_test

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/flitsinc/go-agents/internal/idgen"
	_ "modernc.org/sqlite"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`CREATE TABLE tasks (
		id TEXT PRIMARY KEY,
		type TEXT,
		status TEXT,
		owner TEXT,
		created_at TEXT,
		updated_at TEXT,
		metadata TEXT,
		payload TEXT
	)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	return db
}

func TestTaskID_FirstTask(t *testing.T) {
	db := openTestDB(t)
	got := idgen.TaskID(db, "agent")
	if got != "agent-1" {
		t.Fatalf("expected agent-1, got %s", got)
	}
}

func TestTaskID_Increments(t *testing.T) {
	db := openTestDB(t)
	_, err := db.Exec(`INSERT INTO tasks (id, type, status, created_at, updated_at) VALUES ('agent-1', 'agent', 'queued', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	got := idgen.TaskID(db, "agent")
	if got != "agent-2" {
		t.Fatalf("expected agent-2, got %s", got)
	}
}

func TestTaskID_MultiplePrefixes(t *testing.T) {
	db := openTestDB(t)
	_, err := db.Exec(`INSERT INTO tasks (id, type, status, created_at, updated_at) VALUES ('agent-1', 'agent', 'queued', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert agent: %v", err)
	}

	got := idgen.TaskID(db, "exec")
	if got != "exec-1" {
		t.Fatalf("expected exec-1, got %s", got)
	}

	got = idgen.TaskID(db, "agent")
	if got != "agent-2" {
		t.Fatalf("expected agent-2, got %s", got)
	}
}

func TestTaskID_CustomName(t *testing.T) {
	db := openTestDB(t)

	got := idgen.TaskID(db, "planner")
	if got != "planner-1" {
		t.Fatalf("expected planner-1, got %s", got)
	}

	_, err := db.Exec(`INSERT INTO tasks (id, type, status, created_at, updated_at) VALUES ('planner-1', 'agent', 'queued', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	got = idgen.TaskID(db, "planner")
	if got != "planner-2" {
		t.Fatalf("expected planner-2, got %s", got)
	}
}

func TestValidateCustomID(t *testing.T) {
	valid := []string{
		"a",
		"fetch-weather",
		"setup-telegram",
		"my-task-123",
		"a1",
		"abc",
		"a-b-c",
	}
	for _, id := range valid {
		if err := idgen.ValidateCustomID(id); err != nil {
			t.Errorf("expected %q to be valid, got error: %v", id, err)
		}
	}

	invalid := []string{
		"",
		"-start-dash",
		"end-dash-",
		"1starts-with-digit",
		"UPPERCASE",
		"has spaces",
		"has_underscore",
		"has.dot",
		strings.Repeat("a", 65),
	}
	for _, id := range invalid {
		if err := idgen.ValidateCustomID(id); err == nil {
			t.Errorf("expected %q to be invalid, got nil error", id)
		}
	}
}
