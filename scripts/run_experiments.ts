import { mkdir, readFile, writeFile } from "fs/promises";
import { join } from "path";

type ExperimentSpec = {
  id: string;
  title?: string;
  prompt: string;
  timeoutSeconds?: number;
};

type SpecFile = {
  id?: string;
  description?: string;
  experiments: ExperimentSpec[];
};

const DEFAULT_BASE = "http://localhost:8080";
const DEFAULT_SPEC = "experiments/specs/consolidated_repo_intelligence.json";
const DEFAULT_TIMEOUT_SECONDS = Number(
  process.env.GO_AGENTS_EXPERIMENT_TIMEOUT || "300",
);
const REQUEST_TIMEOUT_MS = Number(
  process.env.GO_AGENTS_REQUEST_TIMEOUT_MS || "20000",
);
const REQUEST_RETRIES = Number(process.env.GO_AGENTS_REQUEST_RETRIES || "3");

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

const apiFetch = async (base: string, path: string, init?: RequestInit) => {
  const url = path.startsWith("http") ? path : `${base}${path}`;
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS);
  try {
    const resp = await fetch(url, { ...init, signal: controller.signal });
    const text = await resp.text();
    clearTimeout(timeout);
    let json: any = null;
    if (text) {
      try {
        json = JSON.parse(text);
      } catch {
        json = { error: "invalid json", raw: text };
      }
    }
    return { status: resp.status, json };
  } catch (err: any) {
    clearTimeout(timeout);
    return {
      status: 0,
      json: { error: err?.message || "fetch failed" },
    };
  }
};

const postJSON = (base: string, path: string, body: any) =>
  apiFetch(base, path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });

const postJSONWithRetry = async (
  base: string,
  path: string,
  body: any,
  retries: number = REQUEST_RETRIES,
) => {
  let last: { status: number; json: any } = { status: 0, json: null };
  for (let attempt = 0; attempt <= retries; attempt++) {
    last = await postJSON(base, path, body);
    const retriable =
      last.status === 0 || last.status >= 500 || last.status === 429;
    if (!retriable) return last;
    if (attempt < retries) {
      await sleep(250 * (attempt + 1));
    }
  }
  return last;
};

const fetchState = (base: string, params?: string) =>
  apiFetch(
    base,
    `/api/state?tasks=200&updates=2000&streams=2000${params ? `&${params}` : ""}`,
  );

const normalizeISO = (value: string) => {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return null;
  return date;
};

const findNewestTask = (tasks: any[], startedAt: Date) => {
  if (!Array.isArray(tasks)) return null;
  const startedMs = startedAt.getTime();
  const candidates = tasks
    .map((task) => {
      const created = normalizeISO(task.created_at);
      return { task, created };
    })
    .filter((entry) => entry.created && entry.created.getTime() >= startedMs - 1000)
    .sort((a, b) => (b.created?.getTime() ?? 0) - (a.created?.getTime() ?? 0));
  return candidates.length > 0 ? candidates[0].task : null;
};

const requestIDFor = (expId: string, index: number) =>
  `${expId}-${Date.now()}-${index}`;

const taskRequestID = (task: any) => {
  if (!task || typeof task !== "object") return "";
  const meta = task.metadata || {};
  const val = meta.request_id;
  return typeof val === "string" ? val : "";
};

const taskEventID = (task: any) => {
  if (!task || typeof task !== "object") return "";
  const meta = task.metadata || {};
  const val = meta.event_id;
  return typeof val === "string" ? val : "";
};

const unknownFieldError = (resp: { status: number; json: any }) => {
  if (!resp || resp.status !== 400) return false;
  const msg = resp?.json?.error;
  return typeof msg === "string" && msg.includes("unknown field");
};

const normalizeTaskTime = (value?: string) => {
  if (!value) return null;
  const dt = new Date(value);
  if (Number.isNaN(dt.getTime())) return null;
  return dt;
};

const filterExecTasks = (tasks: any[], parentID: string, startedAt: Date) => {
  if (!Array.isArray(tasks)) return [];
  const startedMs = startedAt.getTime() - 1000;
  return tasks.filter((task) => {
    if (!task) return false;
    if (parentID && task.parent_id !== parentID) return false;
    const createdAt = normalizeTaskTime(task.created_at);
    if (!createdAt) return false;
    return createdAt.getTime() >= startedMs;
  });
};

