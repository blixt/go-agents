import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { EntryCard, StatusBadge, buildDisplayEntries } from "./components/history";
import { useRuntimeState } from "./hooks/useRuntimeState";
import type { Agent, DisplayEntry, History } from "./types";
import { formatDateTime, formatTime, usePrefersDark } from "./utils";

const SELECTED_AGENT_STORAGE_KEY = "go-agents.selected-agent";

export function App(): React.ReactElement {
  const { state, status, refresh } = useRuntimeState();
  const darkMode = usePrefersDark();
  const [selectedAgent, setSelectedAgent] = useState("");
  const savedAgentRef = useRef(
    typeof window !== "undefined" ? (window.localStorage.getItem(SELECTED_AGENT_STORAGE_KEY) || "").trim() : "",
  );
  const [newAgentId, setNewAgentId] = useState("operator");
  const [message, setMessage] = useState("");
  const [sendStatus, setSendStatus] = useState("");
  const [compactStatus, setCompactStatus] = useState("");
  const timelineRef = useRef<HTMLDivElement | null>(null);
  const textareaRef = useRef<HTMLTextAreaElement | null>(null);

  const agents = useMemo(() => {
    const fromServer = Array.isArray(state.agents) ? (state.agents as Agent[]) : [];
    if (fromServer.length > 0) {
      if (!selectedAgent || fromServer.some((agent) => agent.id === selectedAgent)) {
        return fromServer;
      }
      return [
        {
          id: selectedAgent,
          status: "idle",
          active_tasks: 0,
          updated_at: "",
          generation: state.histories?.[selectedAgent]?.generation || 1,
        },
        ...fromServer,
      ];
    }
    // No agents from server — build from server-known IDs only (no localStorage phantoms).
    const ids = new Set<string>();
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
  }, [selectedAgent, state.agents, state.histories, state.sessions]);

  useEffect(() => {
    if (agents.length === 0) return;
    if (selectedAgent && agents.some((a) => a.id === selectedAgent)) return;
    // Restore saved selection if the server knows about it, otherwise pick the first agent.
    const saved = savedAgentRef.current;
    if (saved && agents.some((a) => a.id === saved)) {
      setSelectedAgent(saved);
    } else {
      setSelectedAgent(agents[0]?.id || "");
    }
  }, [agents, selectedAgent]);

  useEffect(() => {
    if (typeof window === "undefined") {
      return;
    }
    if (selectedAgent) {
      savedAgentRef.current = selectedAgent;
      window.localStorage.setItem(SELECTED_AGENT_STORAGE_KEY, selectedAgent);
      return;
    }
    window.localStorage.removeItem(SELECTED_AGENT_STORAGE_KEY);
  }, [selectedAgent]);

  // Autofocus the textarea on mount and when the selected agent changes.
  useEffect(() => {
    textareaRef.current?.focus();
  }, [selectedAgent]);

  const selectedHistory = (state.histories?.[selectedAgent] || { generation: 1, entries: [] }) as History;
  const selectedSession = state.sessions?.[selectedAgent] || null;
  const selectedAgentState = agents.find((agent) => agent.id === selectedAgent) || {
    id: selectedAgent,
    status: "idle",
    active_tasks: 0,
    generation: selectedHistory.generation || 1,
  };
  const timelineEntries = useMemo<DisplayEntry[]>(
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

    // Resolve the target: selected agent, or the new agent ID field.
    const targetAgent = selectedAgent || newAgentId.trim();
    if (!targetAgent) {
      setSendStatus("enter an agent ID first");
      return;
    }

    setSendStatus("sending");
    try {
      // Upsert: create the agent if it doesn't exist (idempotent).
      const createRes = await fetch("/api/tasks", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ id: targetAgent, type: "agent" }),
      });
      if (!createRes.ok) {
        const text = await createRes.text();
        setSendStatus(`error ${createRes.status}: ${text}`);
        return;
      }
      // Send the message to the agent.
      const res = await fetch(`/api/tasks/${encodeURIComponent(targetAgent)}/send`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ message: trimmed, source: "web", priority: "wake" }),
      });
      if (!res.ok) {
        const text = await res.text();
        setSendStatus(`error ${res.status}: ${text}`);
        return;
      }
      if (!selectedAgent) {
        setSelectedAgent(targetAgent);
        setNewAgentId("operator");
      }
      setMessage("");
      setSendStatus("sent");
      void refresh();
    } catch (err) {
      const errorMessage = err instanceof Error ? err.message : String(err);
      setSendStatus(`error: ${errorMessage}`);
    }
  }, [message, refresh, selectedAgent, newAgentId]);

  const handleCompact = useCallback(async () => {
    if (!selectedAgent) {
      setCompactStatus("no agent selected");
      return;
    }
    setCompactStatus("compacting");
    try {
      const res = await fetch(`/api/tasks/${encodeURIComponent(selectedAgent)}/compact`, {
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
      void refresh();
    } catch (err) {
      const errorMessage = err instanceof Error ? err.message : String(err);
      setCompactStatus(`error: ${errorMessage}`);
    }
  }, [refresh, selectedAgent]);

  return (
    <div className={`app ${darkMode ? "theme-dark" : "theme-light"}`}>
      <header className="topbar">
        <div>
          <h1>go-agents</h1>
          <div className="subtitle">Agent History Console</div>
        </div>
        <div className="status-bar">
          <span className={`dot ${status.connected ? "online" : ""}`} />
          <span>{status.connected ? "Live" : "Disconnected"}</span>
          <span className="muted">snapshot {formatTime(state.generated_at)}</span>
          {status.error ? <span className="error">{status.error}</span> : null}
        </div>
      </header>
      <main className="layout">
        <aside className="sidebar panel">
          <h2>Agents</h2>
          <div className="agent-list">
            {agents.map((agent) => (
              <button
                key={agent.id}
                className={`agent-row ${agent.id === selectedAgent ? "active" : ""}`}
                onClick={() => setSelectedAgent(agent.id)}
              >
                <div className="agent-main">
                  <div className="agent-id">{agent.id}</div>
                  <div className="agent-meta">
                    active {agent.active_tasks || 0} · gen {agent.generation || 1} · {formatTime(agent.updated_at)}
                  </div>
                </div>
                <StatusBadge status={agent.status || "idle"} />
              </button>
            ))}
            {agents.length === 0 ? <div className="muted">No agents yet.</div> : null}
          </div>
        </aside>
        <section className="panel detail">
          <div className="detail-head">
            <h2>{selectedAgent || "No agent selected"}</h2>
            <div className="detail-meta">
              <StatusBadge status={selectedAgentState.status || "idle"} />
              <span className="muted">generation {selectedHistory.generation || 1}</span>
              {selectedSession?.updated_at ? (
                <span className="muted">updated {formatDateTime(String(selectedSession.updated_at))}</span>
              ) : null}
            </div>
          </div>
          <div className="timeline" ref={timelineRef}>
            {!timelineEntries || timelineEntries.length === 0 ? (
              <div className="muted">No history yet.</div>
            ) : (
              timelineEntries.map((entry) => <EntryCard key={(entry as any).id || `${entry.type}-${(entry as any).created_at}`} entry={entry} darkMode={darkMode} />)
            )}
          </div>
          <div className="composer">
            {!selectedAgent ? (
              <div className="new-agent-row">
                <label>Agent ID</label>
                <input
                  type="text"
                  value={newAgentId}
                  onChange={(event) => setNewAgentId(event.target.value)}
                  placeholder="e.g. operator"
                />
              </div>
            ) : null}
            <textarea
              ref={textareaRef}
              autoFocus
              value={message}
              onChange={(event) => setMessage(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === "Enter" && !event.shiftKey) {
                  event.preventDefault();
                  void handleSend();
                }
              }}
              placeholder={selectedAgent ? `Message ${selectedAgent}...` : "Type a message for the new agent..."}
            />
            <div className="composer-actions">
              <button onClick={() => void handleSend()}>Send</button>
              <button className="danger" onClick={() => void handleCompact()} disabled={!selectedAgent}>
                Compact Context
              </button>
              {sendStatus ? <span className="muted">{sendStatus}</span> : null}
              {compactStatus ? <span className="muted">{compactStatus}</span> : null}
            </div>
          </div>
        </section>
      </main>
    </div>
  );
}
