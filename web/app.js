import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import { marked } from "marked";
import DOMPurify from "dompurify";

const API_BASE = "";
const AGENT_ID = "operator";
const CLIENT_ID = "human";
const STREAMS = ["task_output", "errors", "signals", "external", "messages"];
const STREAM_LIMIT = 200;

const ASCII_FONT = {
  A: [" ███ ", "█   █", "█████", "█   █", "█   █"],
  B: ["████ ", "█   █", "████ ", "█   █", "████ "],
  C: [" ████", "█    ", "█    ", "█    ", " ████"],
  D: ["████ ", "█   █", "█   █", "█   █", "████ "],
  E: ["█████", "█    ", "████ ", "█    ", "█████"],
  F: ["█████", "█    ", "████ ", "█    ", "█    "],
  G: [" ████", "█    ", "█  ██", "█   █", " ████"],
  H: ["█   █", "█   █", "█████", "█   █", "█   █"],
  I: ["█████", "  █  ", "  █  ", "  █  ", "█████"],
  J: ["  ███", "   █ ", "   █ ", "█  █ ", " ██  "],
  K: ["█   █", "█  █ ", "███  ", "█  █ ", "█   █"],
  L: ["█    ", "█    ", "█    ", "█    ", "█████"],
  M: ["█   █", "██ ██", "█ █ █", "█   █", "█   █"],
  N: ["█   █", "██  █", "█ █ █", "█  ██", "█   █"],
  O: [" ███ ", "█   █", "█   █", "█   █", " ███ "],
  P: ["████ ", "█   █", "████ ", "█    ", "█    "],
  Q: [" ███ ", "█   █", "█   █", "█  ██", " ████"],
  R: ["████ ", "█   █", "████ ", "█  █ ", "█   █"],
  S: [" ████", "█    ", " ███ ", "    █", "████ "],
  T: ["█████", "  █  ", "  █  ", "  █  ", "  █  "],
  U: ["█   █", "█   █", "█   █", "█   █", " ███ "],
  V: ["█   █", "█   █", "█   █", " █ █ ", "  █  "],
  W: ["█   █", "█   █", "█ █ █", "██ ██", "█   █"],
  X: ["█   █", " █ █ ", "  █  ", " █ █ ", "█   █"],
  Y: ["█   █", " █ █ ", "  █  ", "  █  ", "  █  "],
  Z: ["█████", "   █ ", "  █  ", " █   ", "█████"],
  "-": ["     ", "     ", "█████", "     ", "     "],
};

function renderAsciiText(text) {
  const rows = ["", "", "", "", ""];
  const chars = text.toUpperCase().split("");
  chars.forEach((ch, idx) => {
    const glyph = ASCII_FONT[ch] || ASCII_FONT["-"];
    for (let i = 0; i < rows.length; i += 1) {
      rows[i] += glyph[i] + (idx === chars.length - 1 ? "" : " ");
    }
  });
  return rows;
}

function frameAscii(lines) {
  const width = Math.max(...lines.map((line) => line.length));
  const top = `╔${"═".repeat(width + 2)}╗`;
  const bottom = `╚${"═".repeat(width + 2)}╝`;
  const framed = lines.map((line) => `║ ${line.padEnd(width, " ")} ║`);
  return [top, ...framed, bottom].join("\n");
}

function formatTime(ts) {
  if (!ts) return "-";
  try {
    return new Date(ts).toLocaleTimeString();
  } catch (_) {
    return "-";
  }
}

function formatDateTime(ts) {
  if (!ts) return "-";
  try {
    return new Date(ts).toLocaleString();
  } catch (_) {
    return "-";
  }
}

async function fetchJSON(path) {
  const res = await fetch(`${API_BASE}${path}`);
  if (!res.ok) {
    throw new Error(`Request failed: ${res.status}`);
  }
  return res.json();
}

function limitList(items, limit) {
  if (!items) return [];
  if (items.length <= limit) return items;
  return items.slice(items.length - limit);
}

function buildTaskIndex(tasks) {
  const byId = {};
  tasks.forEach((task) => {
    byId[task.id] = task;
  });
  return byId;
}

