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
- Validate wake + cancel for LLM tasks (interrupt handling path).
- Test parallel async parent/child tasks where parent cancel kills child.
- Test subagent bidirectional messaging without human involvement (baseline agent-to-agent symmetry).

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

- E026: Runtime health report (real-world ops goal).
  Goal: Produce a concise runtime health report (task counts by status/type, top 3 oldest running tasks, queued exec flag).
  Test: Sent a human request to operator to compute the report using exec + apiJSON.
  Result: LLM task ran, spawned exec subtasks, but became stale and was cancelled with reason "stale from wake message" before completing the report.
  Takeaway: Wake logic can derail user-facing LLM tasks; stale detection should avoid targeting non-exec tasks.

- E027: Wake filtering for exec tasks only.
  Change: In task_health wake logic, only mark exec tasks as stale; keep full snapshots for all tasks. Updated prompt to state wake messages are exec-only.
  Next: Re-run the health report goal after rebuild to verify the LLM task completes without being cancelled.

- E028: Health report after wake filtering.
  Goal: Produce a concise runtime health report using exec + apiJSON.
  Result: LLM returned intent-only text and no exec tasks were created; report was not generated.
  Takeaway: Prompt needs stronger enforcement to avoid intent-only replies.

- E029: Enforce exec-first responses.
  Change: Prompt now forbids intent-only replies and requires exec as the first response when computed data is requested.
  Result: LLM issued an exec tool call, but returned an empty final output; exec results were computed but not surfaced.
  Takeaway: Need explicit instruction to surface tool results and avoid silent completions.

- E030: Tool-result usage + exec thrash.
  Change: Prompt required final textual response after tool calls and to avoid fabricating data.
  Result: LLM spawned many exec tasks, hallucinated a backlog, and produced an inaccurate report despite successful exec results.
  Takeaway: Async exec results are hard for the model to retrieve reliably; need a sync/await path.

- E031: Add exec wait_seconds (sync/await).
  Change: Exec tool now supports wait_seconds to block for completion and return result/pending status. Prompt updated to mention wait_seconds and tool-result usage. Also added exec wait_seconds tests and ensured runtime falls back to existing LLM session when config is missing (test clients).

Additional experiments (unnumbered)
- Experiment: Message delivery after DB reset.
  Hypothesis: Message drops were caused by the corrupted/locked DB; a fresh DB will restore LLM runs.
  Test: Stopped compose, deleted `data/go-agents.db*`, restarted, and sent a “repo risk audit” request.
  Result: LLM task was created and completed; exec tasks ran. The agent produced an audit response.
  Takeaway: The DB state can suppress agent runs; a clean DB restored processing.

- Experiment: Repo risk audit (real-world code review task).
  Goal: “Top 5 concurrency/shutdown risks with file references.”
  Result: LLM used exec with grep-based heuristics and returned a short list pointing to `cmd/agentd/main.go`. The output was concise but shallow (regex-based signals, no deeper reasoning).
  Takeaway: The agent can run multi-step exec tasks but defaults to regex heuristics; the quality ceiling is low without more robust parsing or domain guidance.

- Experiment: API endpoint inventory (real-world API mapping task).
  Goal: JSON map of HTTP endpoints from Go source.
  Result: LLM used exec to regex-scan Go files and returned JSON. Paths were accurate, but handler names were mostly single-letter receiver variables (e.g., `"s"`), which is not the actual function name.
  Takeaway: Naive regex extraction yields misleading handler identifiers. A Go AST parse would be needed for reliable handler names.

- Experiment: TODO/FIXME audit.
  Goal: JSON list of TODO/FIXME comments.
  Result: LLM returned an empty list. Manual `rg` shows TODOs only under `.git/hooks/*.sample`, which are likely out of scope.
  Takeaway: The output is reasonable if `.git` is excluded; confirms agent can return empty datasets when none are found in code.

- Observation: exec tool sometimes fails with `SQLITE_BUSY`.
  Evidence: `llm_tool_done` updates included “spawn exec task: insert task: database is locked (SQLITE_BUSY)” for some tool calls during the risk-audit run, even though other exec tasks succeeded.
  Takeaway: Task creation is still vulnerable to DB lock contention, which can silently degrade tool reliability.

- Experiment: Repo metrics (multi-step data extraction task).
  Goal: JSON with go packages, TS files, largest file by lines, and Go LOC.
  Result: LLM used exec but reported `data/go-agents.db-wal` as the largest file (9797 lines), which is not a source file.
  Takeaway: The agent doesn’t filter out runtime artifacts (`data/`), leading to misleading metrics. It needs heuristics to exclude data/build artifacts when asked for repo stats.

