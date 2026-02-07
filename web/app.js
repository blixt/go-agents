import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import { marked } from "marked";
import DOMPurify from "dompurify";
import Prism from "prismjs";
import "prismjs/components/prism-markup";
import "prismjs/components/prism-javascript";
import "prismjs/components/prism-typescript";

const API_BASE = "";
const STREAMS = ["history", "messages", "task_output", "signals", "errors", "external"];
const STREAM_LIMIT = 200;
const TOOL_EVENT_TYPES = new Set(["tool_call", "tool_status", "tool_result"]);

function formatDateTime(ts) {
  if (!ts) return "-";
  try {
    return new Date(ts).toLocaleString();
  } catch (_) {
    return "-";
  }
}

function formatTime(ts) {
  if (!ts) return "-";
  try {
    return new Date(ts).toLocaleTimeString();
  } catch (_) {
    return "-";
  }
}

function normalizeStatus(status) {
  return String(status || "idle")
    .toLowerCase()
    .trim()
    .replace(/[^a-z0-9_-]+/g, "-");
}

function parseJSONSafe(raw) {
  try {
    return { ok: true, value: JSON.parse(raw), error: "" };
  } catch (err) {
    return { ok: false, value: null, error: err?.message || String(err) };
  }
}

async function fetchJSON(path) {
  const res = await fetch(`${API_BASE}${path}`);
  if (!res.ok) {
    throw new Error(`request failed: ${res.status}`);
  }
  return res.json();
}

function cloneHistory(history) {
  if (!history) {
    return { agent_id: "", generation: 1, entries: [] };
  }
  return {
    agent_id: history.agent_id || "",
    generation: typeof history.generation === "number" ? history.generation : 1,
    entries: Array.isArray(history.entries) ? [...history.entries] : [],
  };
}

function parseHistoryEvent(evt) {
  if (!evt || evt.stream !== "history" || !evt.payload) return null;
  const payload = evt.payload || {};
  const hasContent = Object.prototype.hasOwnProperty.call(payload, "content");
  const entry = {
    id: evt.id || payload.id || "",
    agent_id: payload.agent_id || evt.scope_id || "",
    generation: Number(payload.generation || evt.metadata?.generation || 1) || 1,
    type: payload.type || evt.metadata?.type || "note",
    role: payload.role || evt.metadata?.role || "system",
    content: hasContent ? String(payload.content ?? "") : evt.body || "",
    task_id: payload.task_id || "",
    tool_call_id: payload.tool_call_id || "",
    tool_name: payload.tool_name || "",
    tool_status: payload.tool_status || "",
    created_at: payload.created_at || evt.created_at,
    data: {},
  };
  Object.entries(payload).forEach(([key, value]) => {
    if (
      [
        "agent_id",
        "generation",
        "type",
        "role",
        "content",
        "task_id",
        "tool_call_id",
        "tool_name",
        "tool_status",
        "created_at",
      ].includes(key)
    ) {
      return;
    }
    entry.data[key] = value;
  });
  return entry.agent_id ? entry : null;
}

function upsertHistory(state, entry) {
  if (!entry || !entry.agent_id) return state;
  const histories = { ...(state.histories || {}) };
  const current = cloneHistory(histories[entry.agent_id]);
  current.agent_id = entry.agent_id;

  if (entry.generation > current.generation) {
    current.generation = entry.generation;
    current.entries = [entry];
  } else if (entry.generation === current.generation) {
    if (!current.entries.some((item) => item.id === entry.id)) {
      current.entries.push(entry);
      current.entries.sort((a, b) => new Date(a.created_at) - new Date(b.created_at));
      if (current.entries.length > 1600) {
        current.entries = current.entries.slice(current.entries.length - 1600);
      }
    }
  }
  histories[entry.agent_id] = current;

  const agentSet = new Set((state.agents || []).map((agent) => agent.id));
  if (!agentSet.has(entry.agent_id)) {
    return {
      ...state,
      histories,
      agents: [
        { id: entry.agent_id, status: "running", active_tasks: 0, updated_at: entry.created_at, generation: entry.generation },
        ...(state.agents || []),
      ],
    };
  }
  return { ...state, histories };
}