function collectAgentIDs(sessions, tasks) {
  const ids = new Set(Object.keys(sessions || {}));
  (tasks || []).forEach((task) => {
    if (task.owner) ids.add(task.owner);
    const meta = task.metadata || {};
    if (meta.agent_id) ids.add(meta.agent_id);
    if (meta.notify_target) ids.add(meta.notify_target);
  });
  if (ids.size === 0) ids.add(AGENT_ID);
  return Array.from(ids);
}

function tasksForAgent(tasks, agentID) {
  return (tasks || []).filter((task) => {
    if (task.owner === agentID) return true;
    const meta = task.metadata || {};
    if (meta.agent_id === agentID) return true;
    if (meta.notify_target === agentID) return true;
    return false;
  });
}

function lastUpdate(updates) {
  if (!updates || updates.length === 0) return null;
  return updates[updates.length - 1];
}

function describeLLMState(task, updates) {
  if (!task) return { label: "idle", status: "idle", detail: "no active LLM task" };
  if (task.status === "queued") {
    return { label: "queued", status: "queued", detail: "awaiting slot" };
  }
  if (task.status === "failed") {
    return { label: "failed", status: "failed", detail: "check error" };
  }
  if (task.status === "cancelled") {
    return { label: "cancelled", status: "cancelled", detail: "stopped" };
  }
  if (task.status === "completed") {
    return { label: "completed", status: "completed", detail: "last run finished" };
  }

  const latest = lastUpdate(updates);
  if (!latest) {
    return { label: "running", status: "running", detail: "awaiting updates" };
  }
  const kind = latest.kind;
  const payload = latest.payload || {};
  if (kind === "llm_thinking") {
    return { label: "thinking", status: "thinking", detail: "reasoning" };
  }
  if (kind === "llm_text") {
    return { label: "responding", status: "responding", detail: "streaming output" };
  }
  if (kind === "llm_tool_start" || kind === "llm_tool_delta" || kind === "llm_tool_status") {
    const toolName = payload.tool_name || payload.tool_label || "tool";
    const toolStatus = payload.status ? ` · ${payload.status}` : "";
    return { label: "waiting tool", status: "tool", detail: `${toolName}${toolStatus}` };
  }
  if (kind === "llm_tool_done") {
    const toolName = payload.tool_name || payload.tool_label || "tool";
    return { label: "processing", status: "running", detail: `finished ${toolName}` };
  }
  if (kind === "await_timeout") {
    return { label: "sleeping", status: "sleeping", detail: "awaiting events" };
  }
  if (kind === "input") {
    return { label: "running", status: "running", detail: "received input" };
  }
  return { label: "running", status: "running", detail: kind };
}

function deriveAgents(sessions, tasks, updatesByTask) {
  const agentIDs = collectAgentIDs(sessions, tasks);
  return agentIDs
    .map((agentID) => {
      const session = sessions?.[agentID] || null;
      const ownedTasks = tasksForAgent(tasks, agentID);
      const activeTasks = ownedTasks.filter((task) => task.status === "running" || task.status === "queued");
      const llmTask = session?.llm_task_id
        ? tasks.find((task) => task.id === session.llm_task_id)
        : ownedTasks
            .filter((task) => task.type === "llm")
            .sort((a, b) => new Date(b.updated_at) - new Date(a.updated_at))[0];
      const llmUpdates = llmTask ? updatesByTask?.[llmTask.id] || [] : [];
      let state = describeLLMState(llmTask, llmUpdates);
      if (!llmTask && session?.llm_task_id) {
        state = { label: "running", status: "running", detail: "llm task pending" };
      }
      if (!llmTask && activeTasks.length > 0) {
        state = { label: "running", status: "running", detail: `${activeTasks.length} active tasks` };
      }
      if (!llmTask && activeTasks.length === 0) {
        state = { label: "idle", status: "idle", detail: "no active tasks" };
      }
      return {
        id: agentID,
        session,
        state,
        activeCount: activeTasks.length,
        totalCount: ownedTasks.length,
        llmTask,
      };
    })
    .sort((a, b) => {
      const aTime = a.session?.updated_at || a.llmTask?.updated_at || "";
      const bTime = b.session?.updated_at || b.llmTask?.updated_at || "";
      return new Date(bTime) - new Date(aTime);
    });
}

