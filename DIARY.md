# Diary

## 2026-02-03
Learnings so far
- Docker dev runs `agentd` and `execd` with Go 1.25.6 (`golang:1.25.6-bookworm`). `agentd` listens on 8080.
- `execd` could not complete tasks because `Bun.mkdir` is undefined; switched to `fs/promises` `mkdir` and new exec tasks now complete.
- The task queue/exec flow works end-to-end once `execd` mkdir is fixed.
- The system depends on a provider API key; without it the agent cannot reason or call tools.
- Provider API keys must be set via provider-specific env vars (no generic key).
- `exec` tasks in Docker cannot reach `localhost:8080`; they must use a docker-aware API base (agentd service) via helper.
- Task health signals + wake messages are essential for detecting and reacting to stuck tasks.

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
- E027: Validate wake + cancel for LLM tasks (interrupt handling path).
- E028: Test parallel async parent/child tasks where parent cancel kills child.
- E029: Test subagent bidirectional messaging without human involvement (baseline agent-to-agent symmetry).

Experiment log
- E001: No API key configured.
  Hypothesis: LLM calls fail fast with a clear error.
  Test: POST `/api/agents/operator/run` with `{"message":"hello"}` and then GET `/api/sessions/operator`.
  Result: Session `last_output` and `last_error` reported “LLM not configured...” and no agent reply was produced.
  Takeaway: We need a valid provider-specific API key in `.env` before tool-usage experiments.

- E002: Exec tool use + “how to add tools” guidance.
  Hypothesis: With a provider key set, the agent will (a) use `exec` when instructed and (b) correctly explain how to add a tool.
  Test: Cleared DB, restarted containers, sent “Use exec to compute 2+2... Then explain how to add a new tool.”
  Result: Agent used `exec` (task completed with result=4). Explanation was generic and not aligned with our actual tool wiring (Go tool registration + `cmd/agentd/main.go` registration).
  Takeaway: The prompt needs a concrete “How to add tools” section tied to our codebase.
  Follow-up change: Added explicit “Adding tools” prompt guidance.
  Follow-up test: Repeated the same test on a clean DB.
  Follow-up result: Agent used `exec` and described the correct tool wiring (Go tool in `internal/agenttools`, register in `cmd/agentd/main.go`, restart).
  Follow-up takeaway: Prompt update resolved the first-attempt tool explanation failure.

- E003: Subagent orchestration baseline.
  Hypothesis: The agent can orchestrate a subagent and return the final result to the human on first attempt.
  Test: Asked operator to spawn a subagent to compute 6*7 and then reply to human with the number only.
  Result: Operator spawned the subagent, but got stuck in a back-and-forth and never sent the final answer to the human. It replied “Waiting for the reply...” to the human and continued dialog with the subagent.
  Takeaway: Prompt must explicitly describe human routing (`agent_id="human"`).

- E004: Human routing prompt update.
  Change: Added guidance about `agent_id="human"`.
  Result: Operator sent a status message to human but instructed the subagent to reply directly to the human with the answer. Human received “42” from subagent, not operator.
  Takeaway: Prompt must explicitly tell subagents to reply to the parent agent, not the human.

- E005: Forbid subagent → human (prompt only).
  Change: Updated prompt to forbid subagents messaging the human.
  Result: Subagent still messaged the human directly, and the system spawned a "human" agent that replied (undesired). This created extra chatter and an error response.
  Takeaway: Runtime should never spawn an agent loop for `human`; `send_message` to `human` must not call `ensureAgent`.

- E006: Disable `ensureAgent` for human.
  Change: `send_message` to human no longer spawns a loop.
  Result: Subagent still sent `42` directly to the human; operator received no reply because the subagent did not produce plain text (only a tool call).
  Takeaway: Prompt must tell agents that replies to other agents should be plain text and `send_message` should only be used to start conversations/spawn subagents.

- E007: Plain-text replies and operator routing.
  Change: Enforced operator-only messaging to human; route operator plain-text replies to the human when the source is a subagent.
  Result: Operator returned the final answer “42” to the human, but still sent an interim “waiting” update first.
  Takeaway: Core delegation works now; we need to discourage interim status updates if we want single final replies.

- E008: No interim status updates to human.
  Change: Prompt forbids interim status updates unless explicitly requested.
  Result: Operator sent only a status update and the subagent LLM task hung (no reply to operator).
  Takeaway: LLM tasks can get stuck; we need a health signal or watchdog + clean interrupt path that informs the operator.

- E009: Manual cancel path.
  Test: Cancelled the stuck LLM task via `POST /api/tasks/{id}/cancel`.
  Result: Task moved to `cancelled`, but the operator received no notification.
  Takeaway: We need task health/interrupt signals delivered to the operator so it can react without manual API calls.

- E010: Task health snapshots.
  Change: Added periodic task_health snapshots on the signals stream for each task owner/target, including age and last-update age.
  Result: Operator saw snapshots but still sent interim updates.