const computeExecMetrics = (execTasks: any[]) => {
  const metrics = {
    exec_calls: execTasks.length,
    exec_failed: 0,
    exec_reused_tool: 0,
    exec_file_writes: 0,
    exec_tool_creation: 0,
    reuse_ratio: 0,
  };

  const reuseRegex = new RegExp(
    'from\\s+["\\\'](code|tools|core)/|\\bbun\\s+(code|tools|core|scripts)/',
    "i",
  );
  const writeRegex = new RegExp(
    "Bun\\.write\\(|writeText\\(|appendText\\(|writeFile\\(|fs\\.writeFile|\\bcat\\s+[^\\n>]+\\s*>|\\btee\\b|\\bprintf\\b.*>",
  );
  const toolCreateRegex = new RegExp(
    "Bun\\.write\\(\\s*[`\"'](?:code|tools|core|scripts)/|write(File|Text|appendText)?\\(\\s*[`\"'](?:code|tools|core|scripts)/|\\bcat\\b[^\\n>]*>\\s*(?:code|tools|core|scripts)/|\\btee\\s+(?:code|tools|core|scripts)/",
    "i",
  );

  for (const task of execTasks) {
    const code = task?.payload?.code || "";
    if (task?.status === "failed") metrics.exec_failed += 1;
    if (reuseRegex.test(code)) metrics.exec_reused_tool += 1;
    if (writeRegex.test(code)) metrics.exec_file_writes += 1;
    if (writeRegex.test(code) && toolCreateRegex.test(code)) {
      metrics.exec_tool_creation += 1;
    }
  }

  metrics.reuse_ratio =
    metrics.exec_calls > 0 ? metrics.exec_reused_tool / metrics.exec_calls : 0;

  return metrics;
};

const ensureDir = async (path: string) => {
  await mkdir(path, { recursive: true });
};

const writeJSON = async (path: string, data: any) => {
  await writeFile(path, JSON.stringify(data, null, 2));
};

const parseDebugUsage = (signals: any[]) => {
  let usage: any = null;
  let stopReason: string | null = null;
  if (!Array.isArray(signals)) return { usage, stopReason };
  for (const evt of signals) {
    if (!evt || evt.subject !== "llm_debug_event") continue;
    const data: string | undefined = evt.payload?.data;
    if (!data || !data.startsWith("data:")) continue;
    const jsonText = data.replace(/^data:\s*/, "");
    try {
      const obj = JSON.parse(jsonText);
      if (obj?.type === "message_delta") {
        if (obj.usage) {
          usage = obj.usage;
        }
        if (obj.delta?.stop_reason) {
          stopReason = obj.delta.stop_reason;
        }
      }
    } catch {
      continue;
    }
  }
  return { usage, stopReason };
};

const extractDebugMessages = (signals: any[]) => {
  if (!Array.isArray(signals)) return [];
  const events = [];
  for (const evt of signals) {
    if (evt?.subject !== "llm_debug_event") continue;
    const data: string | undefined = evt.payload?.data;
    if (!data || !data.startsWith("data:")) continue;
    const jsonText = data.replace(/^data:\s*/, "");
    try {
      const obj = JSON.parse(jsonText);
      events.push({ ...obj, created_at: evt.created_at });
    } catch {
      continue;
    }
  }
  events.sort((a, b) => {
    const ta = Date.parse(a.created_at || "") || 0;
    const tb = Date.parse(b.created_at || "") || 0;
    return ta - tb;
  });

  const messages: any[] = [];
  let current: any = null;
  for (const ev of events) {
    if (ev.type === "message_start") {
      if (current) messages.push(current);
      current = {
        message_id: ev.message?.id || ev.message_id || null,
        stop_reason: null,
        usage: null,
        tool_calls: [],
        started_at: ev.created_at,
        ended_at: null,
      };
      continue;
    }
    if (!current) continue;
    if (ev.type === "message_delta") {
      if (ev.delta?.stop_reason) current.stop_reason = ev.delta.stop_reason;
      if (ev.usage) current.usage = ev.usage;
    }
    if (ev.type === "content_block_start") {
      const block = ev.content_block || {};
      if (block.type === "tool_use") {
        current.tool_calls.push({
          tool_use_id: block.id || null,
          name: block.name || null,
        });
      }
    }
    if (ev.type === "message_stop") {
      current.ended_at = ev.created_at;
      messages.push(current);
      current = null;
    }
  }
  if (current) messages.push(current);
  return messages;
};

const extractLLMText = (updates: any[]) => {
  if (!Array.isArray(updates)) return "";
  return updates
    .filter((u) => u?.kind === "llm_text")
    .map((u) => u?.payload?.text || "")
    .join("");
};