function normalizeState(data) {
  const tasks = data?.tasks || [];
  const updates = data?.updates || {};
  const streams = data?.streams || {};
  const sessions = data?.sessions || {};
  return {
    generatedAt: data?.generated_at || null,
    tasksById: buildTaskIndex(tasks),
    updatesByTask: updates,
    streams: streams,
    sessions: sessions,
  };
}

function applyTaskUpdate(state, evt) {
  const taskId = evt?.metadata?.task_id;
  if (!taskId) return state;
  const kind = evt?.metadata?.task_kind || evt.body || "update";
  const update = {
    id: evt.id,
    task_id: taskId,
    kind,
    payload: evt.payload || {},
    created_at: evt.created_at,
  };

  const updatesByTask = { ...state.updatesByTask };
  const list = updatesByTask[taskId] ? [...updatesByTask[taskId], update] : [update];
  updatesByTask[taskId] = list;

  const tasksById = { ...state.tasksById };
  const existing = tasksById[taskId];
  if (!existing) {
    return { ...state, updatesByTask, needsRefresh: true };
  }

  let next = { ...existing, updated_at: evt.created_at || existing.updated_at };
  if (["started", "completed", "failed", "cancelled", "killed"].includes(kind)) {
    const statusMap = {
      started: "running",
      completed: "completed",
      failed: "failed",
      cancelled: "cancelled",
      killed: "cancelled",
    };
    const settled = ["completed", "failed", "cancelled"].includes(existing.status);
    const isCancel = kind === "cancelled" || kind === "killed";
    if (!(settled && isCancel)) {
      next.status = statusMap[kind] || next.status;
    }
    if (kind === "completed") {
      next.result = update.payload || next.result;
    }
    if (kind === "failed") {
      next.error = update.payload?.error || next.error;
    }
  }
  tasksById[taskId] = next;

  return { ...state, updatesByTask, tasksById };
}

function applyStreamEvent(state, evt) {
  const streams = { ...state.streams };
  const list = streams[evt.stream] ? [...streams[evt.stream], evt] : [evt];
  streams[evt.stream] = limitList(list, STREAM_LIMIT);
  return { ...state, streams };
}

function useSyncState() {
  const [state, setState] = useState(() => ({
    generatedAt: null,
    tasksById: {},
    updatesByTask: {},
    streams: {},
    sessions: {},
  }));
  const [status, setStatus] = useState({ connected: false, lastError: "" });
  const refreshTimer = useRef(null);

  const refreshState = useCallback(async () => {
    try {
      const data = await fetchJSON(`/api/state?tasks=200&updates=200&streams=${STREAM_LIMIT}`);
      setState((prev) => ({ ...prev, ...normalizeState(data), needsRefresh: false }));
      setStatus((prev) => ({ ...prev, lastError: "" }));
    } catch (err) {
      setStatus((prev) => ({ ...prev, lastError: err.message || String(err) }));
    }
  }, []);

  const scheduleRefresh = useCallback(() => {
    if (refreshTimer.current) return;
    refreshTimer.current = setTimeout(() => {
      refreshTimer.current = null;
      refreshState();
    }, 600);
  }, [refreshState]);

  useEffect(() => {
    refreshState();
    const source = new EventSource(`/api/streams/subscribe?streams=${STREAMS.join(",")}`);

    source.onopen = () => setStatus((prev) => ({ ...prev, connected: true }));
    source.onerror = () => setStatus((prev) => ({ ...prev, connected: false }));

    source.onmessage = (event) => {
      try {
        const evt = JSON.parse(event.data);
        setState((prev) => {
          let next = prev;
          if (evt.stream === "task_output") {
            next = applyTaskUpdate(next, evt);
            if (next.needsRefresh) {
              scheduleRefresh();
            }
          }
          next = applyStreamEvent(next, evt);
          return next;
        });
      } catch (_) {
        // ignore
      }
    };

    return () => {
      source.close();
    };
  }, [refreshState, scheduleRefresh]);

  return { state, status, refreshState };
}

function buildTaskTree(tasksById) {
  const childrenByParent = {};
  const roots = [];
  Object.values(tasksById).forEach((task) => {
    const parent = task.parent_id || task.metadata?.parent_id || "";
    if (parent) {
      if (!childrenByParent[parent]) childrenByParent[parent] = [];
      childrenByParent[parent].push(task);
    } else {
      roots.push(task);
    }
  });

  const sortedRoots = roots.sort((a, b) => new Date(b.updated_at) - new Date(a.updated_at));
  const list = [];

  function walk(node, depth) {
    list.push({ task: node, depth });
    const children = (childrenByParent[node.id] || []).sort(
      (a, b) => new Date(a.created_at) - new Date(b.created_at),
    );
    children.forEach((child) => walk(child, depth + 1));
  }

  sortedRoots.forEach((root) => walk(root, 0));
  return list;
}