- E011: Wake messages for stale tasks.
  Change: Added a wake message on the messages stream when task_health detects stale tasks (with cooldown).
  Test: Created a long-running exec task and waited for the next task_health tick.
  Result: task_health signals showed the stale exec task, and a wake message (subject “wake: task_health”) was emitted to operator.
  Takeaway: Wake messages work, but an agent loop must be running to react.

- E012: Default operator loop.
  Change: Start the operator loop by default at runtime startup so wake messages are handled even without explicit user messages.
  Result: A wake message triggered an operator LLM task without any human input.

- E013: Wake subject includes task IDs.
  Change: Wake message subject now includes stale task IDs.
  Result: Operator acknowledged the wake but did not inspect or cancel; it asked for human direction.
  Takeaway: The agent needs explicit guidance on how to inspect/cancel tasks via the API when woken.

- E014: Task API hints in prompt (curl).
  Change: Added Task API usage hints (inspect/cancel) to the prompt using curl.
  Result: Exec failed because `code/shell.ts` could not be resolved in the exec sandbox.
  Takeaway: Exec needs access to code/ module paths or the prompt should not recommend importing `code/shell.ts`.

- E015: Symlink code/ into exec sandbox.
  Change: Symlinked repo `code/` into exec temp dir `node_modules`.
  Result: Operator queued an exec task to inspect/cancel, but it was blocked behind the long-running exec (single-worker).
  Takeaway: execd needs parallelism so recovery exec tasks can run while a long exec is in flight.

- E016: Execd parallelism.
  Change: Added execd parallelism (default 2) and exposed via `--parallel`/`EXEC_PARALLEL`. Updated compose to run execd with `--parallel 2`.
  Result: Still blocked because the main loop waited for long tasks to finish before claiming new ones.

- E017: LLM history corruption.
  Issue: Repeated wake-triggered LLM calls started failing with Anthropic `invalid_request_error` (messages.*.content.*.text.text missing), likely due to reused LLM history across runs with tool calls.
  Change: Switched to fresh LLM sessions per run (no per-agent LLM caching) to avoid corrupted history.

- E018: Execd concurrency loop fix.
  Issue: execd parallel=2 still blocked because the loop waited for long tasks to finish.
  Change: Refactored execd loop to maintain a running set and keep polling/claiming while tasks are in flight (concurrency pool).

- E019: Default cancel behavior.
  Observation: Operator exec tasks ran concurrently, but only inspected stale tasks and did not cancel.
  Change: Prompt now states that wake ids are already stale; exec tasks should be cancelled by default unless there’s a reason not to.

- E020: Exec tasks cannot reach localhost from Docker.
  Hypothesis: exec tasks can reach agentd at http://localhost:8080.
  Test: Spawned exec task that fetches /api/health via Bun fetch.
  Result: Exec failed with “Unable to connect.”
  Takeaway: Exec tasks cannot use localhost to reach agentd in Docker; need docker-aware API base URL helper.

- E021: Add code/api.ts helper.
  Change: Added `code/api.ts` helper to auto-discover API base (prefers config.json, then agentd service when in Docker) and updated prompt to use apiJSON/apiPostJSON instead of curl.
  Validation: Exec task using `apiJSON('/api/health')` succeeded; state cached `api_base = http://agentd:8080`.

- E022: Wake + cancel with API helper.
  Hypothesis: With code/api.ts and updated prompt, operator will use apiJSON/apiPostJSON and cancel stale exec tasks.
  Test: Spawned a long-running exec task and waited for task_health wake.
  Result: Operator used apiJSON/apiPostJSON via exec and successfully cancelled the stale exec task. Task status became cancelled with reason “stale task detected by task_health.”

- E023: Prompt string literal fix.
  Issue: Using JS template literal backticks inside Go raw-string prompt broke agentd build.
  Fix: Switched to single-quoted examples in prompt.

## 2026-02-04
Experiment log
- E024: Execd queue failures after restart (DB corruption).
  Hypothesis: Restart should not break exec queue; if it does, agentd should still return queue results.
  Test: Restarted compose and attempted to claim exec queue from execd.
  Result: execd saw 400s; manual curl from execd to /api/tasks/queue?type=exec returned error: database disk image is malformed (11).
  Takeaway: DB corruption blocks exec workers; recovery requires recreating the DB.
  Fix: Stopped compose, removed data/go-agents.db (+-wal/-shm), and restarted to recreate a clean DB.
  Validation: execd queue requests returned null (OK).

- E025: Wake + cancel after DB reset.
  Hypothesis: After clean restart, the default operator loop will still cancel stale exec tasks via apiJSON/apiPostJSON.
  Test: Spawned a long exec task (sleep 5m), waited for task_health wake.
  Result: Task was cancelled with reason "stale task detected by task_health" after ~1 minute.
  Takeaway: Wake + cancel pipeline still works after DB reset.