const extractToolInputs = (updates: any[]) => {
  if (!Array.isArray(updates)) return [];
  const tools = new Map<
    string,
    { id: string; name?: string; raw: string; parse_error?: string | null }
  >();
  for (const u of updates) {
    if (u?.kind === "llm_tool_start") {
      const id = u?.payload?.tool_call_id;
      if (!id) continue;
      const entry = tools.get(id) || { id, raw: "" };
      entry.name = u?.payload?.tool_name || entry.name;
      tools.set(id, entry);
    }
    if (u?.kind === "llm_tool_delta") {
      const id = u?.payload?.tool_call_id;
      if (!id) continue;
      const entry = tools.get(id) || { id, raw: "" };
      entry.raw += u?.payload?.delta || "";
      tools.set(id, entry);
    }
  }
  const out: any[] = [];
  for (const entry of tools.values()) {
    let parsed: any = null;
    let parseError: string | null = null;
    if (entry.raw) {
      try {
        parsed = JSON.parse(entry.raw);
      } catch (err: any) {
        parseError = err?.message || "parse error";
      }
    } else {
      parseError = "empty tool input";
    }
    out.push({
      id: entry.id,
      name: entry.name,
      raw: entry.raw,
      parsed,
      parse_error: parseError,
    });
  }
  return out;
};

const extractToolDeltas = (updates: any[]) => {
  if (!Array.isArray(updates)) return [];
  return updates
    .filter((u) => u?.kind === "llm_tool_delta")
    .map((u) => ({
      tool_call_id: u?.payload?.tool_call_id,
      delta: u?.payload?.delta || "",
      created_at: u?.created_at,
    }))
    .filter((u) => u.tool_call_id || u.delta);
};

const summarizeToolDeltas = (deltas: any[]) => {
  const stats: Record<
    string,
    { tool_call_id: string; delta_count: number; delta_bytes: number }
  > = {};
  for (const entry of deltas || []) {
    const id = entry?.tool_call_id || "unknown";
    if (!stats[id]) {
      stats[id] = { tool_call_id: id, delta_count: 0, delta_bytes: 0 };
    }
    stats[id].delta_count += 1;
    stats[id].delta_bytes += (entry?.delta || "").length;
  }
  return Object.values(stats);
};

const loadSpec = async (path: string): Promise<SpecFile> => {
  const raw = await readFile(path, "utf8");
  return JSON.parse(raw);
};