function deriveLLMTask(tasks, selectedTaskId) {
  if (selectedTaskId) {
    const selected = tasks.find((task) => task.id === selectedTaskId);
    if (selected?.type === "llm") return selected;
    if (selected?.owner) {
      const candidate = tasks
        .filter((task) => task.type === "llm" && task.owner === selected.owner)
        .sort((a, b) => new Date(b.updated_at) - new Date(a.updated_at))[0];
      if (candidate) return candidate;
    }
  }
  return tasks
    .filter((task) => task.type === "llm")
    .sort((a, b) => new Date(b.updated_at) - new Date(a.updated_at))[0];
}

function toMarkdown(value) {
  if (value === null || value === undefined) return "";
  if (typeof value === "string") return value;
  try {
    return `\n\n\`\`\`json\n${JSON.stringify(value, null, 2)}\n\`\`\`\n`;
  } catch (_) {
    return String(value);
  }
}

function buildTrace(updates, promptText) {
  const items = [];
  if (promptText) {
    items.push({ id: "prompt", role: "system", text: promptText });
  }
  if (!updates || updates.length === 0) return items;

  const toolMap = new Map();
  let assistantItem = null;
  let thinkingItem = null;

  updates.forEach((update, index) => {
    const kind = update.kind;
    const payload = update.payload || {};
    if (kind === "input") {
      const message = payload.message || payload.text || "";
      if (message) {
        assistantItem = null;
        thinkingItem = null;
        items.push({ id: update.id || `input-${index}`, role: "user", text: message });
      }
      return;
    }

    if (kind === "llm_text") {
      if (!assistantItem) {
        assistantItem = { id: update.id || `llm-${index}`, role: "assistant", text: "" };
        items.push(assistantItem);
      }
      thinkingItem = null;
      assistantItem.text += payload.text || "";
      return;
    }

    if (kind === "llm_thinking") {
      const text = payload.summary || payload.text || "";
      if (thinkingItem) {
        thinkingItem.text += text ? `\n${text}` : "";
      } else {
        thinkingItem = {
          id: update.id || `thinking-${index}`,
          role: "thinking",
          text,
        };
        items.push(thinkingItem);
      }
      return;
    }

    if (kind === "llm_tool_start") {
      const toolItem = {
        id: payload.tool_call_id || update.id || `tool-${index}`,
        role: "tool",
        name: payload.tool_name || payload.tool_label || "tool",
        input: "",
        status: "start",
        result: null,
      };
      toolMap.set(toolItem.id, toolItem);
      items.push(toolItem);
      return;
    }

    if (kind === "llm_tool_delta") {
      const toolCallId = payload.tool_call_id;
      if (!toolCallId) return;
      let toolItem = toolMap.get(toolCallId);
      if (!toolItem) {
        toolItem = {
          id: toolCallId,
          role: "tool",
          name: "tool",
          input: "",
          status: "delta",
          result: null,
        };
        toolMap.set(toolCallId, toolItem);
        items.push(toolItem);
      }
      toolItem.input += payload.delta || "";
      toolItem.status = "streaming";
      return;
    }

    if (kind === "llm_tool_status") {
      const toolCallId = payload.tool_call_id;
      if (!toolCallId) return;
      const toolItem = toolMap.get(toolCallId);
      if (toolItem) {
        toolItem.status = payload.status || toolItem.status;
      }
      return;
    }

    if (kind === "llm_tool_done") {
      const toolCallId = payload.tool_call_id;
      if (!toolCallId) return;
      const toolItem = toolMap.get(toolCallId);
      if (toolItem) {
        toolItem.status = "done";
        toolItem.result = payload.result || payload.metadata || payload;
      }
      return;
    }
  });

  return items;
}

const markdownSanitizer =
  DOMPurify && typeof DOMPurify.sanitize === "function"
    ? DOMPurify
    : DOMPurify && DOMPurify.default && typeof DOMPurify.default.sanitize === "function"
      ? DOMPurify.default
      : null;