- Experiment: go.mod dependency inventory.
  Goal: JSON array of `{module, version, indirect}` parsed from go.mod.
  Result: LLM used exec and returned 17 entries with correct direct/indirect flags and versions.
  Takeaway: Structured parsing tasks work well when the input is concise and unambiguous (go.mod).

- Process improvement: reproducible experiment runner.
  Change: Added `scripts/run_experiments.ts` and `experiments/specs/long_horizon.json` to run a suite and save artifacts under `experiments/runs/<timestamp>/` (gitignored). Added `experiments/README.md`.
  Takeaway: We can now re-run experiments and inspect full artifacts (task updates, exec outputs, session snapshots, signals) to build an eval suite over time.

- Experiment: Long-horizon suite (lh01/lh02/lh03).
  lh01 (shutdown/concurrency checklist + apply): failed with `max_tokens`. LLM used exec to enumerate and read multiple files; the response never finalized.
  lh02 (AST route inventory + apply): failed with `max_tokens` after exec listing files and repo root.
  lh03 (dependency upgrade plan + apply): exceeded the 120s experiment timeout; task remained running and was manually cancelled (`experiment timeout`).
  Takeaway: Long-horizon prompts that trigger broad file scans cause context overflow or long runtimes. We need to design experiments that bound scan scope and data volume if we want consistent completion without changing the prompt.

- Process improvement: auto-cancel timed-out experiments.
  Change: Updated `scripts/run_experiments.ts` to cancel LLM tasks when an experiment times out (records final status post-cancel).
  Takeaway: Prevents runaway LLM tasks during batch experiment runs.

- Experiment: Long-horizon bounded suite (lhb01/lhb02/lhb03).
  lhb01 (scoped shutdown checklist): still failed with `max_tokens` despite file scoping, likely due to dumping full file contents into the LLM context.
  lhb02 (scoped AST inventory): completed, but exec tasks failed and the LLM produced a hallucinated route list (not present in repo). Indicates tool failure does not always block final responses.
  lhb03 (dependency upgrade plan): timed out; script now auto-cancels on timeout.
  Takeaway: Even scoped prompts can overflow if the LLM chooses to read large files; we need experiments that explicitly constrain data volume (e.g., targeted snippets, or summarizing via exec before returning to LLM).

- Process improvement: capture LLM stop reason + token usage.
  Change: `scripts/run_experiments.ts` now reads full signal events and extracts Anthropic `usage` and `stop_reason` from debug events.
  Takeaway: We can distinguish output token exhaustion from context overflow.

- Experiment: Long-horizon probe (lhp01_scoped_summary).
  Result: Failed with `stop_reason=max_tokens`; usage showed ~2048 output tokens, input tokens ~459 (plus cached prompt tokens). This is output-cap exhaustion, not context length.
  Takeaway: Failures previously attributed to “context window” are actually output token limits. Large tool outputs likely drive the model to verbose reasoning/response that hits the output cap.

- Prompt tweak: encourage subagent delegation for large outputs.
  Change: Added explicit guidance to delegate large intermediate work to subagents and summarize before responding.
  Takeaway: Sets a norm for handling high-volume tasks without expanding the main response.

- Process improvement: file-based utilities for large outputs.
  Change: Added `code/fs.ts` with basic file helpers and updated `code/shell.ts` to auto-write oversized stdout/stderr to temp files (`stdoutPath`/`stderrPath`).
  Takeaway: Large command outputs can be redirected to files, keeping LLM responses small and inspectable.

- Experiment: large-output file routing (fo01).
  Goal: List all Go files with line counts, return only top 5 and filename for full list.
  Result: LLM used `writeText` to save full list to `/tmp/go_files_full_list.json` and returned top 5 in JSON. Output tokens stayed low.
  Takeaway: File-based workflows reduce output token pressure and help long-horizon tasks complete.

- Analysis: “context window exceeded” failures.
  Finding: Failures are due to output token limits (`stop_reason=max_tokens`, output_tokens≈2048), not input context length. Large tool outputs trigger verbose responses that hit the output cap.
  Takeaway: To improve reliability without more tools, we need to bound output volume (file-based outputs, subagent summaries, or strict snippet limits).

- Reading notes: “What I learned building an opinionated and minimal coding agent.”
  Takeaway: Emphasizes strict context control, full observability of model interactions, minimal system prompt/tooling, and file-based state (TODO/plan files) instead of hidden modes. Suggests keeping agent core small and using files for durable state and large outputs.

