import React from "react";
import { marked } from "marked";
import DOMPurify from "dompurify";
import Prism from "prismjs";
import XMLViewer from "react-xml-viewer";
import "prismjs/components/prism-markup";
import "prismjs/components/prism-javascript";
import "prismjs/components/prism-typescript";
import type { DisplayEntry, HistoryEntry, ReasoningGroup, ToolGroup } from "../types";
import { formatDateTime, isPrimitive, normalizeStatus, parseJSONSafe, toJSON, xmlTheme } from "../utils";

let reactJSONComponent: React.ComponentType<any> | null = null;

function getReactJSONComponent(): React.ComponentType<any> {
  if (reactJSONComponent !== null) return reactJSONComponent;
  reactJSONComponent = (require("react-json-view") as { default: React.ComponentType<any> }).default;
  return reactJSONComponent;
}

const TOOL_EVENT_TYPES = new Set(["tool_call", "tool_status", "tool_result"]);

function renderMarkdown(text: string): string {
  if (!text) return "";
  const html = marked.parse(text, { gfm: true, breaks: true, mangle: false, headerIds: false }) as string;
  const sanitizer =
    DOMPurify && typeof (DOMPurify as any).sanitize === "function"
      ? (DOMPurify as any)
      : DOMPurify && (DOMPurify as any).default && typeof (DOMPurify as any).default.sanitize === "function"
        ? (DOMPurify as any).default
        : null;
  return sanitizer ? sanitizer.sanitize(html) : html;
}

function Markdown({ text }: { text: string }): React.ReactElement {
  if (!text || text.trim() === "") {
    return <div className="muted">Empty</div>;
  }
  return <div className="markdown" dangerouslySetInnerHTML={{ __html: renderMarkdown(text) }} />;
}

export function StatusBadge({ status }: { status: string }): React.ReactElement {
  const normalized = normalizeStatus(status);
  return <span className={`status-pill status-${normalized}`}>{normalized || "idle"}</span>;
}

function renderInlineObject(obj: Record<string, unknown>, className = "inline-fields"): React.ReactElement | null {
  if (!obj || typeof obj !== "object" || Array.isArray(obj)) return null;
  const entries = Object.entries(obj).filter(([, value]) => value !== undefined);
  if (entries.length === 0) return null;
  const allSimple = entries.every(([, value]) => isPrimitive(value) && String(value).length <= 120);
  if (!allSimple || entries.length > 6) return null;
  return (
    <dl className={className}>
      {entries.flatMap(([key, value]) => [
        <dt key={`${className}-k-${key}`}>{key}</dt>,
        <dd key={`${className}-v-${key}`}>{String(value)}</dd>,
      ])}
    </dl>
  );
}

function JsonView({ value, darkMode, collapsed = false }: { value: unknown; darkMode: boolean; collapsed?: boolean }): React.ReactElement | null {
  if (value === null || value === undefined) return null;
  const ReactJson = getReactJSONComponent();
  const src =
    value !== null && typeof value === "object" && !Array.isArray(value)
      ? (value as Record<string, unknown>)
      : Array.isArray(value)
        ? { items: value }
        : { value };
  return (
    <div className="json-viewer">
      <ReactJson
        src={src}
        name={false}
        collapsed={collapsed ? 1 : false}
        iconStyle="triangle"
        displayDataTypes={false}
        displayObjectSize={false}
        enableClipboard={false}
        quotesOnKeys={false}
        collapseStringsAfterLength={180}
        theme={darkMode ? "monokai" : "rjv-default"}
        style={{ backgroundColor: "transparent", fontFamily: "IBM Plex Mono, monospace", fontSize: "12px" }}
      />
    </div>
  );
}

function XMLBlock({ xml, darkMode }: { xml: string; darkMode: boolean }): React.ReactElement {
  return (
    <div className="xml-viewer">
      <XMLViewer xml={xml} theme={xmlTheme(darkMode)} collapsible indentSize={2} showLineNumbers />
    </div>
  );
}

