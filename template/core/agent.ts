import { createTask, sendInput } from "./api.ts"

type AgentModel = "fast" | "balanced" | "smart"

type AgentOptions = {
  id?: string
  task_id?: string
  message: string
  system?: string
  model?: AgentModel
  source?: string
  priority?: "interrupt" | "wake" | "normal" | "low"
  request_id?: string
  context?: Record<string, unknown>
}

type AgentResult = {
  task_id: string
  event_id?: string
  status?: string
}

export async function agent(options: AgentOptions): Promise<AgentResult> {
  if (!options || !options.message || options.message.trim() === "") {
    throw new Error("agent: message is required")
  }
  const targetTaskID = options.task_id && options.task_id.trim() ? options.task_id.trim() : ""
  if (targetTaskID) {
    await sendInput(targetTaskID, options.message, {
      source: options.source || "agent",
      priority: options.priority || "wake",
      request_id: options.request_id,
      context: options.context,
    })
    return { task_id: targetTaskID }
  }
  const result = await createTask({
    id: options.id,
    type: "agent",
    payload: {
      message: options.message,
      system: options.system,
      model: options.model,
    },
    source: options.source || "agent",
    priority: options.priority || "wake",
    context: options.context,
  })
  return {
    task_id: result.task_id || "",
    status: result.status,
  }
}

export type { AgentOptions, AgentResult, AgentModel }
