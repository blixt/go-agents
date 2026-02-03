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
- Hypothesis: With a provider key set, the agent will (a) use `exec` when instructed and (b) correctly explain how to add a tool.
  Test: Cleared DB, restarted containers, sent: “Use exec to compute 2+2... Then explain how to add a new tool.”
  Result: Agent used `exec` (task completed with result=4). Explanation was generic and not aligned with our actual tool wiring (Go tool registration + `cmd/agentd/main.go` registration).
  Takeaway: The prompt needs a concrete “How to add tools” section tied to our codebase.
  Follow-up test: After adding explicit “Adding tools” prompt guidance, repeated the same test on a clean DB.
  Follow-up result: Agent used `exec` and described the correct tool wiring (Go tool in `internal/agenttools`, register in `cmd/agentd/main.go`, restart).
  Follow-up takeaway: Prompt update resolved the first-attempt tool explanation failure.
- Hypothesis: The agent can orchestrate a subagent and return the final result to the human on first attempt.
  Test: Asked operator to spawn a subagent to compute 6*7 and then reply to human with the number only.
  Result: Operator spawned the subagent, but then got stuck in a back-and-forth with the subagent and never sent the final answer to the human. It replied “Waiting for the reply...” to the human and then continued dialog with the subagent.
  Takeaway: The prompt must explicitly tell the agent that the human user is `agent_id="human"` and that it should call `send_message` to human after subagent coordination.
  Follow-up test: After adding guidance about `agent_id="human"`, repeated the subagent test on a clean DB.
  Follow-up result: Operator sent a status message to human, but instructed the subagent to reply directly to the human with the answer. Human received “42” from subagent, not operator.
  Follow-up takeaway: Prompt must explicitly instruct agents to have subagents reply to the parent agent, not to the human.
  Second follow-up test: After updating prompt to forbid subagents messaging the human, repeated the test.
  Second follow-up result: Subagent still messaged the human directly, and the system spawned a "human" agent that replied to the subagent (undesired). This created extra chatter and an error response.
  Second follow-up takeaway: The runtime should never spawn an agent loop for `human`; `send_message` to `human` must not call `ensureAgent`.
  Third follow-up test: After disabling `ensureAgent` for `human`, repeated the subagent test.
  Third follow-up result: Subagent still sent `42` directly to the human; operator received no reply because the subagent did not produce plain text (only a tool call).
  Third follow-up takeaway: Prompt must tell agents that replies to other agents should be plain text (the runtime auto-routes), and send_message should only be used to start conversations/spawn subagents.
  Fourth follow-up change: Enforce in `send_message` that only the operator can target `human`. Subagents attempting to message human will error and must reply in plain text to the operator.
  Fifth follow-up change: Route operator plain-text replies to the human when the source is a subagent, to avoid accidental auto-replies back to subagents. Added prompt note explaining this operator behavior.
  Sixth follow-up test: After routing operator replies to human, repeated the subagent test.
  Sixth follow-up result: Operator returned the final answer "42" to the human. It still sent an interim "waiting" update first.
  Sixth follow-up takeaway: Core delegation works now; we may still need to further discourage interim status messages if we want a single final reply.
  Seventh follow-up change: Strengthened prompt to forbid interim status updates to human unless explicitly requested.
  Seventh follow-up test: With the strengthened prompt, the operator sent only a status update and the subagent LLM task hung (no reply to operator).
  Seventh follow-up takeaway: LLM tasks can get stuck; we need a health signal or watchdog and a clean interrupt path that informs the operator.
  Manual experiment: Cancelled the stuck LLM task via `POST /api/tasks/{id}/cancel`.
  Manual result: Task moved to `cancelled`, but the operator received no notification.
  Manual takeaway: We need task health/interrupt signals delivered to the operator so it can react without manual API calls.
  Eighth follow-up change: Added periodic task_health snapshots on the signals stream for each task owner/target, including age and last-update age, so agents can decide when to interrupt or investigate.
  Ninth follow-up change: Ensure operator receives a full task_health snapshot that includes all running tasks (including subagents), not just operator-owned tasks.
  Ninth follow-up test: After this change, task_health for operator now lists both operator and subagent running tasks.
  Ninth follow-up result: Operator still sends an interim status update before the final answer despite prompt instruction.
  Tenth follow-up change: Added a wake message on the messages stream when task_health detects stale tasks (with cooldown). This wakes the agent without hard-coded cancellation.
  Tenth follow-up test: Created a long-running exec task and waited for the next task_health tick.
  Tenth follow-up result: task_health signals showed the stale exec task, and a wake message (subject "wake: task_health") was emitted to operator.
  Tenth follow-up takeaway: Wake messages work, but an agent loop must be running to react; we may want a default always-on agent loop.
  Eleventh follow-up change: Start the operator loop by default at runtime startup so wake messages are handled even without explicit user messages.
  Eleventh follow-up test: Created a long-running exec task and waited for a task_health wake.
  Eleventh follow-up result: A wake message triggered an operator LLM task even without any human message, confirming the default loop is active.
  Twelfth follow-up change: Wake message subject now includes the stale task ids so the agent can act without needing event payloads.
  Twelfth follow-up test: Created a stale exec task and observed the operator response.
  Twelfth follow-up result: Operator acknowledged the wake but did not inspect or cancel the task; it asked for human direction.
  Twelfth follow-up takeaway: The agent needs explicit guidance on how to inspect/cancel tasks via the API when woken.
  Thirteenth follow-up change: Added Task API usage hints (inspect/cancel) to the system prompt so the agent can act on wake messages without extra tooling.
  Thirteenth follow-up test: Operator attempted to cancel stale tasks using exec + curl, but exec failed because "code/shell.ts" could not be resolved in the exec sandbox.
  Thirteenth follow-up takeaway: exec needs access to the code/ module path or the prompt should not recommend importing "code/shell.ts".
  Fourteenth follow-up change: Symlink repo `code/` into exec temp dir node_modules so `import "code/shell.ts"` works inside exec tasks. Added prompt guardrails to only cancel exec tasks from wake ids.
  Fifteenth follow-up test: With Task API hints and code module access, operator attempted to cancel stale exec tasks on wake.
  Fifteenth follow-up result: The operator queued an exec task to inspect/cancel, but it was blocked behind the long-running exec (single-worker).
  Fifteenth follow-up takeaway: execd needs parallelism so recovery exec tasks can run while a long exec is in flight.
  Sixteenth follow-up change: Added execd parallelism (default 2) and exposed via `--parallel`/`EXEC_PARALLEL`. Updated compose to run execd with parallel=2.
  Seventeenth follow-up issue: Repeated wake-triggered LLM calls started failing with Anthropic `invalid_request_error` (messages.*.content.*.text.text missing), likely due to reused LLM history across runs with tool calls.
  Seventeenth follow-up change: Switched to fresh LLM sessions per run (no per-agent LLM caching) to avoid corrupted history.
  Eighteenth follow-up issue: execd parallel=2 still blocked because the main loop waited for long tasks to finish before claiming new ones; queued cancellation execs never ran.
  Eighteenth follow-up change: Refactored execd loop to maintain a running set and keep polling/claiming while tasks are in flight (concurrency pool).
  Nineteenth follow-up test: With execd concurrency fixed, operator exec tasks ran concurrently, but the agent only inspected stale tasks and did not cancel.
  Nineteenth follow-up takeaway: The agent needs an explicit default action on wake; inspection alone is insufficient.
  Twentieth follow-up change: Prompt now states that wake ids are already stale and exec tasks should be cancelled by default unless there's a reason not to.