- Experiment: subagent diary summary (sa01).
  Goal: Delegate diary scan to a subagent and return a short top-5 summary plus a detail file path.
  Result: Completed with low output tokens; returned a short JSON and a detail file path.
  Takeaway: Subagent delegation can keep the main response small and reliable when it succeeds.

- Experiment: subagent runbook (sa02).
  Goal: Subagent drafts a longer runbook, main agent returns a short summary + file path.
  Result: The main agent replied with “waiting for subagent,” but no exec tasks ran and the send_message tool failed due to SQLITE_BUSY (event insert lock).
  Takeaway: Eventbus write contention (SQLITE_BUSY) can break subagent workflows; this is a reliability bottleneck for long-horizon delegation.

- Observation: send_message can fail with SQLITE_BUSY.
  Evidence: `llm_tool_done` result showed “SendMessage failed: insert event: database is locked (SQLITE_BUSY)” in the subagent run.
  Takeaway: SQLite locking affects event delivery; long-horizon orchestration needs more robust event writes or retries.

- Fix attempt: SQLite busy mitigation.
  Change: Open DB with pragma DSN (busy_timeout/WAL/foreign_keys) and restrict pool to 1 connection; add retry/backoff for eventbus inserts and task update writes.
  Takeaway: Should prevent transient SQLITE_BUSY during concurrent writes (needs live validation).

- Experiment batch (neutral prompts): long_horizon / bounded / probe / file_output / subagent_summary / audio_transcribe.
  Goal: Rerun all experiments with non-technical prompts to avoid biasing solutions.
  Results:
  - long_horizon: lh01 failed to spawn an LLM task; lh02 timed out (cancelled during tool_use); lh03 completed but returned a verbose dependency review.
  - bounded: all three (lhb01/lhb02/lhb03) timed out and were cancelled during tool_use; outputs were inconsistent (e.g., shutdown prompt yielded dependency review).
  - probe: cancelled without output.
  - file_output: completed; returned largest Go files list with low output tokens.
  - subagent_summary: sa01 cancelled; sa02 failed due to Anthropic EOF (network/API error).
  - audio_transcribe: task hung; manually cancelled; no transcript produced.
  Takeaway: With unbiased prompts, the agent often stalls in tool_use and times out; it can still handle narrow, bounded tasks. Subagent orchestration remains fragile and external network/API stability is a limiting factor.

- Process improvement: experiment runner now handles fetch timeouts without crashing.
  Change: `apiFetch` now catches abort errors and returns a structured error instead of throwing.
  Takeaway: Batch runs can proceed even when the API stalls.
  Result: A wait_seconds run hit provider error `max_tokens` before completing.
  Takeaway: Need to keep tool use concise and/or add retry logic for max_tokens failures.

- E032: LLM visibility instrumentation.
  Goal: Ensure full visibility into raw LLM requests/events, thinking, tool calls, and output.
  Change: Added bus debugger (signals stream) for RawRequest/RawEvent with API key redaction, and task_output updates for llm_text/thinking/tool_* events.
  Result: signals stream shows llm_debug_request with full payload; task_output shows LLM task updates.
  Takeaway: Debug visibility is working; need to validate tool and thinking streams after fixing thinking budget.

- E033: Anthropic thinking budget validation.
  Hypothesis: Enabling thinking with budget 512 should work.
  Result: Anthropic returned 400 invalid_request_error: thinking.enabled.budget_tokens must be >= 1024.
  Fix: Raised default thinking budget to 1024 in ai client.

- E034: LLM tool update loss due to SQLITE_BUSY.
  Observation: llm_tool_done updates were missing; signals reported llm_update_error: database is locked (SQLITE_BUSY) during tool_done insert.
  Change: Added retry with short backoff for llm update writes; log to signals on persistent failure.
  Next: Re-run a tool-using task to confirm tool_done is recorded.

- E035: LLM visibility validation (thinking + tool calls).
  Goal: Confirm thinking, tool_start/delta/done, and tool results are captured end-to-end.
  Test: Asked agent to count dependencies in go.mod (requires exec).
  Result: task_output now includes llm_thinking, llm_tool_start/delta/done, and llm_text; tool_done payload recorded after retry logic.
  Takeaway: Full LLM visibility is functional after adding retry for SQLITE_BUSY.

