# Diary

## 2026-02-03
Learnings so far
- Docker dev runs `agentd` and `execd` with Go 1.25.6 (`golang:1.25.6-bookworm`). `agentd` listens on 8080.
- `execd` needed `fs/promises` `mkdir`; once fixed, exec tasks complete end to end.
- Provider API keys must be set via provider-specific env vars (no generic key).
- `exec` tasks in Docker cannot reach `localhost:8080`; they must use a docker-aware API base (agentd service).
- Task health signals + wake messages are essential for detecting and reacting to stuck tasks.

Ideas from the user request
- Make the agent self-diagnose stuck or resource-heavy tasks via eventbus signals instead of hard-coded heuristics.
- Keep heuristics minimal; prefer agent choices with explicit signals and tight context budgets.
- Allow long-running agents; design for weeks-long uptime with periodic compaction and self-observation.

Notes from the Pi/OpenClaw blog post
- Pi has a tiny core prompt and only four tools (Read, Write, Edit, Bash), with most capability pushed into extensions.
- Extensions can persist state into sessions; Pi includes hot reload so the agent can write, reload, and test extensions iteratively.
- Sessions are trees: you can branch to do side quests and then summarize back.
- Pi intentionally omits MCP; the philosophy is to have the agent extend itself by writing code rather than downloading skills.
- Pi keeps provider portability by not leaning too hard on provider-specific features; it stores custom session messages for state.
- Extensions can render richer TUI components (spinners, tables, pickers) inside the terminal.
- Extensions/skills can be created quickly and discarded if they turn out not to be needed.
- The agent is expected to build and maintain its own functionality (skills and extensions) rather than rely on community catalogs.

Future experiment ideas
- E024: Validate wake + cancel for LLM tasks (interrupt handling path).
- E025: Test parallel async parent/child tasks where parent cancel kills child.
- E026: Test subagent bidirectional messaging without human involvement (baseline agent-to-agent symmetry).

Experiment log
- E001: No API key configured.
  Hypothesis: LLM calls fail fast with a clear error.
  Test: POST `/api/agents/operator/run` and then GET `/api/sessions/operator`.
  Result: Session reported “LLM not configured...” with no reply.
  Takeaway: API key must be set first.

- E002: Exec tool use + “how to add tools” guidance.
  Hypothesis: With provider key set, agent can use exec and explain tool wiring.
  Test: Ask operator to exec 2+2 and explain how to add a tool.
  Result: Exec succeeded; tool explanation was generic and wrong.
  Change: Added explicit “Adding tools” instructions tied to repo paths.
  Validation: Re-ran test on clean DB; explanation was correct.

- E003: Subagent orchestration baseline.
  Hypothesis: Operator will spawn subagent and return final answer to human.
  Test: Ask operator to spawn subagent to compute 6*7 and reply only number.
  Result: Operator got stuck with subagent; no final answer to human.
  Takeaway: Prompt must describe human routing explicitly.

- E004: Human routing prompt update.
  Change: Added “human is agent_id=human” guidance.
  Result: Operator told subagent to reply directly to human; human got answer from subagent.
  Takeaway: Prompt must forbid subagent -> human messages.

- E005: Forbid subagent -> human, but runtime still spawned human agent.
  Change: Updated prompt to forbid subagent->human.
  Result: Subagent still messaged human directly; runtime created a human agent loop.
  Takeaway: Runtime must not `ensureAgent` for human.

- E006: Disable `ensureAgent` for human.
  Change: `send_message` to human no longer spawns a loop.
  Result: Subagent still messaged human directly; operator received nothing.
  Takeaway: Need rule that replies to agents must be plain text.

- E007: Plain-text replies and operator routing.
  Change: Enforced operator-only messaging to human; route operator plain text (from subagents) to human.
  Result: Operator finally returned “42” to human, but sent a status update first.
  Takeaway: Need to discourage interim status updates.

- E008: No interim status updates to human.
  Change: Prompt forbids interim updates unless requested.
  Result: Subagent LLM hung; operator stuck waiting.
  Takeaway: Need task health monitoring + interrupts.

- E009: Manual cancel path.
  Test: Cancel stuck task via API.
  Result: Task cancelled, but operator got no notification.
  Takeaway: Need task health signals to inform operator.

- E010: Task health snapshots.
  Change: Periodic `task_health` signals with age/updated time.
  Result: Operator saw snapshots but still sent interim updates.

- E011: Wake messages for stale tasks.
  Change: Emit wake messages on stale tasks (cooldown).
  Result: Wake emitted, but no agent loop to handle it.

- E012: Default operator loop.
  Change: Start operator loop on runtime start.
  Result: Wake triggered operator LLM task without human input.

- E013: Wake subject includes task ids.
  Change: Subject now includes stale task IDs.
  Result: Operator asked for human direction.
  Takeaway: Prompt must teach API inspection/cancel.

- E014: Task API hints in prompt (curl).
  Change: Added curl commands to prompt.
  Result: Exec failed; `code/shell.ts` not found in exec sandbox.
  Takeaway: Provide code/ modules to exec sandbox.

- E015: Symlink code/ into exec sandbox.
  Change: `execd` symlinks repo `code/` into exec temp dir.
  Result: Operator exec task still blocked by single exec worker.

- E016: Execd parallelism.
  Change: Added `--parallel`/`EXEC_PARALLEL` and set `--parallel 2` in compose.
  Result: Still blocked because loop waited for long task to finish.

- E017: LLM history corruption.
  Issue: Anthropic `invalid_request_error` (messages.*.content.*.text.text missing).
  Change: New LLM session per run (no per-agent cached LLM).

- E018: Execd concurrency loop fix.
  Change: Refactored execd loop to keep polling while tasks in flight.
  Result: Recovery exec tasks ran concurrently, but only inspected tasks.

- E019: Default cancel behavior.
  Change: Prompt says wake IDs are stale; cancel exec tasks by default.

- E020: Exec tasks cannot reach localhost from Docker.
  Test: exec fetch to http://localhost:8080/api/health.
  Result: “Unable to connect” from Bun fetch.
  Takeaway: Need Docker-aware API helper.

- E021: Add code/api.ts + prompt updates to use apiJSON/apiPostJSON.
  Change: Added API helper to resolve base URL (prefers config, then agentd in Docker).
  Validation: exec task using `apiJSON('/api/health')` succeeded and cached api_base.

- E022: Wake + cancel with API helper.
  Test: Spawn long exec task and wait for wake.
  Result: Operator used apiJSON/apiPostJSON via exec and cancelled stale task successfully.

- E023: Prompt string literal fix.
  Issue: Using JS template literal backticks inside Go raw string broke build.
  Fix: Switched to single-quoted examples in prompt.