Change log
- Removed the generic `GO_AGENTS_LLM_API_KEY` path; now only provider-specific keys are supported.
- Added explicit “Adding tools” guidance to the system prompt to match how tools are wired in this repo.

Notes from the Pi/OpenClaw blog post
- Pi has a tiny core prompt and only four tools (Read, Write, Edit, Bash), with most capability pushed into extensions.
- Extensions can persist state into sessions; Pi includes hot reload so the agent can write, reload, and test extensions iteratively.
- Sessions are trees: you can branch to do side quests (eg, fix tools) and then summarize back.
- Pi intentionally omits MCP; the philosophy is to have the agent extend itself by writing code rather than downloading skills.
- Pi keeps provider portability by not leaning too hard on provider-specific features; it stores custom session messages for state.
- Extensions can render richer TUI components (spinners, tables, pickers) inside the terminal.
- Extensions/skills can be created quickly and discarded if they turn out not to be needed.
- The agent is expected to build and maintain its own functionality (skills and extensions) rather than rely on community catalogs.
- Experiment: exec task fetch to localhost from inside execd container.
  Hypothesis: exec tasks can reach agentd at http://localhost:8080.
  Test: spawned exec task that fetches /api/health via Bun fetch.
  Result: exec failed with "Unable to connect"; stderr shows inability to access URL.
  Takeaway: exec tasks cannot use localhost to reach agentd in docker. Need a docker-aware API base URL helper and prompt guidance.
- Follow-up change: added code/api.ts helper to auto-discover the agent API base (prefers config.json, then agentd service when in Docker) and updated the prompt to use apiJSON/apiPostJSON instead of curl.
- Validation: exec task using apiJSON('/api/health') succeeded; state cached api_base = http://agentd:8080.
