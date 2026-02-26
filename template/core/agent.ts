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
  request_id?: string
  event_id?: string
  status?: string
}

type ScopedAgentOptions = {
  namespace: string
  key: string
  prefix?: string
  message: string
  system?: string
  model?: AgentModel
  source?: string
  priority?: "interrupt" | "wake" | "normal" | "low"
  request_id?: string
  context?: Record<string, unknown>
}

function slugPart(input: string): string {
  const value = (input || "")
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/-+/g, "-")
    .replace(/^-+|-+$/g, "")
  return value || "x"
}

function shortHash(input: string): string {
  let hash = 2166136261
  for (let i = 0; i < input.length; i++) {
    hash ^= input.charCodeAt(i)
    hash += (hash << 1) + (hash << 4) + (hash << 7) + (hash << 8) + (hash << 24)
  }
  return (hash >>> 0).toString(36)
}

function ensureTaskID(id: string): string {
  let out = (id || "").toLowerCase().replace(/[^a-z0-9-]/g, "-").replace(/-+/g, "-")
  out = out.replace(/^-+/, "")
  if (!/^[a-z]/.test(out)) out = `a-${out}`
  out = out.replace(/-+$/, "")
  if (!/[a-z0-9]$/.test(out)) out = `${out}0`
  if (out.length > 64) out = out.slice(0, 64)
  out = out.replace(/-+$/, "")
  if (!/[a-z0-9]$/.test(out)) out = `${out.slice(0, 63)}0`
  return out
}

export function scopedAgentTaskID(namespace: string, key: string, prefix = "agent"): string {
  const p = slugPart(prefix)
  const n = slugPart(namespace)
  const k = slugPart(key)
  const base = `${p}-${n}-${k}`
  if (base.length <= 64) {
    return ensureTaskID(base)
  }

  const hash = shortHash(`${namespace}:${key}`)
  const head = `${p}-${n}`
  const reserve = head.length + hash.length + 2 // two dashes
  const keyBudget = Math.max(1, 64 - reserve)
  const shortened = k.slice(0, keyBudget)
  return ensureTaskID(`${head}-${shortened}-${hash}`)
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

  const sendResult = await sendInput(taskId, options.message, {
    source: options.source,
    priority: options.priority,
    request_id: options.request_id,
    context: options.context,
  })

  return {
    task_id: taskId,
    request_id: sendResult.request_id,
  }
}

export async function scopedAgent(options: ScopedAgentOptions): Promise<AgentResult> {
  const id = scopedAgentTaskID(options.namespace, options.key, options.prefix)
  return agent({
    id,
    message: options.message,
    system: options.system,
    model: options.model,
    source: options.source,
    priority: options.priority,
    request_id: options.request_id,
    context: options.context,
  })
}

export type { AgentOptions, AgentResult, AgentModel, ScopedAgentOptions }