function renderMarkdown(text) {
  if (!text) return "";
  const html = marked.parse(text, {
    breaks: true,
    gfm: true,
    mangle: false,
    headerIds: false,
  });
  return markdownSanitizer ? markdownSanitizer.sanitize(html) : html;
}

function Markdown({ text }) {
  if (!text) {
    return React.createElement("div", { className: "markdown empty" }, "-");
  }
  const html = renderMarkdown(text);
  return React.createElement("div", { className: "markdown", dangerouslySetInnerHTML: { __html: html } });
}

function StatusBadge({ status, label }) {
  const cls = `status-pill status-${status || "unknown"}`;
  return React.createElement("span", { className: cls }, label || status || "unknown");
}

function escapeHtml(value) {
  return value
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

function ToolInput({ toolName, raw }) {
  if (!raw) return null;
  if (toolName && toolName.toLowerCase() === "exec") {
    try {
      const parsed = JSON.parse(raw);
      const code = typeof parsed.code === "string" ? parsed.code : "";
      const rest = { ...parsed };
      delete rest.code;
      return React.createElement(
        "div",
        { className: "tool-input-block" },
        code
          ? React.createElement(
              "pre",
              { className: "code-block language-ts" },
              React.createElement("code", { dangerouslySetInnerHTML: { __html: escapeHtml(code) } }),
            )
          : null,
        Object.keys(rest).length > 0
          ? React.createElement(Markdown, { text: `\n\n\`\`\`json\n${JSON.stringify(rest, null, 2)}\n\`\`\`\n` })
          : null,
      );
    } catch (_) {
      // fall through
    }
  }
  return React.createElement(Markdown, { text: raw });
}

function App() {
  const { state, status } = useSyncState();
  const [selectedTaskId, setSelectedTaskId] = useState(null);
  const [activeTab, setActiveTab] = useState("trace");
  const [activeStream, setActiveStream] = useState("messages");
  const [messageInput, setMessageInput] = useState("");
  const [sendStatus, setSendStatus] = useState(null);
  const traceRef = useRef(null);

  const tasks = useMemo(() => Object.values(state.tasksById || {}), [state.tasksById]);
  const taskTree = useMemo(() => buildTaskTree(state.tasksById || {}), [state.tasksById]);
  const activeTasks = useMemo(
    () => tasks.filter((task) => task.status === "running" || task.status === "queued"),
    [tasks],
  );
  const latestTask = useMemo(
    () => tasks.slice().sort((a, b) => new Date(b.updated_at) - new Date(a.updated_at))[0],
    [tasks],
  );
  const selectedTask = selectedTaskId
    ? state.tasksById[selectedTaskId]
    : latestTask || null;

  useEffect(() => {
    if (!selectedTaskId && latestTask) {
      setSelectedTaskId(latestTask.id);
    }
  }, [selectedTaskId, latestTask]);

  const llmTask = useMemo(() => deriveLLMTask(tasks, selectedTaskId), [tasks, selectedTaskId]);
  const llmUpdates = llmTask ? state.updatesByTask?.[llmTask.id] || [] : [];
  const prompt = state.sessions?.[AGENT_ID]?.prompt || "";
  const trace = useMemo(() => buildTrace(llmUpdates, prompt), [llmUpdates, prompt]);

  const streamItems = (state.streams?.[activeStream] || []).slice().sort((a, b) => new Date(a.created_at) - new Date(b.created_at));

  const chatMessages = (state.streams?.messages || []).slice().sort(
    (a, b) => new Date(a.created_at) - new Date(b.created_at),
  );

  const agents = useMemo(
    () => deriveAgents(state.sessions || {}, tasks, state.updatesByTask || {}),
    [state.sessions, tasks, state.updatesByTask],
  );

  const totalTasks = tasks.length;
  const runningTasks = tasks.filter((task) => task.status === "running").length;
  const failedTasks = tasks.filter((task) => task.status === "failed").length;

  const logo = useMemo(() => {
    const titleLines = renderAsciiText("GO-AGENTS");
    const subtitle = "REALTIME OPERATIONS CONSOLE";
    const paddedSubtitle = subtitle.padStart(Math.floor((titleLines[0].length + subtitle.length) / 2), " ");
    return frameAscii([...titleLines, paddedSubtitle]);
  }, []);

  const handleSend = useCallback(async () => {
    const message = messageInput.trim();
    if (!message) return;
    setSendStatus("sending");
    setMessageInput("");
    try {
      const res = await fetch(`${API_BASE}/api/agents/${encodeURIComponent(AGENT_ID)}/run`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ message, source: CLIENT_ID }),
      });
      if (!res.ok) {
        let detail = "";
        try {
          const payload = await res.json();
          detail = payload.error ? `: ${payload.error}` : "";
        } catch (_) {
          // ignore
        }
        setSendStatus(`error:${res.status}${detail}`);
        return;
      }
      setSendStatus("sent");
    } catch (err) {
      setSendStatus(`error:${err.message || err}`);
    }
  }, [messageInput]);

  useEffect(() => {
    if (activeTab !== "trace") return;
    const node = traceRef.current;
    if (!node) return;
    node.scrollTop = node.scrollHeight;
  }, [trace, activeTab]);

  return React.createElement(
    "div",
    { className: "app" },
    React.createElement(
      "header",
      { className: "topbar" },
      React.createElement("pre", { className: "ascii-logo", "aria-label": "Go-Agents" }, logo),
      React.createElement(
        "div",
        { className: "status" },
        React.createElement("span", { className: "status-label" }, "STATUS"),
        React.createElement("span", { className: `dot ${status.connected ? "online" : ""}` }),
        React.createElement("span", { className: "status-text" }, status.connected ? "Live" : "Disconnected"),
        status.lastError
          ? React.createElement("span", { className: "status-error" }, status.lastError)
          : null,
      ),
    ),
    React.createElement(
      "main",
      { className: "layout" },
      React.createElement(
        "aside",
        { className: "sidebar" },
        React.createElement(
          "section",
          { className: "panel" },
          React.createElement(
            "div",
            { className: "panel-header" },
            React.createElement("h2", null, "Overview"),
          ),
          React.createElement(
            "div",
            { className: "panel-body metrics" },
            React.createElement(
              "div",
              { className: "metric" },
              React.createElement("span", { className: "metric-label" }, "Tasks"),
              React.createElement("span", { className: "metric-value" }, totalTasks),
            ),
            React.createElement(
              "div",
              { className: "metric" },
              React.createElement("span", { className: "metric-label" }, "Running"),
              React.createElement("span", { className: "metric-value" }, runningTasks),
            ),
            React.createElement(
              "div",
              { className: "metric" },
              React.createElement("span", { className: "metric-label" }, "Failed"),
              React.createElement("span", { className: "metric-value" }, failedTasks),
            ),
            React.createElement(
              "div",
              { className: "metric" },
              React.createElement("span", { className: "metric-label" }, "Snapshot"),
              React.createElement("span", { className: "metric-value" }, formatTime(state.generatedAt)),
            ),
          ),
        ),
        React.createElement(
          "section",
          { className: "panel" },
          React.createElement(
            "div",
            { className: "panel-header" },
            React.createElement("h2", null, "Agents"),
          ),
          React.createElement(
            "div",
            { className: "panel-body agent-list" },
            agents.length === 0
              ? React.createElement("div", { className: "muted" }, "No agents yet.")
              : agents.map((agent) =>
                  React.createElement(
                    "div",
                    { key: agent.id, className: "agent-row" },
                    React.createElement(
                      "div",
                      { className: "agent-main" },
                      React.createElement(
                        "div",
                        { className: "agent-title" },
                        React.createElement("span", { className: "agent-id" }, agent.id),
                        React.createElement("span", { className: "agent-count" }, `${agent.activeCount} active`),
                      ),
                      React.createElement(
                        "div",
                        { className: "agent-detail" },
                        agent.state.detail,
                      ),
                    ),
                    React.createElement(StatusBadge, { status: agent.state.status, label: agent.state.label }),
                  ),
                ),
          ),
        ),
        React.createElement(
          "section",
          { className: "panel" },
          React.createElement(
            "div",
            { className: "panel-header" },
            React.createElement("h2", null, "Tasks"),
          ),
          React.createElement(
            "div",
            { className: "panel-body task-list" },
            taskTree.length === 0
              ? React.createElement("div", { className: "muted" }, "No tasks yet.")
              : React.createElement(
                  React.Fragment,
                  null,
                  React.createElement(
                    "div",
                    { className: "task-group" },
                    React.createElement(
                      "div",
                      { className: "task-group-title" },
                      `Active (${activeTasks.length})`,
                    ),
                    activeTasks.length === 0
                      ? React.createElement("div", { className: "muted" }, "No active tasks.")
                      : activeTasks
                          .slice()
                          .sort((a, b) => new Date(b.updated_at) - new Date(a.updated_at))
                          .map((task) =>
                            React.createElement(
                              "button",
                              {
                                key: `active-${task.id}`,
                                className: `task-row ${selectedTaskId === task.id ? "active" : ""}`,
                                onClick: () => setSelectedTaskId(task.id),
                              },
                              React.createElement("span", { className: "task-main" },
                                React.createElement("span", { className: "task-type" }, task.type),
                                React.createElement("span", { className: "task-id" }, task.id.slice(0, 8)),
                              ),
                              React.createElement(StatusBadge, { status: task.status }),
                            ),
                          ),
                  ),
                  React.createElement("div", { className: "task-group-title" }, "All"),
                  taskTree.map(({ task, depth }) =>
                    React.createElement(
                      "button",
                      {
                        key: task.id,
                        className: `task-row ${selectedTaskId === task.id ? "active" : ""}`,
                        onClick: () => setSelectedTaskId(task.id),
                      },
                      React.createElement(
                        "span",
                        { className: "task-indent", style: { width: `${depth * 12}px` } },
                        depth > 0 ? "└" : "",
                      ),
                      React.createElement(
                        "span",
                        { className: "task-main" },
                        React.createElement("span", { className: "task-type" }, task.type),
                        React.createElement("span", { className: "task-id" }, task.id.slice(0, 8)),
                      ),
                      React.createElement(StatusBadge, { status: task.status }),
                    ),
                  ),
                ),
          ),
        ),
      ),
      React.createElement(
        "section",
        { className: "detail" },
        React.createElement(
          "section",
          { className: "panel" },
          React.createElement(
            "div",
            { className: "panel-header" },
            React.createElement("h2", null, "Drilldown"),
            React.createElement(
              "div",
              { className: "tab-row" },
              [
                { id: "trace", label: "Trace" },
                { id: "chat", label: "Chat" },
                { id: "events", label: "Events" },
                { id: "task", label: "Task" },
              ].map((tab) =>
                React.createElement(
                  "button",
                  {
                    key: tab.id,
                    className: `tab ${activeTab === tab.id ? "active" : ""}`,
                    onClick: () => setActiveTab(tab.id),
                  },
                  tab.label,
                ),
              ),
            ),
          ),
          React.createElement(
            "div",
            { className: "panel-body" },
            activeTab === "trace"
              ? React.createElement(
                  "div",
                  { className: "trace" },
                  React.createElement(
                    "div",
                    { className: "trace-scroll", ref: traceRef },
                    llmTask
                      ? React.createElement(
                          "div",
                          { className: "trace-header" },
                          React.createElement("span", { className: "muted" }, `LLM Task: ${llmTask.id.slice(0, 8)} · ${llmTask.status}`),
                          React.createElement("span", { className: "muted" }, formatDateTime(llmTask.updated_at)),
                        )
                      : React.createElement("div", { className: "muted" }, "No LLM task found."),
                    trace.map((item) => {
                      if (item.role === "tool") {
                        return React.createElement(
                          "div",
                          { className: "trace-item trace-tool", key: item.id },
                          React.createElement(
                            "div",
                            { className: "trace-meta trace-tool-meta" },
                            React.createElement("span", null, item.name || "tool"),
                            React.createElement(StatusBadge, { status: item.status, label: item.status }),
                          ),
                          item.input ? React.createElement(ToolInput, { toolName: item.name, raw: item.input }) : null,
                          item.result ? React.createElement(Markdown, { text: toMarkdown(item.result) }) : null,
                        );
                      }
                      if (item.role === "thinking") {
                        return React.createElement(
                          "details",
                          { className: "trace-item trace-thinking", key: item.id },
                          React.createElement("summary", null, "Thinking"),
                          React.createElement(Markdown, { text: item.text }),
                        );
                      }
                      return React.createElement(
                        "div",
                        { className: `trace-item trace-${item.role}`, key: item.id },
                        React.createElement(
                          "div",
                          { className: "trace-meta" },
                          item.role.toUpperCase(),
                        ),
                        React.createElement(Markdown, { text: item.text }),
                      );
                    }),
                  ),
                  React.createElement(
                    "div",
                    { className: "trace-chat" },
                    React.createElement("input", {
                      className: "trace-chat-input",
                      value: messageInput,
                      onChange: (event) => setMessageInput(event.target.value),
                      onKeyDown: (event) => {
                        if (event.key === "Enter") {
                          event.preventDefault();
                          handleSend();
                        }
                      },
                      placeholder: "Message the agent…",
                    }),
                    React.createElement(
                      "div",
                      { className: "muted" },
                      sendStatus ? `Status: ${sendStatus}` : "Enter to send",
                    ),
                  ),
                )
              : null,
            activeTab === "chat"
              ? React.createElement(
                  "div",
                  { className: "chat" },
                  React.createElement(
                    "div",
                    { className: "chat-log" },
                    chatMessages.length === 0
                      ? React.createElement("div", { className: "muted" }, "No messages yet.")
                      : chatMessages.map((msg) => {
                          const source = msg.metadata?.source || "system";
                          return React.createElement(
                            "div",
                            { key: msg.id, className: `chat-item chat-${source === CLIENT_ID ? "user" : "assistant"}` },
                            React.createElement(
                              "div",
                              { className: "chat-meta" },
                              `${source} · ${formatTime(msg.created_at)}`,
                            ),
                            React.createElement(Markdown, { text: msg.body || "" }),
                          );
                        }),
                  ),
                )
              : null,
            activeTab === "events"
              ? React.createElement(
                  "div",
                  { className: "events" },
                  React.createElement(
                    "div",
                    { className: "event-tabs" },
                    STREAMS.map((stream) =>
                      React.createElement(
                        "button",
                        {
                          key: stream,
                          className: `tab ${activeStream === stream ? "active" : ""}`,
                          onClick: () => setActiveStream(stream),
                        },
                        stream,
                      ),
                    ),
                  ),
                  React.createElement(
                    "div",
                    { className: "event-log" },
                    streamItems.length === 0
                      ? React.createElement("div", { className: "muted" }, "No events.")
                      : streamItems.map((evt) =>
                          React.createElement(
                            "div",
                            { className: "event-item", key: evt.id },
                            React.createElement(
                              "div",
                              { className: "event-meta" },
                              `${evt.stream} · ${formatTime(evt.created_at)} · ${evt.subject || evt.body}`,
                            ),
                            React.createElement(Markdown, { text: toMarkdown(evt.payload || evt.metadata || evt.body) }),
                          ),
                        ),
                  ),
                )
              : null,
            activeTab === "task"
              ? React.createElement(TaskDetail, { task: selectedTask })
              : null,
          ),
        ),
      ),
    ),
  );
}