- Debugging improvement: capture partial tool calls and per-turn tool input.
  Change: experiment runner now records reconstructed tool inputs, tool deltas, and per-tool delta stats. Also parses raw Anthropic debug events into per-message summaries (message_start/stop, stop_reason, tool_use blocks) when available.
  Takeaway: We can now see if a tool call JSON was truncated and whether a failure happened mid-tool-input vs after tool execution.

- Debugging trust check: multi-turn tool usage vs single-turn batching.
  Finding: For lh02_api_inventory, 17 tool calls were emitted across 17 message_start events (sequential turns), not parallel tool calls. The last tool call never emitted any deltas, indicating the model hit max_tokens before starting the next tool call.
  Takeaway: Earlier inference about a single response batching tool calls was incorrect due to missing message_stop tracking; message-level debug now addresses this gap.

- Max output tokens tuning for Anthropic.
  Change: Set output max tokens to 64,000 and thinking to 0; this caused provider 400 because output+thinking exceeded the 64k cap. Fixed by setting output max tokens to 62,976 with thinking 1,024 (total 64,000).
  Takeaway: Anthropic enforces max_tokens as output+thinking; we must budget both.

- Experiment batch (max output 64k, thinking 1024): all suites completed.
  Runs: 2026-02-04T17-53-41-785Z, 17-57-17-762Z, 18-00-44-746Z, 18-02-22-220Z, 18-02-41-424Z, 18-05-22-267Z.
  Results:
  - long_horizon: all 3 completed, end_turn.
  - bounded: all 3 completed, end_turn.
  - probe, file_output, subagent_summary, audio_transcribe: completed, end_turn.
  Metrics snapshot: exec_calls ranged 2-18; reuse_ratio mostly 0.85-1.0; tool creation was rare.
  Takeaway: Output token cap was the main failure mode for earlier runs; with 64k budget, prompts complete without stalls.

- Observation: minimal toolset still yields many small exec calls.
  Evidence: lh02_api_inventory required 17 exec calls; audio_transcribe used 18 exec calls.
  Takeaway: The model prefers iterative probing rather than creating reusable helpers without explicit incentive.

- Experiment: Self-improvement suite (self_improve.json).
  Goal: Measure tool creation, step reduction, and token economy with minimal prompting.
  Run: 2026-02-04T18-51-32-448Z.
  Results:
  - si01_endpoint_report: completed, exec_calls=5, reuse_ratio=1.0, file_writes=1, tool_creation=1.
  - si02_endpoint_refresh: completed, exec_calls=7, reuse_ratio=1.0, file_writes=0, tool_creation=0.
  - si03_large_files_to_file: completed, exec_calls=1, file_writes=1, tool_creation=1.
  - si04_todo_to_file: completed, exec_calls=3, file_writes=1, tool_creation=1.
  - si05_self_improve_helper: completed, exec_calls=1, file_writes=1, tool_creation=1.
  Artifact: Agent created reusable helpers under code/: code/dir.ts, code/json.ts, code/strings.ts, code/tasks.ts.
  Takeaway: The minimal prompt plus explicit “make it easy to repeat” nudges can trigger helper creation, but reuse in the immediate follow-up (si02) did not reduce exec_calls; the model still re-ran discovery steps.

Future experiments (focus: minimal toolset + self-improvement)
- Reuse enforcement: repeat a task twice and check if newly created helpers are actually used (compare exec_calls and tool_inputs).
- Helper discovery: ask for a task that benefits from code/json.ts or code/tasks.ts and see whether the agent naturally uses them.
- Step budget: add an explicit “limit yourself to 3 exec calls” constraint and see if tool creation emerges as a workaround.
- File-first outputs: ask for large outputs with required file paths to measure output_tokens reduction and whether summaries are concise.

Future experiments (minimal toolset, encourage self-improvement)
- Tool creation incentive test: ask for a task that naturally repeats a shell pipeline twice and see whether it writes a reusable helper under code/. If not, add a single prompt sentence encouraging reusable helpers and compare exec_calls + reuse_ratio.
- Step reduction test: same task run twice; second run should reuse a helper and reduce exec_calls and wall-clock time. Measure deltas.
- Token economy test: ask for a high-volume report but require a file output with a short summary; measure output_tokens and check file path usage.
- Self-improvement loop test: ask the agent to identify one bottleneck in its own workflow (e.g., repeated grep) and propose a small helper; then run a follow-up task to see if the helper is used.
- Minimal prompt ablation: remove the tool-creation hint, run the same task, compare tool creation and exec_calls to verify the hint is effective.

