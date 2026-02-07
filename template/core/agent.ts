type AgentModel = "fast" | "balanced" | "smart"

type AgentOptions = {
  agent_id?: string
  message: string
  system?: string
  model?: AgentModel
  source?: string
  priority?: "interrupt" | "wake" | "normal" | "low"
  request_id?: string
}

type AgentResult = {
  agent_id: string
  task_id?: string
  event_id?: string
  status?: string
}

function resolveAPIURL() {
  const envURL = Bun.env.GO_AGENTS_API_URL
  if (envURL && envURL.trim() !== "") return envURL.trim()
  return "http://localhost:8080"
}

function newAgentID() {
  return `subagent-${crypto.randomUUID()}`
}

export async function agent(options: AgentOptions): Promise<AgentResult> {
  if (!options || !options.message || options.message.trim() === "") {
    throw new Error("agent: message is required")
  }
  const agentID = options.agent_id && options.agent_id.trim() ? options.agent_id.trim() : newAgentID()
  const payload = {
    message: options.message,
    system: options.system,
    model: options.model,
    source: options.source || "agent",
    priority: options.priority || "wake",
    request_id: options.request_id,
  }
  const res = await fetch(`${resolveAPIURL()}/api/agents/${agentID}/run`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  })
  if (!res.ok) {
    const text = await res.text()
    throw new Error(`agent: request failed (${res.status}) ${text}`)
  }
  const data = (await res.json()) as AgentResult
  return { ...data, agent_id: agentID }
}

export type { AgentOptions, AgentResult, AgentModel }
