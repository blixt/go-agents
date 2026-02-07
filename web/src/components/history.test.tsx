import { expect, test } from "bun:test";
import React from "react";
import { render, screen } from "@testing-library/react";
import { buildDisplayEntries, EntryCard } from "./history";
import type { HistoryEntry } from "../types";

test("renders streaming-to-done tool call as a single card with JSON result", () => {
  const toolEntries: HistoryEntry[] = [
    {
      id: "h-1",
      agent_id: "agent-1",
      generation: 1,
      type: "tool_call",
      role: "assistant",
      content: "",
      task_id: "task-1",
      tool_call_id: "call-1",
      tool_name: "exec",
      tool_status: "streaming",
      created_at: "2026-02-07T00:00:00Z",
      data: {
        delta: "{\"code\":\"console.log('hello')\",\"wait_seconds\":5}",
      },
    },
    {
      id: "h-2",
      agent_id: "agent-1",
      generation: 1,
      type: "tool_result",
      role: "assistant",
      content: "",
      task_id: "task-1",
      tool_call_id: "call-1",
      tool_name: "exec",
      tool_status: "done",
      created_at: "2026-02-07T00:00:01Z",
      data: {
        result: {
          label: "Success",
          content: [{ type: "json", data: "{\"status\":\"completed\",\"result\":{\"ok\":true}}" }],
        },
      },
    },
  ];

  const display = buildDisplayEntries(toolEntries);
  expect(display.length).toBe(1);
  expect(display[0]?.type).toBe("tool_call_group");

  render(<EntryCard entry={display[0]} darkMode={false} />);

  expect(screen.getByText("exec")).toBeInTheDocument();
  expect(screen.getByText("done")).toBeInTheDocument();
  expect(screen.getByText("wait_seconds")).toBeInTheDocument();
  expect(screen.getByText("status")).toBeInTheDocument();
  expect(screen.getByText("ok")).toBeInTheDocument();
});

test("renders llm_input XML envelope through XML viewer path", () => {
  const xmlEntry: HistoryEntry = {
    id: "h-xml-1",
    agent_id: "agent-1",
    generation: 1,
    type: "llm_input",
    role: "system",
    content:
      '<user_turn source="runtime" priority="normal"><system_updates user_authored="false"><context_updates generated_at="2026-02-07T00:00:00Z"></context_updates></system_updates></user_turn>',
    task_id: "task-xml-1",
    tool_call_id: "",
    tool_name: "",
    tool_status: "",
    created_at: "2026-02-07T00:00:02Z",
    data: {},
  };

  const { container } = render(<EntryCard entry={xmlEntry} darkMode={true} />);
  expect(container.querySelector(".xml-viewer")).not.toBeNull();
  expect(container.querySelector(".history-system-update")).not.toBeNull();
});
