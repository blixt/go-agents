import type { History, HistoryEntry, RuntimeState, RuntimeStatus } from "./types";

const STREAMS = ["history", "messages", "task_output", "signals", "errors", "external"];
const STREAM_LIMIT = 200;
const POLL_INTERVAL_MS = 2000;
const REFRESH_DEBOUNCE_MS = 400;
const REFRESH_FAILURE_THRESHOLD = 3;
const GLOBAL_STORE_KEY = "__go_agents_runtime_store__";

type Listener = () => void;

type RuntimeSnapshot = {
  state: RuntimeState;
  status: RuntimeStatus;
};

function initialState(): RuntimeState {
  return {
    generated_at: null,
    agents: [],
    sessions: {},
    histories: {},
    tasks: [],
    updates: {},
  };
}

function cloneHistory(history?: History): History {
  if (!history) {
    return { agent_id: "", generation: 1, entries: [] };
  }
  return {
    agent_id: history.agent_id || "",
    generation: typeof history.generation === "number" ? history.generation : 1,
    entries: Array.isArray(history.entries) ? [...history.entries] : [],
  };
}

function parseHistoryEvent(evt: any): HistoryEntry | null {
  if (!evt || evt.stream !== "history" || !evt.payload) return null;
  const payload = evt.payload || {};
  const hasContent = Object.prototype.hasOwnProperty.call(payload, "content");
  const entry: HistoryEntry = {
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

function upsertHistory(state: RuntimeState, entry: HistoryEntry): RuntimeState {
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
      current.entries.sort((a, b) => new Date(a.created_at).getTime() - new Date(b.created_at).getTime());
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
        {
          id: entry.agent_id,
          status: "running",
          active_tasks: 0,
          updated_at: entry.created_at,
          generation: entry.generation,
        },
        ...(state.agents || []),
      ],
    };
  }

  return { ...state, histories };
}

function mergeHistories(previous: Record<string, History>, incoming: Record<string, History>): Record<string, History> {
  const merged: Record<string, History> = {};
  Object.entries(previous || {}).forEach(([agentID, history]) => {
    merged[agentID] = cloneHistory(history);
  });
  Object.entries(incoming || {}).forEach(([agentID, history]) => {
    merged[agentID] = cloneHistory(history);
  });
  return merged;
}

function normalizeState(data: any): RuntimeState {
  return {
    generated_at: data?.generated_at || null,
    agents: Array.isArray(data?.agents) ? data.agents : [],
    sessions: data?.sessions || {},
    histories: data?.histories || {},
    tasks: Array.isArray(data?.tasks) ? data.tasks : [],
    updates: data?.updates || {},
  };
}

async function fetchJSON(path: string): Promise<any> {
  const res = await fetch(path);
  if (!res.ok) {
    throw new Error(`request failed: ${res.status}`);
  }
  return res.json();
}

class RuntimeStore {
  private snapshot: RuntimeSnapshot = {
    state: initialState(),
    status: { connected: false, error: "" },
  };

  private listeners = new Set<Listener>();
  private eventSource: EventSource | null = null;
  private refreshTimer: ReturnType<typeof setTimeout> | null = null;
  private pollTimer: ReturnType<typeof setInterval> | null = null;
  private started = false;
  private refreshInFlight: Promise<void> | null = null;
  private failureCount = 0;

  subscribe = (listener: Listener): (() => void) => {
    this.listeners.add(listener);
    this.ensureStarted();
    return () => {
      this.listeners.delete(listener);
    };
  };

  getSnapshot = (): RuntimeSnapshot => this.snapshot;

  refresh = async (): Promise<void> => {
    if (this.refreshInFlight) {
      return this.refreshInFlight;
    }

    this.refreshInFlight = (async () => {
      try {
        const data = await fetchJSON(`/api/state?tasks=250&updates=400&streams=${STREAM_LIMIT}&history=1200`);
        const next = normalizeState(data);
        this.snapshot = {
          state: {
            generated_at: next.generated_at,
            agents: next.agents.length > 0 ? next.agents : this.snapshot.state.agents,
            sessions: { ...(this.snapshot.state.sessions || {}), ...(next.sessions || {}) },
            histories: mergeHistories(this.snapshot.state.histories || {}, next.histories || {}),
            tasks: next.tasks,
            updates: next.updates,
          },
          status: {
            connected: true,
            error: "",
          },
        };
        this.failureCount = 0;
      } catch (err) {
        this.failureCount += 1;
        if (this.failureCount >= REFRESH_FAILURE_THRESHOLD) {
          const message = err instanceof Error ? err.message : String(err);
          this.snapshot = {
            state: this.snapshot.state,
            status: {
              connected: false,
              error: message,
            },
          };
        }
      } finally {
        this.emit();
      }
    })();

    try {
      await this.refreshInFlight;
    } finally {
      this.refreshInFlight = null;
    }
  };

  private ensureStarted(): void {
    if (this.started || typeof window === "undefined") {
      return;
    }
    this.started = true;
    void this.refresh();
    this.pollTimer = setInterval(() => {
      void this.refresh();
    }, POLL_INTERVAL_MS);
    this.startEventSource();
  }

  private startEventSource(): void {
    if (this.eventSource || typeof window === "undefined") {
      return;
    }

    const src = new EventSource(`/api/streams/subscribe?streams=${STREAMS.join(",")}`);
    this.eventSource = src;

    src.onopen = () => {
      this.failureCount = 0;
      this.snapshot = {
        state: this.snapshot.state,
        status: {
          connected: true,
          error: "",
        },
      };
      this.emit();
    };

    src.onerror = () => {
      // EventSource reconnects automatically; use refresh health for connection state.
      this.scheduleRefresh();
    };

    src.onmessage = (event) => {
      try {
        const evt = JSON.parse(event.data);
        if (!evt || typeof evt !== "object") return;
        if (evt.stream === "history") {
          const parsed = parseHistoryEvent(evt);
          if (!parsed) return;
          this.snapshot = {
            state: upsertHistory(this.snapshot.state, parsed),
            status: this.snapshot.status,
          };
          this.emit();
          return;
        }
        this.scheduleRefresh();
      } catch {
        // ignore malformed stream payloads
      }
    };
  }

  private scheduleRefresh(): void {
    if (this.refreshTimer) {
      return;
    }
    this.refreshTimer = setTimeout(() => {
      this.refreshTimer = null;
      void this.refresh();
    }, REFRESH_DEBOUNCE_MS);
  }

  private emit(): void {
    for (const listener of this.listeners) {
      listener();
    }
  }
}

function resolveGlobalStore(): RuntimeStore {
  const globalRef = globalThis as unknown as { [GLOBAL_STORE_KEY]?: RuntimeStore };
  if (!globalRef[GLOBAL_STORE_KEY]) {
    globalRef[GLOBAL_STORE_KEY] = new RuntimeStore();
  }
  return globalRef[GLOBAL_STORE_KEY] as RuntimeStore;
}

export const runtimeStore = resolveGlobalStore();
