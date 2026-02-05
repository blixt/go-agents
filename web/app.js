import React, { useCallback, useEffect, useMemo, useRef, useState } from "https://esm.sh/react@18.3.1";
import { createRoot } from "https://esm.sh/react-dom@18.3.1/client";
import { Streamdown } from "https://esm.sh/streamdown@2.1.0";

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
    next.status = statusMap[kind] || next.status;
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

  updates.forEach((update, index) => {
    const kind = update.kind;
    const payload = update.payload || {};
    if (kind === "input") {
      const message = payload.message || payload.text || "";
      if (message) {
        assistantItem = null;
        items.push({ id: update.id || `input-${index}`, role: "user", text: message });
      }
      return;
    }

    if (kind === "llm_text") {
      if (!assistantItem) {
        assistantItem = { id: update.id || `llm-${index}`, role: "assistant", text: "" };
        items.push(assistantItem);
      }
      assistantItem.text += payload.text || "";
      return;
    }

    if (kind === "llm_thinking") {
      items.push({
        id: update.id || `thinking-${index}`,
        role: "thinking",
        text: payload.summary || payload.text || "",
      });
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

function Markdown({ text }) {
  if (!text) {
    return React.createElement("div", { className: "markdown empty" }, "-");
  }
  return React.createElement(Streamdown, { className: "markdown" }, text);
}

function StatusBadge({ status }) {
  const cls = `status-pill status-${status || "unknown"}`;
  return React.createElement("span", { className: cls }, status || "unknown");
}

function App() {
  const { state, status } = useSyncState();
  const [selectedTaskId, setSelectedTaskId] = useState(null);
  const [activeTab, setActiveTab] = useState("trace");
  const [activeStream, setActiveStream] = useState("messages");
  const [messageInput, setMessageInput] = useState("");
  const [sendStatus, setSendStatus] = useState(null);

  const tasks = useMemo(() => Object.values(state.tasksById || {}), [state.tasksById]);
  const taskTree = useMemo(() => buildTaskTree(state.tasksById || {}), [state.tasksById]);
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
            React.createElement("h2", null, "Tasks"),
          ),
          React.createElement(
            "div",
            { className: "panel-body task-list" },
            taskTree.length === 0
              ? React.createElement("div", { className: "muted" }, "No tasks yet.")
              : taskTree.map(({ task, depth }) =>
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
                { id: "graph", label: "Graph" },
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
                          { className: "trace-meta" },
                          React.createElement("span", null, `Tool · ${item.name}`),
                          React.createElement("span", { className: "muted" }, item.status),
                        ),
                        item.input ? React.createElement(Markdown, { text: item.input }) : null,
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
                  React.createElement(
                    "div",
                    { className: "chat-input" },
                    React.createElement("textarea", {
                      value: messageInput,
                      onChange: (event) => setMessageInput(event.target.value),
                      onKeyDown: (event) => {
                        if (event.key === "Enter" && !event.shiftKey) {
                          event.preventDefault();
                          handleSend();
                        }
                      },
                      placeholder: "Message the agent. Enter to send, Shift+Enter for newline.",
                      rows: 4,
                    }),
                    React.createElement(
                      "div",
                      { className: "muted" },
                      sendStatus ? `Status: ${sendStatus}` : "Enter to send · Shift+Enter for newline",
                    ),
                  ),
                )
              : null,
            activeTab === "graph"
              ? React.createElement(TaskGraph, { tasks })
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

function TaskGraph({ tasks }) {
  if (!tasks || tasks.length === 0) {
    return React.createElement("div", { className: "muted" }, "No tasks yet.");
  }
  const nodes = tasks.map((task) => ({
    id: task.id,
    parent: task.parent_id || task.metadata?.parent_id || null,
    label: task.type,
  }));

  const depthMap = {};
  const childrenMap = {};
  nodes.forEach((node) => {
    if (!childrenMap[node.parent]) childrenMap[node.parent] = [];
    childrenMap[node.parent].push(node);
  });

  function assignDepth(node, depth) {
    depthMap[node.id] = depth;
    (childrenMap[node.id] || []).forEach((child) => assignDepth(child, depth + 1));
  }

  (childrenMap[null] || childrenMap[""] || []).forEach((root) => assignDepth(root, 0));

  const nodesByDepth = {};
  nodes.forEach((node) => {
    const depth = depthMap[node.id] || 0;
    if (!nodesByDepth[depth]) nodesByDepth[depth] = [];
    nodesByDepth[depth].push(node);
  });

  const positions = {};
  const columnWidth = 180;
  const rowHeight = 90;
  const depths = Object.keys(nodesByDepth).map(Number).sort((a, b) => a - b);

  depths.forEach((depth) => {
    const column = nodesByDepth[depth];
    column.forEach((node, index) => {
      positions[node.id] = {
        x: depth * columnWidth + 60,
        y: index * rowHeight + 40,
      };
    });
  });

  const width = Math.max(400, (depths.length + 1) * columnWidth);
  const maxRows = Math.max(...Object.values(nodesByDepth).map((arr) => arr.length), 1);
  const height = Math.max(300, maxRows * rowHeight + 80);

  return React.createElement(
    "svg",
    { className: "task-graph", viewBox: `0 0 ${width} ${height}` },
    nodes.map((node) => {
      if (!node.parent) return null;
      const from = positions[node.parent];
      const to = positions[node.id];
      if (!from || !to) return null;
      return React.createElement("line", {
        key: `${node.parent}-${node.id}`,
        x1: from.x + 40,
        y1: from.y,
        x2: to.x - 40,
        y2: to.y,
        className: "task-edge",
      });
    }),
    nodes.map((node) => {
      const pos = positions[node.id];
      if (!pos) return null;
      return React.createElement(
        "g",
        { key: node.id, transform: `translate(${pos.x - 40}, ${pos.y - 20})` },
        React.createElement("rect", { className: "task-node", width: 120, height: 40, rx: 4, ry: 4 }),
        React.createElement(
          "text",
          { x: 60, y: 24, className: "task-node-label", textAnchor: "middle" },
          node.label,
        ),
      );
    }),
  );
}

const root = createRoot(document.getElementById("root"));
root.render(React.createElement(App));