const main = async () => {
  const base = process.env.GO_AGENTS_BASE || DEFAULT_BASE;
  const specPath = process.argv[2] || DEFAULT_SPEC;
  const spec = await loadSpec(specPath);

  const runId = new Date().toISOString().replace(/[:.]/g, "-");
  const outDir = join("experiments", "runs", runId);
  await ensureDir(outDir);

  await writeJSON(join(outDir, "run.json"), {
    run_id: runId,
    base_url: base,
    spec_path: specPath,
    spec_id: spec.id ?? null,
    description: spec.description ?? null,
    started_at: new Date().toISOString(),
  });

  for (const [index, exp] of spec.experiments.entries()) {
    const expId = exp.id || `exp_${index + 1}`;
    const expDir = join(outDir, expId);
    await ensureDir(expDir);

    const startedAt = new Date();
    const requestID = requestIDFor(expId, index + 1);
    let request: Record<string, any> = {
      message: exp.prompt,
      source: "external",
      priority: "wake",
      request_id: requestID,
    };
    let runResp = await postJSONWithRetry(base, "/api/agents/run", request);
    let correlationRequestID = requestID;
    let correlationEventID =
      typeof runResp?.json?.event_id === "string" ? runResp.json.event_id : "";
    let agentID =
      typeof runResp?.json?.agent_id === "string" ? runResp.json.agent_id : "";
    if (unknownFieldError(runResp)) {
      request = {
        message: exp.prompt,
        source: "external",
      };
      runResp = await postJSONWithRetry(base, "/api/agents/run", request);
      correlationRequestID = "";
      correlationEventID =
        typeof runResp?.json?.event_id === "string" ? runResp.json.event_id : "";
      agentID =
        typeof runResp?.json?.agent_id === "string" ? runResp.json.agent_id : "";
    }

    await writeJSON(join(expDir, "request.json"), {
      experiment: exp,
      request,
      response: runResp,
      started_at: startedAt.toISOString(),
    });

    let llmTask: any = null;
    let latestState: any = null;
    for (let i = 0; i < 60; i++) {
      const stateResp = await fetchState(base);
      latestState = stateResp.json;
      const tasks = Array.isArray(latestState?.tasks) ? latestState.tasks : [];
      const llmCandidates = tasks.filter((task: any) => {
        if (task?.type !== "llm") return false;
        if (agentID && task?.owner !== agentID) return false;
        if (!correlationRequestID && !correlationEventID) return true;
        if (correlationRequestID && taskRequestID(task) === correlationRequestID) {
          return true;
        }
        if (correlationEventID && taskEventID(task) === correlationEventID) {
          return true;
        }
        return false;
      });
      llmTask = findNewestTask(llmCandidates, startedAt);
      if (llmTask) break;
      await sleep(500);
    }

    if (!llmTask) {
      await writeJSON(join(expDir, "result.json"), {
        error: "llm task not found",
        request_id: correlationRequestID,
        event_id: correlationEventID,
        run_response: runResp,
      });
      continue;
    }

    const timeout = (exp.timeoutSeconds ?? DEFAULT_TIMEOUT_SECONDS) * 1000;
    const deadline = Date.now() + timeout;
    let llmTaskFinal = llmTask;
    while (Date.now() < deadline) {
      const stateResp = await fetchState(base);
      latestState = stateResp.json;
      const tasks = Array.isArray(latestState?.tasks) ? latestState.tasks : [];
      const candidate = tasks.find((task: any) => task.id === llmTask.id);
      if (candidate) {
        llmTaskFinal = candidate;
      }
      if (
        llmTaskFinal &&
        ["completed", "failed", "cancelled"].includes(llmTaskFinal.status)
      ) {
        break;
      }
      await sleep(1000);
    }
    if (
      llmTaskFinal &&
      !["completed", "failed", "cancelled"].includes(llmTaskFinal.status)
    ) {
      await postJSON(base, `/api/tasks/${llmTask.id}/cancel`, {
        reason: "experiment timeout",
      });
      const stateResp = await fetchState(base);
      latestState = stateResp.json;
      const tasks = Array.isArray(latestState?.tasks) ? latestState.tasks : [];
      const candidate = tasks.find((task: any) => task.id === llmTask.id);
      if (candidate) {
        llmTaskFinal = candidate;
      }
    }

    const updatesByTask = latestState?.updates || {};
    const updatesForLLM = updatesByTask[llmTask.id] || [];
    const sessions = latestState?.sessions || {};
    const session = (agentID && sessions[agentID]) || null;

    const execTasks = (latestState?.tasks || []).filter(
      (task: any) => task.type === "exec",
    );
    const execTasksForRun = filterExecTasks(execTasks, llmTask.id, startedAt);
    const metrics = computeExecMetrics(execTasksForRun);
    const execDetails = execTasksForRun.map((task: any) => ({
      task,
      updates: updatesByTask[task.id] || [],
    }));

    const startedMs = startedAt.getTime();
    const signals = Array.isArray(latestState?.streams?.signals)
      ? latestState.streams.signals.filter((evt: any) => {
          const ts = normalizeISO(evt.created_at);
          return ts && ts.getTime() >= startedMs - 1000;
        })
      : [];

    const debug = parseDebugUsage(signals);
    const llmMessages = extractDebugMessages(signals);
    const llmText = extractLLMText(updatesForLLM);
    const llmToolInputs = extractToolInputs(updatesForLLM);
    const llmToolDeltas = extractToolDeltas(updatesForLLM);
    const llmToolDeltaStats = summarizeToolDeltas(llmToolDeltas);

    await writeJSON(join(expDir, "result.json"), {
      llm_task: llmTaskFinal,
      agent_id: agentID || null,
      correlation: {
        request_id: correlationRequestID,
        event_id: correlationEventID,
        llm_task_request_id: taskRequestID(llmTaskFinal),
        llm_task_event_id: taskEventID(llmTaskFinal),
      },
      updates: updatesForLLM,
      session,
      exec_tasks: execDetails,
      metrics,
      llm_text: llmText,
      llm_tool_inputs: llmToolInputs,
      llm_tool_deltas: llmToolDeltas,
      llm_tool_delta_stats: llmToolDeltaStats,
      llm_messages: llmMessages,
      signals,
      llm_usage: debug.usage,
      llm_stop_reason: debug.stopReason,
      finished_at: new Date().toISOString(),
    });
  }
};

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