function normalizeState(data) {
  return {
    generated_at: data?.generated_at || null,
    agents: Array.isArray(data?.agents) ? data.agents : [],
    sessions: data?.sessions || {},
    histories: data?.histories || {},
    tasks: Array.isArray(data?.tasks) ? data.tasks : [],
    updates: data?.updates || {},
  };
}

function useRuntimeState() {
  const [state, setState] = useState(() => ({
    generated_at: null,
    agents: [],
    sessions: {},
    histories: {},
    tasks: [],
    updates: {},
  }));
  const [status, setStatus] = useState({ connected: false, error: "" });
  const refreshTimer = useRef(null);

  const refresh = useCallback(async () => {
    try {
      const data = await fetchJSON(`/api/state?tasks=250&updates=400&streams=${STREAM_LIMIT}&history=1200`);
      setState((prev) => ({ ...prev, ...normalizeState(data) }));
      setStatus((prev) => ({ ...prev, error: "" }));
    } catch (err) {
      setStatus((prev) => ({ ...prev, error: err.message || String(err) }));
    }
  }, []);

  const scheduleRefresh = useCallback(() => {
    if (refreshTimer.current) return;
    refreshTimer.current = setTimeout(() => {
      refreshTimer.current = null;
      refresh();
    }, 400);
  }, [refresh]);

  useEffect(() => {
    refresh();
    const src = new EventSource(`/api/streams/subscribe?streams=${STREAMS.join(",")}`);
    src.onopen = () => setStatus((prev) => ({ ...prev, connected: true }));
    src.onerror = () => setStatus((prev) => ({ ...prev, connected: false }));
    src.onmessage = (event) => {
      try {
        const evt = JSON.parse(event.data);
        if (!evt || typeof evt !== "object") return;
        if (evt.stream === "history") {
          const parsed = parseHistoryEvent(evt);
          if (!parsed) return;
          setState((prev) => upsertHistory(prev, parsed));
          return;
        }
        scheduleRefresh();
      } catch (_) {
        // ignore malformed stream payloads
      }
    };
    return () => {
      src.close();
    };
  }, [refresh, scheduleRefresh]);

  return { state, status, refresh };
}

const sanitizer =
  DOMPurify && typeof DOMPurify.sanitize === "function"
    ? DOMPurify
    : DOMPurify && DOMPurify.default && typeof DOMPurify.default.sanitize === "function"
      ? DOMPurify.default
      : null;

function renderMarkdown(text) {
  if (!text) return "";
  const html = marked.parse(text, { gfm: true, breaks: true, mangle: false, headerIds: false });
  return sanitizer ? sanitizer.sanitize(html) : html;
}

function Markdown({ text }) {
  if (!text || text.trim() === "") {
    return React.createElement("div", { className: "muted" }, "Empty");
  }
  return React.createElement("div", {
    className: "markdown",
    dangerouslySetInnerHTML: { __html: renderMarkdown(text) },
  });
}

function StatusBadge({ status }) {
  const normalized = normalizeStatus(status);
  return React.createElement("span", { className: `status-pill status-${normalized}` }, normalized || "idle");
}

function toJSON(value) {
  if (value === null || value === undefined) return "";
  try {
    return JSON.stringify(value, null, 2);
  } catch (_) {
    return String(value);
  }
}

function isPrimitive(value) {
  return value === null || ["string", "number", "boolean"].includes(typeof value);
}

function renderInlineObject(obj, className = "inline-fields") {
  if (!obj || typeof obj !== "object" || Array.isArray(obj)) return null;
  const entries = Object.entries(obj).filter(([, value]) => value !== undefined);
  if (entries.length === 0) return null;
  const allSimple = entries.every(([, value]) => isPrimitive(value) && String(value).length <= 120);
  if (!allSimple || entries.length > 6) return null;
  return React.createElement(
    "dl",
    { className },
    entries.flatMap(([key, value]) => [
      React.createElement("dt", { key: `${className}-k-${key}` }, key),
      React.createElement("dd", { key: `${className}-v-${key}` }, String(value)),
    ]),
  );
}

