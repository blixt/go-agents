# go-agents

Minimal, event-driven agent runtime. Agents are long-lived tasks that consume messages, call an LLM, and execute TypeScript code via Bun — all coordinated through a SQLite-backed event bus.

## Architecture

```
                        ┌──────────────────┐
                        │    Web UI        │
                        │  (Bun + React)   │
                        └───────┬──────────┘
                                │ /api/* proxy
                                ▼
┌───────────────────────────────────────────────────────────────────┐
│                          agentd (Go)                              │
│                                                                   │
│  ┌─────────────┐   ┌──────────────┐   ┌────────────────────────┐  │
│  │  HTTP API   │──▶│ Task Manager │──▶│       Event Bus        │  │
│  │             │   │              │   │                        │  │
│  │ /api/tasks  │   │ spawn, await │   │  task_input  signals   │  │
│  │ /api/state  │   │ complete,    │   │  task_output errors    │  │
│  │ /api/stream │   │ fail, cancel │   │  external    history   │  │
│  └─────────────┘   └──────┬───────┘   └───────────┬────────────┘  │
│                           │                       │               │
│                    ┌──────┴───────┐               │               │
│                    │   Runtime    │◀──────────────┘               │
│                    │              │                               │
│                    │ per-agent    │   ┌─────────┐                 │
│                    │ message loop │──▶│   LLM   │                 │
│                    │              │   │ provider│                 │
│                    └──────────────┘   └─────────┘                 │
│                                                                   │
│                        SQLite (tasks, events, history)            │
└───────────────────────────────────────────────────────────────────┘
        ▲                                           ▲
        │ poll /api/tasks/queue?type=exec           │ managed by supervisor
        │                                           │
┌───────┴──────────┐                    ┌───────────┴──────────────┐
│  execd (Bun)     │                    │  Service Supervisor (Bun)│
│                  │                    │                          │
│  claims exec     │                    │  watches services/*/     │
│  tasks, runs     │                    │  auto-restart on crash   │
│  TypeScript in   │                    │  reload on file change   │
│  temp sandboxes  │                    │  backoff + drain safety  │
└──────────────────┘                    └──────────────────────────┘
```

### Core concepts

**Tasks** are the universal work unit. Every piece of async work — an LLM call, a code execution, a user request — is a durable task row in SQLite with a status lifecycle: `queued → running → completed | failed | cancelled`.

**Agents** are a special case of tasks (`type=agent`) that run a persistent loop. Each agent subscribes to scoped event streams, wakes on incoming messages or child-task completions, calls an LLM with accumulated context, and executes tools. Agents are single-threaded: one message processed at a time.

**Event Bus** is the coordination backbone. Events are pushed to named streams (`task_input`, `task_output`, `signals`, `errors`, `external`, `history`) with scope (`task/{id}` or `global/*`) and priority (`interrupt > wake > normal > low`). Priority determines agent behavior: *interrupt* cancels the current LLM turn, *wake* unblocks an awaiting task early, *normal/low* queue for the next turn.

**Exec** is the primary tool. When an LLM decides to run code, it spawns an `exec` task. The external `execd` worker (Bun) polls for these, runs the TypeScript in a temp sandbox with symlinked `core/` and `tools/` libraries, and posts the result back. The agent awaits the task to get the output.

**Services** are long-running background processes (e.g., a Telegram bot, a webhook listener). The service supervisor watches `~/.go-agents/services/*/run.ts`, auto-starts them, restarts on crash with exponential backoff, and reloads on file or `.env` changes. Services communicate with agents by posting to the API.

### How a message flows

1. A message arrives (API call, web UI, or service)
2. The API pushes it onto `task_input` scoped to the target agent
3. The agent loop wakes, builds a system prompt (via `PROMPT.ts` in Bun), and calls the LLM
4. The LLM may call tools (`exec`, `await_task`, `send_task`, `kill_task`, `noop`, `view_image`)
5. Tool results flow back as task completions on `task_output`
6. The LLM produces a final response, which is routed back to the message source
7. Everything is recorded to `history` for observability

### Key directories

```
cmd/agentd/          Go entry point — HTTP server, wiring
internal/
  api/               HTTP handlers (tasks, state, SSE streaming)
  engine/            Agent runtime loop, LLM orchestration, context assembly
  tasks/             Task manager — spawn, await, complete, cancel
  eventbus/          SQLite-backed event bus with in-memory fanout
  agenttools/        Tool implementations (exec, await_task, send_task, etc.)
  ai/                Multi-provider LLM client (Anthropic, OpenAI, Google)
  prompt/            Dynamic prompt builder (runs PROMPT.ts via Bun)
  state/             SQLite schema and migrations
  config/            Configuration loading (env + config.json)
exec/
  execd.ts           External Bun worker — polls and runs exec tasks
  bootstrap.ts       Per-task entry point for sandboxed execution
  service-supervisor.ts  Manages long-running services
web/
  server.tsx          Bun HTTP server for the React UI
  src/                React app (state polling, SSE, agent controls)
template/
  PROMPT.ts           System prompt builder (copied to ~/.go-agents)
  core/               Agent creation helpers, API client
  tools/              User-extensible tool libraries
```

## Getting started

1. Install tools: `mise install`
2. Run the server: `mise start` (or `mise dev` for auto-reload)
3. Open `http://localhost:8080`

### Enable LLM

Set your provider API key in `.env` (or your shell):
- `GO_AGENTS_ANTHROPIC_API_KEY`
- `GO_AGENTS_OPENAI_API_KEY`
- `GO_AGENTS_GOOGLE_API_KEY`

Provider and model are configured in `config.json` (or `data/config.json`):
```json
{
  "http_addr": ":8080",
  "data_dir": "data",
  "db_path": "data/go-agents.db",
  "llm_debug_dir": "data/llm-debug",
  "llm_provider": "anthropic",
  "llm_model": "claude-sonnet-4-5",
  "restart_token": ""
}
```

### Tests / Format

- `mise test`
- `mise format`

If `go test ./...` fails with a `version "go1.x.y" does not match go tool version` error, clear stale `GOROOT` first:
- `unset GOROOT` (or run `GOROOT=$(go env GOROOT) go test ./...`)
