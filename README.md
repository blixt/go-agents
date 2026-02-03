# go-agents

Minimal agent runtime with an exec tool (Bun/TypeScript), event bus, async tasks, and a web control panel.

**Getting Started**
1. Install tools: `mise install`
2. Run the server: `mise start` (or `mise dev` for auto-reload)
3. Open `http://localhost:8080`

**Enable LLM (optional)**
Set in `.env` (or your shell):
- `GO_AGENTS_LLM_PROVIDER` (default `openai-responses`)
- `GO_AGENTS_LLM_MODEL`
- `GO_AGENTS_LLM_API_KEY`

**Tests / Format**
- `mise test`
- `mise format`