function renderMaybeCollapsedJSON(title, value, options = {}) {
  if (value === null || value === undefined) return null;
  const collapseAt = typeof options.collapseAt === "number" ? options.collapseAt : 420;
  const json = toJSON(value);
  if (json.trim() === "") return null;
  const content = React.createElement("pre", { className: "json" }, json);
  if (json.length <= collapseAt) {
    return React.createElement(
      React.Fragment,
      null,
      title ? React.createElement("div", { className: "history-title compact" }, title) : null,
      content,
    );
  }
  return React.createElement(
    "details",
    null,
    React.createElement("summary", null, title || "Details"),
    content,
  );
}

function JsonBlock({ value }) {
  if (value === null || value === undefined) return null;
  return React.createElement("pre", { className: "json" }, toJSON(value));
}

function CodeBlock({ code, language = "typescript", className = "code-block" }) {
  const lang = String(language || "text").toLowerCase();
  const prismLanguage = lang === "xml" ? "markup" : lang;
  const grammar =
    Prism.languages[prismLanguage] ||
    Prism.languages.typescript ||
    Prism.languages.javascript ||
    Prism.languages.clike;
  const raw = String(code || "");
  if (!grammar) {
    return React.createElement(
      "pre",
      { className: `${className} language-${prismLanguage}` },
      React.createElement("code", null, raw),
    );
  }
  const html = Prism.highlight(raw, grammar, prismLanguage);
  return React.createElement(
    "pre",
    { className: `${className} language-${prismLanguage}` },
    React.createElement("code", { dangerouslySetInnerHTML: { __html: html } }),
  );
}

function TextBlock({ text, className = "text-block" }) {
  return React.createElement("pre", { className }, String(text || ""));
}

function isRuntimeInputEnvelope(text) {
  if (typeof text !== "string") return false;
  const value = text.trimStart();
  return value.startsWith("<user_turn") && value.includes("<system_updates");
}

function simplifyToolResult(result) {
  if (!result || typeof result !== "object" || !Array.isArray(result.content)) {
    return result;
  }
  const parsed = result.content.map((item) => {
    if (!item || typeof item !== "object") return item;
    if (item.type === "json" && typeof item.data === "string") {
      const maybe = parseJSONSafe(item.data);
      return maybe.ok ? maybe.value : item.data;
    }
    if (item.type === "text" && typeof item.text === "string") {
      return item.text;
    }
    return item;
  });
  if (parsed.length === 1 && (!result.error || result.error === "")) {
    return parsed[0];
  }
  const out = {};
  if (result.label && result.label !== "Success") out.label = result.label;
  if (result.error) out.error = result.error;
  out.content = parsed;
  return out;
}

function statusRank(status) {
  const normalized = normalizeStatus(status);
  const rank = {
    failed: 0,
    error: 0,
    cancelled: 1,
    done: 2,
    completed: 2,
    running: 3,
    streaming: 4,
    start: 5,
    queued: 6,
  };
  return rank[normalized] ?? 10;
}

function chooseStatus(current, next) {
  if (!next) return current || "";
  if (!current) return next;
  return statusRank(next) <= statusRank(current) ? next : current;
}

function extractResultError(result) {
  if (!result || typeof result !== "object") return "";
  if (typeof result.error === "string" && result.error.trim() !== "") return result.error.trim();
  return "";
}

function isToolEvent(entry) {
  return TOOL_EVENT_TYPES.has(entry?.type);
}

