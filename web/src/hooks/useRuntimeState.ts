import { useCallback, useEffect, useRef, useState } from "react";
import type { History, HistoryEntry, RuntimeState, RuntimeStatus } from "../types";

const STREAMS = ["history", "messages", "task_output", "signals", "errors", "external"];
const STREAM_LIMIT = 200;

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
        { id: entry.agent_id, status: "running", active_tasks: 0, updated_at: entry.created_at, generation: entry.generation },
        ...(state.agents || []),
      ],
    };
  }
  return { ...state, histories };
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

export function useRuntimeState(): { state: RuntimeState; status: RuntimeStatus; refresh: () => Promise<void> } {
  const [state, setState] = useState<RuntimeState>({
    generated_at: null,
    agents: [],
    sessions: {},
    histories: {},
    tasks: [],
    updates: {},
  });
  const [status, setStatus] = useState<RuntimeStatus>({ connected: false, error: "" });
  const refreshTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const refresh = useCallback(async () => {
    try {
      const data = await fetchJSON(`/api/state?tasks=250&updates=400&streams=${STREAM_LIMIT}&history=1200`);
      setState((prev) => ({ ...prev, ...normalizeState(data) }));
      setStatus((prev) => ({ ...prev, error: "" }));
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      setStatus((prev) => ({ ...prev, error: message }));
    }
  }, []);

  const scheduleRefresh = useCallback(() => {
    if (refreshTimer.current) return;
    refreshTimer.current = setTimeout(() => {
      refreshTimer.current = null;
      void refresh();
    }, 400);
  }, [refresh]);

  useEffect(() => {
    void refresh();
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
      } catch {
        // ignore malformed stream payloads
      }
    };
    return () => {
      src.close();
      if (refreshTimer.current) {
        clearTimeout(refreshTimer.current);
        refreshTimer.current = null;
      }
    };
  }, [refresh, scheduleRefresh]);

  return { state, status, refresh };
}
