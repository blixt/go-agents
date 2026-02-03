const apiBase = "";
const reader = "ui";

const tasksBody = document.getElementById("tasks-body");
const updatesBody = document.getElementById("updates-body");
const streamsBody = document.getElementById("streams-body");
const selectedTaskLabel = document.getElementById("selected-task");
const statusIndicator = document.getElementById("status-indicator");
const statusText = document.getElementById("status-text");

const agentsBody = document.getElementById("agents-body");
const promptBody = document.getElementById("prompt-body");

let selectedTaskId = null;
let activeStream = "task_output";
let sseOpened = false;

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
    const session = await fetchJSON(`/api/sessions/operator`);
    promptBody.textContent = session.prompt || "";
  } catch (_) {
    const prompt = await fetchJSON(`/api/prompt`);
    promptBody.textContent = prompt.system_prompt || "";
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
  const source = new EventSource(`/api/streams/subscribe?streams=task_output,errors,signals,external`);
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

setupStreamButtons();
setupSSE();

refreshTasks().catch(() => {});
refreshStream().catch(() => {});
refreshAgents().catch(() => {});
refreshPrompt().catch(() => {});