function buildDisplayEntries(entries) {
  if (!Array.isArray(entries) || entries.length === 0) return [];
  const ordered = [...entries].sort((a, b) => new Date(a.created_at) - new Date(b.created_at));
  const out = [];
  const toolByCallID = new Map();

  for (const entry of ordered) {
    if (entry.type === "context_event") {
      continue;
    }

    if (isToolEvent(entry) && entry.tool_call_id) {
      const callID = entry.tool_call_id;
      let group = toolByCallID.get(callID);
      if (!group) {
        group = {
          id: `tool-${callID}`,
          type: "tool_call_group",
          role: "tool",
          task_id: entry.task_id || "",
          tool_call_id: callID,
          tool_name: entry.tool_name || "",
          tool_status: entry.tool_status || (entry.type === "tool_call" ? "start" : ""),
          created_at: entry.created_at,
          updated_at: entry.created_at,
          args_raw: "",
          args: undefined,
          args_parse_error: "",
          result: undefined,
          result_error: "",
          metadata: undefined,
          events: [],
        };
        toolByCallID.set(callID, group);
        out.push(group);
      }

      group.updated_at = entry.created_at || group.updated_at;
      if (entry.tool_name) group.tool_name = entry.tool_name;
      if (entry.tool_status) group.tool_status = chooseStatus(group.tool_status, entry.tool_status);
      if (entry.type === "tool_result" && !group.tool_status) {
        group.tool_status = "done";
      }

      const data = entry.data || {};
      if (typeof data.delta === "string" && data.delta !== "") {
        group.args_raw += data.delta;
      }
      if (typeof data.args_raw === "string" && data.args_raw.trim() !== "") {
        group.args_raw = data.args_raw;
      }
      if (data.args !== undefined) {
        group.args = data.args;
      } else if (group.args === undefined && group.args_raw.trim() !== "") {
        const parsed = parseJSONSafe(group.args_raw);
        if (parsed.ok) {
          group.args = parsed.value;
          group.args_parse_error = "";
        } else {
          group.args_parse_error = parsed.error;
        }
      }
      if (data.result !== undefined) {
        group.result = simplifyToolResult(data.result);
      }
      if (data.metadata !== undefined) {
        group.metadata = data.metadata;
      }
      const resultError = extractResultError(group.result);
      if (resultError !== "") {
        group.result_error = resultError;
        group.tool_status = "failed";
      }
      group.events.push({
        type: entry.type,
        status: entry.tool_status || "",
        at: entry.created_at,
      });
      continue;
    }

    if (entry.type === "reasoning") {
      const last = out.length > 0 ? out[out.length - 1] : null;
      if (last && last.type === "reasoning_group") {
        if (entry.content) {
          last.content += entry.content;
          last.parts += 1;
        }
        if (typeof entry.data?.summary === "string" && entry.data.summary.trim() !== "") {
          last.summary = entry.data.summary.trim();
        }
        last.updated_at = entry.created_at || last.updated_at;
        if (entry.data?.reasoning_id) {
          last.reasoning_id = String(entry.data.reasoning_id);
        }
        continue;
      }

      const group = {
        id: `reasoning-${entry.id}`,
        type: "reasoning_group",
        role: "assistant",
        created_at: entry.created_at,
        updated_at: entry.created_at,
        reasoning_id: entry.data?.reasoning_id || "",
        content: entry.content || "",
        summary: typeof entry.data?.summary === "string" ? entry.data.summary.trim() : "",
        parts: entry.content ? 1 : 0,
      };
      out.push(group);
      continue;
    }

    out.push(entry);
  }

  return out;
}

function renderToolArgs(toolEntry) {
  const args = toolEntry.args;
  const raw = String(toolEntry.args_raw || "");
  if (toolEntry.tool_name === "exec" && args && typeof args === "object" && typeof args.code === "string") {
    const extra = { ...args };
    delete extra.code;
    return React.createElement(
      React.Fragment,
      null,
      React.createElement("div", { className: "history-title compact" }, "Arguments"),
      React.createElement(CodeBlock, { code: args.code }),
      renderInlineObject(extra) || (Object.keys(extra).length > 0 ? React.createElement(JsonBlock, { value: extra }) : null),
    );
  }

  if (toolEntry.tool_name === "await_task" && args && typeof args === "object") {
    return React.createElement(
      React.Fragment,
      null,
      React.createElement("div", { className: "history-title compact" }, "Arguments"),
      renderInlineObject(args) || React.createElement(JsonBlock, { value: args }),
    );
  }

  if (args !== undefined) {
    return React.createElement(
      React.Fragment,
      null,
      React.createElement("div", { className: "history-title compact" }, "Arguments"),
      renderInlineObject(args) || React.createElement(JsonBlock, { value: args }),
    );
  }

  if (raw.trim() !== "") {
    return React.createElement(
      React.Fragment,
      null,
      React.createElement("div", { className: "history-title compact" }, "Arguments (raw stream)"),
      React.createElement("pre", { className: "json" }, raw),
      toolEntry.args_parse_error
        ? React.createElement("div", { className: "muted" }, `parse error: ${toolEntry.args_parse_error}`)
        : null,
    );
  }
  return null;
}