function TaskDetail({ task }) {
  if (!task) {
    return React.createElement("div", { className: "muted" }, "Select a task to inspect.");
  }
  return React.createElement(
    "div",
    { className: "task-detail" },
    React.createElement(
      "div",
      { className: "task-detail-header" },
      React.createElement("h3", null, task.type),
      React.createElement("span", { className: "muted" }, task.id),
    ),
    React.createElement(
      "div",
      { className: "task-detail-grid" },
      React.createElement("div", { className: "task-field" }, "Status", React.createElement(StatusBadge, { status: task.status })),
      React.createElement("div", { className: "task-field" }, "Owner", task.owner || "-"),
      React.createElement("div", { className: "task-field" }, "Parent", task.parent_id || "-"),
      React.createElement("div", { className: "task-field" }, "Updated", formatDateTime(task.updated_at)),
    ),
    React.createElement(
      "div",
      { className: "task-detail-block" },
      React.createElement("h4", null, "Payload"),
      React.createElement(Markdown, { text: toMarkdown(task.payload || {}) }),
    ),
    React.createElement(
      "div",
      { className: "task-detail-block" },
      React.createElement("h4", null, "Result"),
      React.createElement(Markdown, { text: toMarkdown(task.result || task.error || "-") }),
    ),
  );
}

const root = createRoot(document.getElementById("root"));
root.render(React.createElement(App));
