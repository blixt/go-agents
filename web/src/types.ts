export type JSONValue = string | number | boolean | null | JSONObject | JSONArray;
export type JSONObject = { [key: string]: JSONValue };
export type JSONArray = JSONValue[];

export type RuntimeStatus = {
  connected: boolean;
  error: string;
};

export type Agent = {
  id: string;
  status: string;
  active_tasks: number;
  updated_at: string;
  generation: number;
};

export type Session = {
  task_id: string;
  updated_at: string;
  [key: string]: unknown;
};

export type HistoryEntry = {
  id: string;
  task_id: string;
  generation: number;
  type: string;
  role: string;
  content: string;
  llm_task_id: string;
  tool_call_id: string;
  tool_name: string;
  tool_status: string;
  created_at: string;
  data: Record<string, unknown>;
};

export type History = {
  agent_id?: string;
  task_id?: string;
  generation: number;
  entries: HistoryEntry[];
};

export type Task = Record<string, unknown>;
export type TaskUpdates = Record<string, unknown>;
export type SessionMap = Record<string, Session>;
export type HistoryMap = Record<string, History>;

export type RuntimeState = {
  generated_at: string | null;
  agents: Agent[];
  sessions: SessionMap;
  histories: HistoryMap;
  tasks: Task[];
  updates: TaskUpdates;
};

export type ToolGroup = {
  id: string;
  type: "tool_call_group";
  role: "tool";
  task_id: string;
  tool_call_id: string;
  tool_name: string;
  tool_status: string;
  created_at: string;
  updated_at: string;
  args_raw: string;
  args: unknown;
  args_parse_error: string;
  result: unknown;
  result_error: string;
  metadata: unknown;
  events: Array<{ type: string; status: string; at: string }>;
};

export type ReasoningGroup = {
  id: string;
  type: "reasoning_group";
  role: "assistant";
  created_at: string;
  updated_at: string;
  reasoning_id: string;
  content: string;
  summary: string;
  parts: number;
};

export type DisplayEntry = HistoryEntry | ToolGroup | ReasoningGroup;
