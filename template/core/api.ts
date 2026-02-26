/** Returns the runtime API base URL (from GO_AGENTS_API_URL env var). */
export function apiURL(): string {
  const envURL = Bun.env.GO_AGENTS_API_URL
  if (envURL && envURL.trim() !== "") return envURL.trim()
  return "http://localhost:8080"
}

async function request(method: string, path: string, body?: unknown): Promise<Response> {
  const url = `${apiURL()}${path}`
  const opts: RequestInit = { method, headers: { "Content-Type": "application/json" } }
  if (body !== undefined) {
    opts.body = JSON.stringify(body)
  }
  const res = await fetch(url, opts)
  if (!res.ok) {
    const text = await res.text()
    throw new Error(`API ${method} ${path} failed (${res.status}): ${text}`)
  }
  return res
}

function currentServiceID(): string {
  return (Bun.env.GO_AGENTS_SERVICE_ID || "").trim()
}

/** Create or ensure an agent exists. Upserts by id. */
export async function createAgent(opts: {
  id?: string
  system?: string
  model?: string
  source?: string
}): Promise<{ task_id: string; status: string; created: boolean }> {
  const res = await request("POST", "/api/tasks", {
    id: opts.id,
    type: "agent",
    payload: {
      ...(opts.system && { system: opts.system }),
      ...(opts.model && { model: opts.model }),
    },
    source: opts.source,
  })
  return (await res.json()) as { task_id: string; status: string; created: boolean }
}

/** Send input to an existing task. For agent tasks, delivers a message. 404 if not found. */
export async function sendInput(
  taskId: string,
  message: string,
  opts?: {
    source?: string
    priority?: string
    request_id?: string
    service_id?: string
    context?: Record<string, unknown>
  },
): Promise<{ ok: boolean; request_id?: string; service_id?: string }> {
  const serviceID = (opts?.service_id || currentServiceID()).trim()
  const context = { ...(opts?.context || {}) }
  const contextServiceID = typeof context.service_id === "string" ? context.service_id.trim() : ""
  if (serviceID !== "" && contextServiceID === "") {
    context.service_id = serviceID
  }
  const source = (opts?.source || "").trim() || serviceID
  const res = await request("POST", `/api/tasks/${encodeURIComponent(taskId)}/send`, {
    message,
    source: source || undefined,
    service_id: serviceID || undefined,
    priority: opts?.priority,
    request_id: opts?.request_id,
    context: Object.keys(context).length > 0 ? context : undefined,
  })
  const payload = await res.json().catch(() => ({})) as Record<string, unknown>
  return {
    ok: payload.ok !== false,
    request_id: typeof payload.request_id === "string" ? payload.request_id : undefined,
    service_id: typeof payload.service_id === "string" ? payload.service_id : undefined,
  }
}

export type AssistantOutputRoute = {
  request_id?: string
  context?: Record<string, unknown>
}

function parseAssistantOutputRoute(raw: unknown): AssistantOutputRoute | null {
  if (!raw || typeof raw !== "object") return null
  const route = raw as Record<string, unknown>
  const request_id = typeof route.request_id === "string" ? route.request_id : undefined
  const contextCandidate = route.context && typeof route.context === "object"
    ? route.context as Record<string, unknown>
    : undefined
  const context = contextCandidate && Object.keys(contextCandidate).length > 0
    ? contextCandidate
    : undefined
  if (!request_id && !context) return null
  return { request_id, context }
}

/**
 * Normalize assistant_output routing metadata to a deterministic list.
 * Uses payload.routes only.
 */
export function assistantOutputRoutes(payload: Record<string, unknown> | undefined): AssistantOutputRoute[] {
  if (!payload) return []
  const routes: AssistantOutputRoute[] = []
  const rawRoutes = Array.isArray(payload.routes) ? payload.routes : []
  for (const rawRoute of rawRoutes) {
    const parsed = parseAssistantOutputRoute(rawRoute)
    if (parsed) routes.push(parsed)
  }
  return routes
}

/** Get updates for a task (stdout, stderr, start, exit, etc.). */
export async function getUpdates(
  taskId: string,
  opts?: { kind?: string; after_id?: string; limit?: number },
): Promise<Array<{ id: string; task_id: string; kind: string; payload?: Record<string, unknown> }>> {
  const params = new URLSearchParams()
  if (opts?.kind) params.set("kind", opts.kind)
  if (opts?.after_id) params.set("after_id", opts.after_id)
  if (opts?.limit) params.set("limit", String(opts.limit))
  const qs = params.toString()
  const path = `/api/tasks/${encodeURIComponent(taskId)}/updates${qs ? `?${qs}` : ""}`
  const res = await request("GET", path)
  return (await res.json()) as Array<{
    id: string
    task_id: string
    kind: string
    payload?: Record<string, unknown>
  }>
}

/** Cancel a task. */
export async function cancelTask(taskId: string, reason?: string): Promise<void> {
  await request("POST", `/api/tasks/${encodeURIComponent(taskId)}/cancel`, {
    reason: reason || "cancelled",
  })
}

/** Get full runtime state (agents, tasks, updates). */
export async function getState(opts?: {
  tasks?: number
  updates?: number
  streams?: number
  history?: number
}): Promise<Record<string, unknown>> {
  const params = new URLSearchParams()
  if (opts?.tasks !== undefined) params.set("tasks", String(opts.tasks))
  if (opts?.updates !== undefined) params.set("updates", String(opts.updates))
  if (opts?.streams !== undefined) params.set("streams", String(opts.streams))
  if (opts?.history !== undefined) params.set("history", String(opts.history))
  const qs = params.toString()
  const path = `/api/state${qs ? `?${qs}` : ""}`
  const res = await request("GET", path)
  return (await res.json()) as Record<string, unknown>
}

type SSESubscription = {
  events: AsyncIterable<Record<string, unknown>>
  close: () => void
}

/** Subscribe to event streams via SSE. Returns an async iterable of events. */
export function subscribe(opts?: { streams?: string[] }): SSESubscription {
  const params = new URLSearchParams()
  if (opts?.streams) {
    for (const s of opts.streams) params.append("stream", s)
  }
  const qs = params.toString()
  const url = `${apiURL()}/api/streams/subscribe${qs ? `?${qs}` : ""}`

  const controller = new AbortController()

  async function* iterateEvents(): AsyncGenerator<Record<string, unknown>> {
    const res = await fetch(url, { signal: controller.signal })
    if (!res.ok) {
      const text = await res.text()
      throw new Error(`SSE subscribe failed (${res.status}): ${text}`)
    }
    if (!res.body) throw new Error("SSE response has no body")

    const reader = res.body.getReader()
    const decoder = new TextDecoder()
    let buffer = ""

    try {
      while (true) {
        const { value, done } = await reader.read()
        if (done) break
        buffer += decoder.decode(value, { stream: true })

        const lines = buffer.split("\n")
        buffer = lines.pop() || ""

        for (const line of lines) {
          if (line.startsWith("data: ")) {
            const data = line.slice(6).trim()
            if (data) {
              try {
                yield JSON.parse(data)
              } catch {
                // skip malformed JSON
              }
            }
          }
        }
      }
    } finally {
      reader.releaseLock()
    }
  }

  return {
    events: iterateEvents(),
    close: () => controller.abort(),
  }
}