function EntryCard({ entry }) {
  const when = formatDateTime(entry.created_at);
  const baseMeta = `${entry.role} · ${entry.type} · ${when}`;

  if (entry.type === "system_prompt") {
    return React.createElement(
      "details",
      { className: "history-card history-system" },
      React.createElement("summary", null, baseMeta),
      React.createElement(Markdown, { text: entry.content || "" }),
    );
  }

  if (entry.type === "tools_config") {
    const tools = Array.isArray(entry.data?.tools) ? entry.data.tools : [];
    return React.createElement(
      "div",
      { className: "history-card history-system" },
      React.createElement("div", { className: "history-meta" }, baseMeta),
      React.createElement("div", { className: "history-title" }, "Tools Configuration"),
      tools.length > 0
        ? React.createElement(
            "div",
            { className: "tool-list" },
            tools.map((tool) => React.createElement("span", { className: "tool-chip", key: tool }, tool)),
          )
        : React.createElement("div", { className: "muted" }, "No tools configured"),
    );
  }

  if (entry.type === "user_message") {
    if (isRuntimeInputEnvelope(entry.content || "")) {
      return React.createElement(
        "div",
        { className: "history-card history-user" },
        React.createElement("div", { className: "history-meta" }, baseMeta),
        React.createElement("div", { className: "history-title compact" }, "Input Envelope (XML sent to model)"),
        React.createElement(CodeBlock, { code: entry.content || "", language: "xml", className: "xml-block" }),
      );
    }
    return React.createElement(
      "div",
      { className: "history-card history-user" },
      React.createElement("div", { className: "history-meta" }, baseMeta),
      React.createElement(Markdown, { text: entry.content || "" }),
    );
  }

  if (entry.type === "llm_input") {
    const looksXML = isRuntimeInputEnvelope(entry.content || "");
    return React.createElement(
      "div",
      { className: "history-card history-system-update" },
      React.createElement("div", { className: "history-meta" }, baseMeta),
      React.createElement("div", { className: "history-title compact" }, "LLM Input"),
      looksXML
        ? React.createElement(CodeBlock, { code: entry.content || "", language: "xml", className: "xml-block" })
        : React.createElement(TextBlock, { text: entry.content || "" }),
    );
  }

  if (entry.type === "assistant_message") {
    const turn = Number(entry.data?.turn || 0);
    const partial = Boolean(entry.data?.partial);
    let metaLabel = baseMeta;
    if (turn > 0) {
      metaLabel = `assistant · turn ${turn} · ${when}`;
    }
    if (partial) {
      metaLabel += " · partial";
    }
    return React.createElement(
      "div",
      { className: "history-card history-assistant" },
      React.createElement("div", { className: "history-meta" }, metaLabel),
      React.createElement(Markdown, { text: entry.content || "" }),
    );
  }

  if (entry.type === "reasoning_group") {
    return React.createElement(
      "div",
      { className: "history-card history-reasoning history-compact" },
      React.createElement("div", { className: "history-meta" }, `assistant · reasoning · ${formatDateTime(entry.updated_at || entry.created_at)}`),
      entry.summary ? React.createElement("div", { className: "muted" }, `summary: ${entry.summary}`) : null,
      React.createElement(TextBlock, { text: entry.content || "" }),
    );
  }

  if (entry.type === "tool_call_group") {
    return React.createElement(
      "div",
      { className: "history-card history-tool history-compact" },
      React.createElement("div", { className: "history-meta" }, `tool · ${formatDateTime(entry.created_at)}`),
      React.createElement(
        "div",
        { className: "tool-head" },
        React.createElement("span", { className: "history-title compact" }, entry.tool_name || "tool"),
        React.createElement(StatusBadge, { status: entry.tool_status || "running" }),
      ),
      entry.tool_call_id ? React.createElement("div", { className: "muted mono" }, `call: ${entry.tool_call_id}`) : null,
      renderToolArgs(entry),
      renderMaybeCollapsedJSON("Result", entry.result, { collapseAt: 600 }),
      entry.result_error ? React.createElement("div", { className: "error" }, entry.result_error) : null,
      renderMaybeCollapsedJSON("Metadata", entry.metadata, { collapseAt: 380 }),
    );
  }

  if (entry.type === "context_event") {
    return React.createElement(
      "div",
      { className: "history-card history-system-update history-compact" },
      React.createElement("div", { className: "history-meta" }, baseMeta),
      React.createElement(
        "div",
        { className: "history-title compact" },
        `${entry.data?.stream || "event"}${entry.data?.priority ? ` · ${entry.data.priority}` : ""}`,
      ),
      entry.content ? React.createElement(Markdown, { text: entry.content }) : null,
      renderMaybeCollapsedJSON("Event payload", entry.data, { collapseAt: 320 }),
    );
  }

  if (entry.type === "system_update") {
    return React.createElement(
      "div",
      { className: "history-card history-system-update history-compact" },
      React.createElement("div", { className: "history-meta" }, baseMeta),
      React.createElement("div", { className: "history-title compact" }, entry.content || "system update"),
      renderMaybeCollapsedJSON("", entry.data, { collapseAt: 340 }),
    );
  }

  if (entry.type === "context_compaction") {
    return React.createElement(
      "div",
      { className: "history-card history-compaction history-compact" },
      React.createElement("div", { className: "history-meta" }, baseMeta),
      React.createElement("div", { className: "history-title compact" }, entry.content || "context compacted"),
      renderMaybeCollapsedJSON("", entry.data, { collapseAt: 420 }),
    );
  }

  return React.createElement(
    "div",
    { className: "history-card history-system" },
    React.createElement("div", { className: "history-meta" }, baseMeta),
    entry.content ? React.createElement(Markdown, { text: entry.content }) : null,
    entry.data && Object.keys(entry.data).length > 0
      ? React.createElement("pre", { className: "json" }, toJSON(entry.data))
      : null,
  );
}