## 2026-02-05
Experiment log
- NT01–NT05: Non-technical suite (real-world requests without solution hints).
  Hypothesis: With minimal prompting, the agent will (a) use exec to ground answers, (b) save long outputs to files, and (c) reuse prior artifacts when asked to refresh.
  Run: 2026-02-05T08-40-10-416Z (nontechnical_suite.json).
  Results summary:
  - nt01_project_intro: completed, exec_calls=4, file_writes=1, tool_creation=1. Wrote `GETTING_STARTED.md` in repo. One exec failed, but final response still succeeded.
  - nt02_project_intro_refresh: completed, exec_calls=1. Exec failed (missing `/tmp/project_summary_*.txt`), but the agent still claimed success and invented file paths. No evidence those files exist in container or host.
  - nt03_shutdown_risks: completed, exec_calls=10, file_writes=1. Saved `/tmp/work_loss_risks.md` inside exec container. Output was detailed and grounded, but relied on /tmp paths.
  - nt04_interface_inventory: completed, exec_calls=3, file_writes=3. Saved `/tmp/interaction_methods_full.txt` in exec container; short summary returned.
  - nt05_running_requirements: completed, exec_calls=3, file_writes=1. Wrote `SETUP_CHECKLIST.md` in repo. Parsed `.env` correctly but incorrectly asserted OpenAI/Google keys are “required.”
  Takeaways:
  - The agent can complete non-technical tasks and often writes files, but it chooses `/tmp` (container-local) paths that are not visible on host; outputs need a stable, shared path strategy.
  - Tool failure does not prevent confident final answers. When exec failed, the agent fabricated file outputs and still reported success.
  - The agent interprets “present but empty” config keys as required; it needs a way to distinguish optional vs required without guesswork.
  - Reuse behavior is weak; the “refresh” task did not locate earlier artifacts and instead fabricated new ones.

Next experiments (minimal prompt, focus on reliability without extra tools)
- Verify “refresh” behavior with explicit artifact location: ask for a report saved to a repo path, then ask to update it and see if reuse succeeds without prompting “use that file.”
- Validate error-grounding: ask for a report, then intentionally point to a missing artifact and see if the agent admits failure instead of fabricating.
- Test “shared output” default: ask for large outputs and observe whether it prefers repo paths vs /tmp; evaluate whether a stable `output/` directory improves retrieval without more prompt guidance.

- NT06–NT10: Non-technical reuse + artifact locality suite.
  Hypothesis: If asked to save artifacts “in the project,” the agent will choose repo paths and reliably reuse them on refresh.
  Run: 2026-02-05T08-48-16-232Z (nontechnical_suite_2.json).
  Results summary:
  - nt06_onboarding_guide: completed, exec_calls=1. Wrote `ONBOARDING.md` in repo with a long guide and short summary.
  - nt07_onboarding_refresh: completed but failed behaviorally; it asked for clarification and did not read/update `ONBOARDING.md` (exec_calls=0).
  - nt08_missing_artifact: completed, exec_calls=2. Claimed it couldn’t find a prior guide and created `DETAILED_AGENT_GUIDE.md`. Search exec ran after the write and failed; it likely would have created a new guide even if the old one existed.
  - nt09_config_checklist: completed, exec_calls=8. Wrote `SETUP-CHECKLIST.md` (new file) and listed requirements. Output still includes optional provider keys, but framed as optional.
  - nt10_interface_report: completed, exec_calls=9. Wrote `INTERACTION_REPORT.md` with endpoint/tool summaries.
  Takeaways:
  - Asking for “save it in the project” shifts output away from `/tmp` and into repo files, improving visibility.
  - Reuse is still weak: the refresh prompt did not trigger a file read; the agent requested clarification instead of assuming the latest artifact.
  - Missing-artifact prompt caused it to create a new guide without verifying first; it attempted a search only after writing.
  - Artifact naming drift (`SETUP_CHECKLIST.md` vs `SETUP-CHECKLIST.md`) makes reuse brittle.

Platform changes (prompt system + sandbox)
- Moved the system prompt to Bun (`template/PROMPT.ts`) with a minimal prompt builder (`template/core/prompt-builder.ts`).
- Added embedded template seeding into `~/.karna` (Go copies template on first boot).
- Exec now runs with `~/.karna` as CWD and exposes `tools/` + `core/` via module alias.
- Docker now builds a self-contained image (Go binary embedded + Bun), with no host mounts; `.env` is passed via compose env_file.
- Fixed execd to create `~/.karna` from template on startup and to spawn Bun via `process.execPath`.