function CodeBlock({ code, language = "typescript", className = "code-block" }: { code: string; language?: string; className?: string }): React.ReactElement {
  const lang = String(language || "text").toLowerCase();
  const prismLanguage = lang === "xml" ? "markup" : lang;
  const grammar =
    (Prism.languages as any)[prismLanguage] ||
    Prism.languages.typescript ||
    Prism.languages.javascript ||
    Prism.languages.clike;
  const raw = String(code || "");
  if (!grammar) {
    return (
      <pre className={`${className} language-${prismLanguage}`}>
        <code>{raw}</code>
      </pre>
    );
  }
  const html = Prism.highlight(raw, grammar, prismLanguage);
  return (
    <pre className={`${className} language-${prismLanguage}`}>
      <code dangerouslySetInnerHTML={{ __html: html }} />
    </pre>
  );
}

function TextBlock({ text, className = "text-block" }: { text: string; className?: string }): React.ReactElement {
  return <pre className={className}>{String(text || "")}</pre>;
}

function isRuntimeInputEnvelope(text: string): boolean {
  if (typeof text !== "string") return false;
  return text.trimStart().startsWith("<system_updates");
}

function simplifyToolResult(result: unknown): unknown {
  const typed = result as any;
  if (!typed || typeof typed !== "object" || !Array.isArray(typed.content)) {
    return result;
  }
  const parsed = typed.content.map((item: any) => {
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
  if (parsed.length === 1 && (!typed.error || typed.error === "")) {
    return parsed[0];
  }
  const out: Record<string, unknown> = {};
  if (typed.label && typed.label !== "Success") out.label = typed.label;
  if (typed.error) out.error = typed.error;
  out.content = parsed;
  return out;
}

function statusRank(status: string): number {
  const normalized = normalizeStatus(status);
  const rank: Record<string, number> = {
    failed: 0,
    error: 0,
    cancelled: 1,
    done: 2,
    completed: 2,
    running: 3,
    waiting: 3,
    streaming: 4,
    start: 5,
    queued: 6,
  };
  return rank[normalized] ?? 10;
}

function chooseStatus(current: string, next: string): string {
  if (!next) return current || "";
  if (!current) return next;
  return statusRank(next) <= statusRank(current) ? next : current;
}

function extractResultError(result: unknown): string {
  const typed = result as any;
  if (!typed || typeof typed !== "object") return "";
  if (typeof typed.error === "string" && typed.error.trim() !== "") return typed.error.trim();
  return "";
}

function isToolEvent(entry: HistoryEntry): boolean {
  return TOOL_EVENT_TYPES.has(entry?.type);
}

export function buildDisplayEntries(entries: HistoryEntry[]): DisplayEntry[] {
  if (!Array.isArray(entries) || entries.length === 0) return [];
  const ordered = [...entries].sort((a, b) => new Date(a.created_at).getTime() - new Date(b.created_at).getTime());
  const out: DisplayEntry[] = [];
  const toolByCallID = new Map<string, ToolGroup>();
  const llmInputTaskIDs = new Set(
    ordered.filter((entry) => entry && entry.type === "llm_input" && entry.llm_task_id !== "").map((entry) => entry.llm_task_id),
  );

  for (const entry of ordered) {
    if (entry.type === "context_event") continue;
    if (entry.type === "user_message" && entry.llm_task_id && llmInputTaskIDs.has(entry.llm_task_id)) continue;

    if (isToolEvent(entry) && entry.tool_call_id) {
      const callID = entry.tool_call_id;
      let group = toolByCallID.get(callID);
      if (!group) {
        group = {
          id: `tool-${callID}`,
          type: "tool_call_group",
          role: "tool",
          task_id: entry.task_id || entry.llm_task_id || "",
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
      if (entry.type === "tool_result" && !group.tool_status) group.tool_status = "done";

      const data = entry.data || {};
      if (typeof data.delta === "string" && data.delta !== "") group.args_raw += data.delta;
      if (typeof data.args_raw === "string" && data.args_raw.trim() !== "") group.args_raw = data.args_raw;
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
      if (data.result !== undefined) group.result = simplifyToolResult(data.result);
      if (data.metadata !== undefined) group.metadata = data.metadata;
      const resultError = extractResultError(group.result);
      if (resultError !== "") {
        group.result_error = resultError;
        group.tool_status = "failed";
      }
      group.events.push({ type: entry.type, status: entry.tool_status || "", at: entry.created_at });
      continue;
    }

    if (entry.type === "reasoning") {
      const last = out.length > 0 ? out[out.length - 1] : null;
      if (last && last.type === "reasoning_group") {
        const typedLast = last as ReasoningGroup;
        if (entry.content) {
          typedLast.content += entry.content;
          typedLast.parts += 1;
        }
        if (typeof entry.data?.summary === "string" && entry.data.summary.trim() !== "") {
          typedLast.summary = entry.data.summary.trim();
        }
        typedLast.updated_at = entry.created_at || typedLast.updated_at;
        if (entry.data?.reasoning_id) {
          typedLast.reasoning_id = String(entry.data.reasoning_id);
        }
        continue;
      }

      const group: ReasoningGroup = {
        id: `reasoning-${entry.id}`,
        type: "reasoning_group",
        role: "assistant",
        created_at: entry.created_at,
        updated_at: entry.created_at,
        reasoning_id: String(entry.data?.reasoning_id || ""),
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

function renderMaybeCollapsedJSON(
  title: string,
  value: unknown,
  options: { collapseAt?: number; darkMode: boolean; collapsed?: boolean },
): React.ReactElement | null {
  if (value === null || value === undefined) return null;
  const json = toJSON(value);
  if (json.trim() === "") return null;
  const collapseAt = typeof options.collapseAt === "number" ? options.collapseAt : 420;
  const shouldCollapse = Boolean(options.collapsed) || json.length > collapseAt;
  return (
    <>
      {title ? <div className="history-title compact">{title}</div> : null}
      <JsonView value={value} darkMode={options.darkMode} collapsed={shouldCollapse} />
    </>
  );
}

function renderToolArgs(toolEntry: ToolGroup, darkMode: boolean): React.ReactElement | null {
  const args = toolEntry.args as any;
  const raw = String(toolEntry.args_raw || "");
  if (toolEntry.tool_name === "exec" && args && typeof args === "object" && typeof args.code === "string") {
    const extra = { ...args };
    delete extra.code;
    return (
      <>
        <div className="history-title compact">Arguments</div>
        <CodeBlock code={args.code} />
        {renderInlineObject(extra) || (Object.keys(extra).length > 0 ? <JsonView value={extra} darkMode={darkMode} /> : null)}
      </>
    );
  }

  if (toolEntry.tool_name === "await_task" && args && typeof args === "object") {
    return (
      <>
        <div className="history-title compact">Arguments</div>
        {renderInlineObject(args) || <JsonView value={args} darkMode={darkMode} />}
      </>
    );
  }

  if (args !== undefined) {
    return (
      <>
        <div className="history-title compact">Arguments</div>
        {renderInlineObject(args) || <JsonView value={args} darkMode={darkMode} />}
      </>
    );
  }

  if (raw.trim() !== "") {
    return (
      <>
        <div className="history-title compact">Arguments (raw stream)</div>
        <pre className="json">{raw}</pre>
        {toolEntry.args_parse_error ? <div className="muted">parse error: {toolEntry.args_parse_error}</div> : null}
      </>
    );
  }
  return null;
}

export function EntryCard({ entry, darkMode }: { entry: DisplayEntry; darkMode: boolean }): React.ReactElement {
  const when = formatDateTime((entry as any).created_at);
  const baseMeta = `${(entry as any).role} · ${(entry as any).type} · ${when}`;

  if (entry.type === "system_prompt") {
    return (
      <details className="history-card history-system">
        <summary>{baseMeta}</summary>
        <Markdown text={(entry as HistoryEntry).content || ""} />
      </details>
    );
  }

  if (entry.type === "tools_config") {
    const tools = Array.isArray((entry as HistoryEntry).data?.tools) ? ((entry as HistoryEntry).data.tools as string[]) : [];
    return (
      <div className="history-card history-system">
        <div className="history-meta">{baseMeta}</div>
        <div className="history-title">Tools Configuration</div>
        {tools.length > 0 ? (
          <div className="tool-list">{tools.map((tool) => <span className="tool-chip" key={tool}>{tool}</span>)}</div>
        ) : (
          <div className="muted">No tools configured</div>
        )}
      </div>
    );
  }

  if (entry.type === "user_message") {
    return (
      <div className="history-card history-user">
        <div className="history-meta">{baseMeta}</div>
        <Markdown text={(entry as HistoryEntry).content || ""} />
      </div>
    );
  }

  if (entry.type === "llm_input") {
    const typed = entry as HistoryEntry;
    const looksXML = isRuntimeInputEnvelope(typed.content || "");
    return (
      <div className="history-card history-system-update">
        <div className="history-meta">{baseMeta}</div>
        {looksXML ? <XMLBlock xml={typed.content || ""} darkMode={darkMode} /> : <TextBlock text={typed.content || ""} />}
      </div>
    );
  }

  if (entry.type === "assistant_message") {
    const typed = entry as HistoryEntry;
    const turn = Number(typed.data?.turn || 0);
    const partial = Boolean(typed.data?.partial);
    let metaLabel = baseMeta;
    if (turn > 0) metaLabel = `assistant · turn ${turn} · ${when}`;
    if (partial) metaLabel += " · partial";
    return (
      <div className="history-card history-assistant">
        <div className="history-meta">{metaLabel}</div>
        <Markdown text={typed.content || ""} />
      </div>
    );
  }

  if (entry.type === "reasoning_group") {
    const typed = entry as ReasoningGroup;
    return (
      <div className="history-card history-reasoning history-compact">
        <div className="history-meta">assistant · reasoning · {formatDateTime(typed.updated_at || typed.created_at)}</div>
        {typed.summary ? <div className="muted">summary: {typed.summary}</div> : null}
        <TextBlock text={typed.content || ""} />
      </div>
    );
  }

  if (entry.type === "tool_call_group") {
    const typed = entry as ToolGroup;
    return (
      <div className="history-card history-tool history-compact">
        <div className="history-meta">tool · {formatDateTime(typed.created_at)}</div>
        <div className="tool-head">
          <span className="history-title compact">{typed.tool_name || "tool"}</span>
          <StatusBadge status={typed.tool_status || "running"} />
        </div>
        {renderToolArgs(typed, darkMode)}
        {renderMaybeCollapsedJSON("Result", typed.result, { collapseAt: 600, darkMode })}
        {typed.result_error ? <div className="error">{typed.result_error}</div> : null}
      </div>
    );
  }

  if (entry.type === "context_event") {
    const typed = entry as HistoryEntry;
    return (
      <div className="history-card history-system-update history-compact">
        <div className="history-meta">{baseMeta}</div>
        <div className="history-title compact">
          {`${String(typed.data?.stream || "event")}${typed.data?.priority ? ` · ${String(typed.data.priority)}` : ""}`}
        </div>
        {typed.content ? <Markdown text={typed.content} /> : null}
        {renderMaybeCollapsedJSON("Event payload", typed.data, { collapseAt: 320, darkMode })}
      </div>
    );
  }

  if (entry.type === "system_update") {
    const typed = entry as HistoryEntry;
    return (
      <div className="history-card history-system-update history-compact">
        <div className="history-meta">{baseMeta}</div>
        <div className="history-title compact">{typed.content || "system update"}</div>
        {renderMaybeCollapsedJSON("", typed.data, { collapseAt: 340, darkMode })}
      </div>
    );
  }

  if (entry.type === "context_compaction") {
    const typed = entry as HistoryEntry;
    return (
      <div className="history-card history-compaction history-compact">
        <div className="history-meta">{baseMeta}</div>
        <div className="history-title compact">{typed.content || "context compacted"}</div>
        {renderMaybeCollapsedJSON("", typed.data, { collapseAt: 420, darkMode })}
      </div>
    );
  }

  const typed = entry as HistoryEntry;
  return (
    <div className="history-card history-system">
      <div className="history-meta">{baseMeta}</div>
      {typed.content ? <Markdown text={typed.content} /> : null}
      {typed.data && Object.keys(typed.data).length > 0 ? <JsonView value={typed.data} darkMode={darkMode} /> : null}
    </div>
  );
}
