# Diary

## 2026-02-03
Learnings so far
- Docker dev now runs `agentd` and `execd` with Go 1.25.6 (`golang:1.25.6-bookworm`). `agentd` listens on 8080.
- `execd` could not complete tasks because `Bun.mkdir` is undefined; switched to `fs/promises` `mkdir` and new exec tasks now complete.
- The task queue/exec flow works end-to-end with the exec worker once that mkdir issue is fixed.
- The system currently depends on an LLM API key; without it the agent can’t actually reason or call tools.
- Provider API keys must be set via provider-specific env vars (no generic key).

Ideas from the user request
- Make the agent self-diagnose stuck or resource-heavy tasks via eventbus signals instead of hard-coded heuristics.
- Keep heuristics minimal; prefer agent choices with explicit signals and tight context budgets.
- Allow long-running agents; design for weeks-long uptime with periodic compaction and self-observation.

Plan / experiments to try (in order)
1. Read the linked blog post for concrete ideas to incorporate.
2. Add a “diagnostic prompt” path and a structured “tool inventory” response to confirm the agent knows tools on first attempt.
3. Introduce a lightweight “task health” signal emitted by the runtime (age, last output, CPU/mem if available).
4. Add a “stuck task” alert stream that the agent can listen to and decide to interrupt or keep waiting.
5. Verify the agent can run builtin tools in a fresh session (restart agent, clear DB) and succeeds on first attempt.
6. Iterate prompt and code based on failures; record each change and result.

Experiment log
- Hypothesis: Without an API key configured, agent runs will fail fast and expose a clear error in the session.
  Test: POST `/api/agents/operator/run` with `{"message":"hello"}` and then GET `/api/sessions/operator`.
  Result: Session `last_output` and `last_error` report “LLM not configured...” and no agent reply was produced.
  Takeaway: We need a valid provider-specific API key in `.env` before tool-usage experiments.

Change log
- Removed the generic `GO_AGENTS_LLM_API_KEY` path; now only provider-specific keys are supported.

Notes from the Pi/OpenClaw blog post
- Pi has a tiny core prompt and only four tools (Read, Write, Edit, Bash), with most capability pushed into extensions.
- Extensions can persist state into sessions; Pi includes hot reload so the agent can write, reload, and test extensions iteratively.
- Sessions are trees: you can branch to do side quests (eg, fix tools) and then summarize back.
- Pi intentionally omits MCP; the philosophy is to have the agent extend itself by writing code rather than downloading skills.
- Pi keeps provider portability by not leaning too hard on provider-specific features; it stores custom session messages for state.
- Extensions can render richer TUI components (spinners, tables, pickers) inside the terminal.
- Extensions/skills can be created quickly and discarded if they turn out not to be needed.
- The agent is expected to build and maintain its own functionality (skills and extensions) rather than rely on community catalogs.
