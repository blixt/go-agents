package state

const schemaSQL = `
CREATE TABLE IF NOT EXISTS agents (
  id TEXT PRIMARY KEY,
  profile TEXT,
  status TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS tasks (
  id TEXT PRIMARY KEY,
  type TEXT NOT NULL,
  status TEXT NOT NULL,
  owner TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  metadata TEXT,
  payload TEXT,
  result TEXT,
  error TEXT
);

CREATE TABLE IF NOT EXISTS task_updates (
  id TEXT PRIMARY KEY,
  task_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  payload TEXT,
  created_at TEXT NOT NULL,
  FOREIGN KEY(task_id) REFERENCES tasks(id)
);

CREATE INDEX IF NOT EXISTS idx_task_updates_task_id ON task_updates(task_id);

CREATE TABLE IF NOT EXISTS events (
  id TEXT PRIMARY KEY,
  stream TEXT NOT NULL,
  scope_type TEXT NOT NULL,
  scope_id TEXT NOT NULL,
  subject TEXT,
  body TEXT NOT NULL,
  metadata TEXT,
  payload TEXT,
  created_at TEXT NOT NULL,
  read_by TEXT
);

CREATE INDEX IF NOT EXISTS idx_events_stream_scope_created ON events(stream, scope_type, scope_id, created_at);

CREATE TABLE IF NOT EXISTS actions (
  id TEXT PRIMARY KEY,
  agent_id TEXT,
  content TEXT NOT NULL,
  status TEXT,
  metadata TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
`
