const apiBase = "";
const agentId = "operator";
const clientId = "human";
const reader = agentId;

const asciiLogo = document.getElementById("ascii-logo");
const tasksBody = document.getElementById("tasks-body");
const updatesBody = document.getElementById("updates-body");
const streamsBody = document.getElementById("streams-body");
const selectedTaskLabel = document.getElementById("selected-task");
const statusIndicator = document.getElementById("status-indicator");
const statusText = document.getElementById("status-text");
const agentMessageInput = document.getElementById("agent-message");
const chatLog = document.getElementById("chat-log");

const agentsBody = document.getElementById("agents-body");
const promptBody = document.getElementById("prompt-body");

let selectedTaskId = null;
let activeStream = "task_output";
let sseOpened = false;
const pendingReplies = [];

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

function renderLogo() {
  if (!asciiLogo) return;
  const titleLines = renderAsciiText("GO-AGENTS");
  const subtitle = "CONTROL PANEL · EXEC/RUNTIME";
  const paddedSubtitle = subtitle.padStart(Math.floor((titleLines[0].length + subtitle.length) / 2), " ");
  const combined = [...titleLines, paddedSubtitle];
  asciiLogo.textContent = frameAscii(combined);
}

function formatTime(ts) {
  try {
    return new Date(ts).toLocaleTimeString();
  } catch (_) {
    return "-";
  }
}

async function fetchJSON(path) {
  const res = await fetch(`${apiBase}${path}`);
  if (!res.ok) {
    throw new Error(`Request failed: ${res.status}`);
  }
  return res.json();
}

async function refreshTasks() {
  const tasks = await fetchJSON(`/api/tasks?limit=50`);
  renderTasks(tasks);
}

function renderTasks(tasks) {
  tasksBody.innerHTML = "";
  tasks.forEach((task) => {
    const row = document.createElement("tr");
    row.innerHTML = `
      <td class="code">${task.id.slice(0, 8)}...</td>
      <td>${task.type}</td>
      <td>${task.status}</td>
      <td>${formatTime(task.updated_at)}</td>
    `;
    row.addEventListener("click", () => selectTask(task.id));
    tasksBody.appendChild(row);
  });
}

async function selectTask(taskId) {
  selectedTaskId = taskId;
  selectedTaskLabel.textContent = `Task ${taskId}`;
  await refreshUpdates();
}

async function refreshUpdates() {
  if (!selectedTaskId) {
    updatesBody.innerHTML = "<p class=\"muted\">Select a task to view updates.</p>";
    return;
  }
  const updates = await fetchJSON(`/api/tasks/${selectedTaskId}/updates?limit=200`);
  renderUpdates(updates);
}

function renderUpdates(updates) {
  updatesBody.innerHTML = "";
  if (!updates.length) {
    updatesBody.innerHTML = "<p class=\"muted\">No updates yet.</p>";
    return;
  }
  updates.forEach((update) => {
    const card = document.createElement("div");
    card.className = "update-item";
    card.innerHTML = `
      <h4>${update.kind} · ${formatTime(update.created_at)}</h4>
      <pre>${JSON.stringify(update.payload || {}, null, 2)}</pre>
    `;
    updatesBody.appendChild(card);
  });
}

async function refreshStream() {
  const events = await fetchJSON(`/api/streams/${activeStream}?limit=50&order=lifo&reader=${reader}`);
  renderStream(events);
}

function renderStream(events) {
  streamsBody.innerHTML = "";
  if (!events.length) {
    streamsBody.innerHTML = "<p class=\"muted\">No events yet.</p>";
    return;
  }
  events.forEach((evt) => appendStreamEvent(evt));
}

function appendStreamEvent(evt) {
  const card = document.createElement("div");
  card.className = "stream-item";
  card.innerHTML = `
    <h4>${evt.subject || evt.id} · ${formatTime(evt.created_at)}</h4>
    <pre>${evt.stream}</pre>
  `;
  streamsBody.prepend(card);
}

async function refreshAgents() {
  const agents = await fetchJSON(`/api/agents?limit=50`);
  agentsBody.innerHTML = "";
  if (!agents.length) {
    agentsBody.innerHTML = "<p class=\"muted\">No agents yet.</p>";
    return;
  }
  agents.forEach((agent) => {
    const card = document.createElement("div");
    card.className = "list-item";
    card.innerHTML = `
      <h4>${agent.profile || agent.id}</h4>
      <pre>Status: ${agent.status || "unknown"}</pre>
    `;
    agentsBody.appendChild(card);
  });
}

