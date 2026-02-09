import { createAgent, sendInput } from "./api.ts"

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

  let taskId: string
  const targetTaskID = options.task_id?.trim()

  if (targetTaskID) {
    taskId = targetTaskID
  } else {
    const result = await createAgent({
      id: options.id,
      system: options.system,
      model: options.model,
      source: options.source,
    })
    taskId = result.task_id
  }

  await sendInput(taskId, options.message, {
    source: options.source,
    priority: options.priority,
    request_id: options.request_id,
    context: options.context,
  })

  return { task_id: taskId }
}

export type { AgentOptions, AgentResult, AgentModel }
