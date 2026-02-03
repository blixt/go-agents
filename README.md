# go-agents

Minimal agent runtime with an exec tool (Bun/TypeScript), event bus, async tasks, and a web control panel.

**Getting Started**
1. Install tools: `mise install`
2. Run the server: `mise start` (or `mise dev` for auto-reload)
3. Open `http://localhost:8080`

**Enable LLM (optional)**
Set your provider API key in `.env` (or your shell):
- `GO_AGENTS_ANTHROPIC_API_KEY`
- `GO_AGENTS_OPENAI_API_KEY`
- `GO_AGENTS_GOOGLE_API_KEY`

Provider/model are configured in `config.json` (see below).

**Config (non‑env)**
Create `config.json` (or `data/config.json`) to adjust runtime settings:
```json
{
  "http_addr": ":8080",
  "data_dir": "data",
  "db_path": "data/go-agents.db",
  "snapshot_dir": "data/exec-snapshots",
  "web_dir": "web",
  "llm_provider": "anthropic",
  "llm_model": "claude-sonnet-4-5",
  "restart_token": ""
}
```

**Tests / Format**
- `mise test`
- `mise format`