Experiment log (new prompt system)
- NT01–NT05 (nontechnical_suite.json) on the new `~/.karna` prompt:
  Run: 2026-02-05T13-38-55-543Z.
  Results:
  - nt01_project_intro: wrote `~/.karna/GETTING_STARTED.md`. Uses exec and writes inside `.karna`.
  - nt02_project_intro_refresh: asked for clarification; did not locate `GETTING_STARTED.md`.
  - nt03_shutdown_risks: produced a long response with many exec calls; saved `~/.karna/data_loss_risk_report.json` but references some incorrect repo paths.
  - nt04_interface_inventory: saved `~/.karna/interaction_methods.json` without citing actual API routes.
  - nt05_running_requirements: saved `~/.karna/setup_checklist.json`, but claimed “no API keys needed” which is incorrect for real LLM use.
  Takeaways:
  - The new prompt keeps work inside `~/.karna` and writes files reliably.
  - The agent still struggles with “refresh” prompts unless the file path is explicit.
  - Without stronger grounding, it fabricates or misstates endpoints and requirements.

- SI01–SI05 (self_improve.json) on the new `~/.karna` prompt:
  Run: 2026-02-05T13-45-14-171Z.
  Results:
  - si01_endpoint_report: created `tools/list-endpoints.ts` but returned an inaccurate endpoint list (nonexistent endpoints and paths).
  - si02_endpoint_refresh: asked for clarification instead of reusing outputs.
  - si03_large_files_to_file: created file outputs and helper scripts inside `~/.karna/tools`.
  - si04_todo_to_file: wrote `~/.karna/todo-fixme-list.txt` with zero TODO/FIXME (reasonable).
  - si05_self_improve_helper: created `tools/helpers.ts` and `tools/HELPERS.md` with a broad helper library.
  Takeaways:
  - The agent now self-creates tools in `~/.karna/tools` without prompting, which is promising.
  - Some tools are over-broad and not tied to actual needs (helpers include HTTP, CSV, etc. without use).
  - Endpoint extraction is still unreliable; agent tends to “infer” endpoints rather than parse routes precisely.

## 2026-02-05
Experiment log
- Simplified API surface to match the realtime UI + experiment needs.
  - Kept: /api/state (snapshot), /api/streams/subscribe (SSE), /api/agents/{id}/run, /api/tasks/queue (exec worker), /api/tasks/{id}/updates|complete|fail|send|cancel|kill.
  - Removed: /api/tasks list/create/get, /api/events, /api/prompt, /api/diagnostics, /api/sessions, /api/streams list/read/ack, /api/streams/ws, /api/actions, /api/agents list/create, /api/admin/restart.
  - Updated the experiment runner to use /api/state only.

- Experiment: file_output (largest Go files) after API simplification.
  Hypothesis: Agent will create an LLM task, use exec, and return correct Go file counts.
  Test: ran `bun scripts/run_experiments.ts experiments/specs/file_output.json`.
  Result: LLM task not found. Root cause: PROMPT.ts had backticks inside the prompt string (e.g., `$.env()`), which caused the Bun prompt builder to fail; HandleMessage exited before spawning tasks.
  Fix: removed backticks from PROMPT.ts, then copied the updated file into the running container at /root/.karna/PROMPT.ts.
  Takeaway: prompt build failures silently prevent task creation; we should surface prompt build errors or add a preflight check. Also note that template updates do not reach existing ~/.karna without a rebuild or manual sync.

- Experiment: file_output re-run with prompt build fixed.
  Hypothesis: With a valid prompt, the agent will use exec to compute Go file sizes.
  Result: LLM task completed and used exec, but reported “no Go files” because it ran find in ~/.karna (default working directory), not the repo. Output was incorrect.
  Takeaway: Success path now executes tools, but the default cwd is wrong for repo inspection. We need either prompt guidance to cd into /app/go-agents when asked about “this project,” or a small helper that resolves project root.
  What worked: LLM spawned exec tasks and streamed tool deltas; wait_seconds usage appeared in tool calls.
  What didn’t: The agent did not infer the repo location; it defaulted to ~/.karna.

Next replication targets (post-API simplification)
- Re-run a “health report” style request, ensuring the prompt builds and the agent can reach the correct data sources.
- Re-run a task-cancellation wake scenario and verify task_health + wake behavior still functions with the new API surface.
- Add a minimal “project root” hint (or helper) and re-run file_output to confirm correct file discovery.
