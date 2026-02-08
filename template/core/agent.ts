type AgentModel = "fast" | "balanced" | "smart"

type AgentOptions = {
  task_id?: string
  message: string
  system?: string
  model?: AgentModel
  source?: string
  priority?: "interrupt" | "wake" | "normal" | "low"
  request_id?: string
}

type AgentResult = {
  task_id: string
  event_id?: string
  status?: string
}

function resolveAPIURL() {
  const envURL = Bun.env.GO_AGENTS_API_URL
  if (envURL && envURL.trim() !== "") return envURL.trim()
  return "http://localhost:8080"
}

export async function agent(options: AgentOptions): Promise<AgentResult> {
  if (!options || !options.message || options.message.trim() === "") {
    throw new Error("agent: message is required")
  }
  const targetTaskID = options.task_id && options.task_id.trim() ? options.task_id.trim() : ""
  if (targetTaskID) {
    // Send message to an existing agent task
    const res = await fetch(`${resolveAPIURL()}/api/tasks/${encodeURIComponent(targetTaskID)}/send`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        message: options.message,
        source: options.source || "agent",
        priority: options.priority || "wake",
        request_id: options.request_id,
      }),
    })
    if (!res.ok) {
      const text = await res.text()
      throw new Error(`agent: send failed (${res.status}) ${text}`)
    }
    return { task_id: targetTaskID }
  }
  // Create a new agent task
  const res = await fetch(`${resolveAPIURL()}/api/tasks`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      type: "agent",
      payload: {
        message: options.message,
        system: options.system,
        model: options.model,
      },
      source: options.source || "agent",
      priority: options.priority || "wake",
    }),
  })
  if (!res.ok) {
    const text = await res.text()
    throw new Error(`agent: request failed (${res.status}) ${text}`)
  }
  const data = (await res.json()) as Record<string, unknown>
  return {
    task_id: (data.task_id as string) || "",
    status: data.status as string | undefined,
  }
}

export type { AgentOptions, AgentResult, AgentModel }