function App() {
  const { state, status, refresh } = useRuntimeState();
  const [selectedAgent, setSelectedAgent] = useState("");
  const [message, setMessage] = useState("");
  const [sendStatus, setSendStatus] = useState("");
  const [compactStatus, setCompactStatus] = useState("");
  const timelineRef = useRef(null);

  const agents = useMemo(() => {
    if (Array.isArray(state.agents) && state.agents.length > 0) return state.agents;
    const ids = new Set();
    Object.keys(state.histories || {}).forEach((id) => ids.add(id));
    Object.keys(state.sessions || {}).forEach((id) => ids.add(id));
    return Array.from(ids)
      .filter((id) => typeof id === "string" && id.trim() !== "")
      .map((id) => ({
      id,
      status: "idle",
      active_tasks: 0,
      updated_at: "",
      generation: state.histories?.[id]?.generation || 1,
      }));
  }, [state.agents, state.histories, state.sessions]);

  useEffect(() => {
    if (!agents.some((agent) => agent.id === selectedAgent)) {
      setSelectedAgent(agents[0]?.id || "");
    }
  }, [agents, selectedAgent]);

  const selectedHistory = state.histories?.[selectedAgent] || { generation: 1, entries: [] };
  const selectedSession = state.sessions?.[selectedAgent] || null;
  const selectedAgentState = agents.find((agent) => agent.id === selectedAgent) || {
    id: selectedAgent,
    status: "idle",
    active_tasks: 0,
    generation: selectedHistory.generation || 1,
  };
  const timelineEntries = useMemo(
    () => buildDisplayEntries(selectedHistory.entries || []),
    [selectedHistory.entries],
  );

  useEffect(() => {
    const node = timelineRef.current;
    if (!node) return;
    node.scrollTop = node.scrollHeight;
  }, [selectedAgent, timelineEntries.length]);

  const handleSend = useCallback(async () => {
    const trimmed = message.trim();
    if (!trimmed) return;
    setSendStatus("sending");
    try {
      const endpoint = selectedAgent
        ? `${API_BASE}/api/agents/${encodeURIComponent(selectedAgent)}/run`
        : `${API_BASE}/api/agents/run`;
      const res = await fetch(endpoint, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ message: trimmed, source: "external", priority: "wake" }),
      });
      if (!res.ok) {
        const text = await res.text();
        setSendStatus(`error ${res.status}: ${text}`);
        return;
      }
      const data = await res.json().catch(() => null);
      if (data && typeof data.agent_id === "string" && data.agent_id.trim() !== "") {
        setSelectedAgent(data.agent_id.trim());
      }
      setMessage("");
      setSendStatus("sent");
      refresh();
    } catch (err) {
      setSendStatus(`error: ${err.message || err}`);
    }
  }, [message, refresh, selectedAgent]);

  const handleCompact = useCallback(async () => {
    if (!selectedAgent) {
      setCompactStatus("no agent selected");
      return;
    }
    setCompactStatus("compacting");
    try {
      const res = await fetch(`${API_BASE}/api/agents/${encodeURIComponent(selectedAgent)}/compact`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ reason: "manual compact from UI" }),
      });
      if (!res.ok) {
        const text = await res.text();
        setCompactStatus(`error ${res.status}: ${text}`);
        return;
      }
      setCompactStatus("compacted");
      refresh();
    } catch (err) {
      setCompactStatus(`error: ${err.message || err}`);
    }
  }, [refresh, selectedAgent]);

  return React.createElement(
    "div",
    { className: "app" },
    React.createElement(
      "header",
      { className: "topbar" },
      React.createElement(
        "div",
        null,
        React.createElement("h1", null, "go-agents"),
        React.createElement("div", { className: "subtitle" }, "Agent History Console"),
      ),
      React.createElement(
        "div",
        { className: "status-bar" },
        React.createElement("span", { className: `dot ${status.connected ? "online" : ""}` }),
        React.createElement("span", null, status.connected ? "Live" : "Disconnected"),
        React.createElement("span", { className: "muted" }, `snapshot ${formatTime(state.generated_at)}`),
        status.error ? React.createElement("span", { className: "error" }, status.error) : null,
      ),
    ),
    React.createElement(
      "main",
      { className: "layout" },
      React.createElement(
        "aside",
        { className: "sidebar panel" },
        React.createElement("h2", null, "Agents"),
        React.createElement(
          "div",
          { className: "agent-list" },
          agents.map((agent) =>
            React.createElement(
              "button",
              {
                key: agent.id,
                className: `agent-row ${agent.id === selectedAgent ? "active" : ""}`,
                onClick: () => setSelectedAgent(agent.id),
              },
              React.createElement(
                "div",
                { className: "agent-main" },
                React.createElement("div", { className: "agent-id" }, agent.id),
                React.createElement(
                  "div",
                  { className: "agent-meta" },
                  `active ${agent.active_tasks || 0} · gen ${agent.generation || 1} · ${formatTime(agent.updated_at)}`,
                ),
              ),
              React.createElement(StatusBadge, { status: agent.status || "idle" }),
            ),
          ),
          agents.length <= 1
            ? React.createElement("div", { className: "muted" }, "No agents yet. Send a message to create one.")
            : null,
        ),
      ),
      React.createElement(
        "section",
        { className: "panel detail" },
        React.createElement(
          "div",
          { className: "detail-head" },
          React.createElement("h2", null, selectedAgent || "No agent selected"),
          React.createElement(
            "div",
            { className: "detail-meta" },
            React.createElement(StatusBadge, { status: selectedAgentState.status || "idle" }),
            React.createElement("span", { className: "muted" }, `generation ${selectedHistory.generation || 1}`),
            selectedSession?.updated_at
              ? React.createElement("span", { className: "muted" }, `updated ${formatDateTime(selectedSession.updated_at)}`)
              : null,
          ),
        ),
        React.createElement(
          "div",
          { className: "timeline", ref: timelineRef },
          !timelineEntries || timelineEntries.length === 0
            ? React.createElement("div", { className: "muted" }, "No history yet.")
            : timelineEntries.map((entry) => React.createElement(EntryCard, { key: entry.id || `${entry.type}-${entry.created_at}`, entry })),
        ),
        React.createElement(
          "div",
          { className: "composer" },
          React.createElement("textarea", {
            value: message,
            onChange: (event) => setMessage(event.target.value),
            placeholder: selectedAgent ? `Message ${selectedAgent}...` : "Message a new agent...",
          }),
          React.createElement(
            "div",
            { className: "composer-actions" },
            React.createElement(
              "button",
              { onClick: handleSend },
              "Send",
            ),
            React.createElement(
              "button",
              { className: "danger", onClick: handleCompact, disabled: !selectedAgent },
              "Compact Context",
            ),
            sendStatus ? React.createElement("span", { className: "muted" }, sendStatus) : null,
            compactStatus ? React.createElement("span", { className: "muted" }, compactStatus) : null,
          ),
        ),
      ),
    ),
  );
}

const root = createRoot(document.getElementById("root"));
root.render(React.createElement(App));