async function refreshPrompt() {
  try {
    const session = await fetchJSON(`/api/sessions/${encodeURIComponent(agentId)}`);
    promptBody.textContent = session.prompt || "";
  } catch (_) {
    const prompt = await fetchJSON(`/api/prompt`);
    promptBody.textContent = prompt.system_prompt || "";
  }
}

function appendChat(role, text) {
  if (!chatLog) return null;
  const item = document.createElement("div");
  item.className = `chat-item chat-${role}`;
  const meta = document.createElement("div");
  meta.className = "chat-meta";
  meta.textContent = role.toUpperCase();
  const body = document.createElement("pre");
  body.textContent = text;
  item.appendChild(meta);
  item.appendChild(body);
  chatLog.appendChild(item);
  chatLog.scrollTop = chatLog.scrollHeight;
  return body;
}

async function runAgent() {
  const message = (agentMessageInput?.value || "").trim();
  if (!message) {
    return;
  }
  appendChat("user", message);
  const assistantBody = appendChat("assistant", "…");
  if (assistantBody) {
    pendingReplies.push(assistantBody);
  }
  try {
    agentMessageInput.value = "";
    const res = await fetch(`${apiBase}/api/agents/${encodeURIComponent(agentId)}/run`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ message, source: clientId }),
    });
    if (!res.ok) {
      let detail = "";
      let payload = null;
      try {
        payload = await res.json();
        detail = payload.error ? `: ${payload.error}` : "";
      } catch (_) {
        // ignore
      }
      if (assistantBody) {
        assistantBody.textContent = `Error: Request failed: ${res.status}${detail}`;
      }
      return;
    }
    await res.json();
  } catch (err) {
    if (assistantBody) {
      assistantBody.textContent = `Error: ${err.message || err}`;
    }
  }
}

function setupStreamButtons() {
  document.querySelectorAll(".stream-btn").forEach((btn) => {
    btn.addEventListener("click", async () => {
      document.querySelectorAll(".stream-btn").forEach((b) => b.classList.remove("active"));
      btn.classList.add("active");
      activeStream = btn.dataset.stream;
      await refreshStream();
    });
  });
}

function setupSSE() {
  const source = new EventSource(`/api/streams/subscribe?streams=task_output,errors,signals,external,messages`);
  const timeout = setTimeout(() => {
    if (!sseOpened) {
      statusIndicator.classList.remove("online");
      statusText.textContent = "Disconnected";
    }
  }, 4000);

  source.onopen = () => {
    sseOpened = true;
    clearTimeout(timeout);
    statusIndicator.classList.add("online");
    statusText.textContent = "Live";
  };

  source.onerror = () => {
    statusIndicator.classList.remove("online");
    statusText.textContent = "Disconnected";
  };

  source.onmessage = async (event) => {
    try {
      const data = JSON.parse(event.data);
      if (data.stream === "messages") {
        const target = data.scope_id || "";
        const sourceId = data.metadata?.source || "";
        if (target === clientId) {
          const body = pendingReplies.length ? pendingReplies.shift() : appendChat("assistant", "");
          if (body) {
            body.textContent = data.body || "";
          }
          if (promptBody) {
            refreshPrompt().catch(() => {});
          }
        }
      }
      if (data.stream === activeStream) {
        appendStreamEvent(data);
      }
      if (data.metadata && data.metadata.task_id) {
        await refreshTasks();
        if (data.metadata.task_id === selectedTaskId) {
          await refreshUpdates();
        }
      }
    } catch (_) {
      // ignore
    }
  };
}

document.getElementById("refresh-tasks").addEventListener("click", refreshTasks);
document.getElementById("refresh-agents").addEventListener("click", refreshAgents);
document.getElementById("refresh-prompt").addEventListener("click", refreshPrompt);
agentMessageInput?.addEventListener("keydown", (event) => {
  if (event.key === "Enter" && !event.shiftKey) {
    event.preventDefault();
    runAgent();
  }
});

setupStreamButtons();
setupSSE();
renderLogo();

refreshTasks().catch(() => {});
refreshStream().catch(() => {});
refreshAgents().catch(() => {});
refreshPrompt().catch(() => {});
