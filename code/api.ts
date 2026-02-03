import { existsSync } from "fs"
import { readFile } from "fs/promises"

const HEALTH_PATH = "/api/health"
const DEFAULT_PORT = "8080"

type FileConfig = {
  http_addr?: string
}

function inDocker(): boolean {
  return existsSync("/.dockerenv")
}

async function loadConfig(): Promise<FileConfig> {
  const paths = ["config.json", "data/config.json"]
  for (const path of paths) {
    try {
      const text = await readFile(path, "utf8")
      if (!text.trim()) continue
      return JSON.parse(text) as FileConfig
    } catch {
      // ignore
    }
  }
  return {}
}

function normalizeHTTPAddr(addr: string): string | undefined {
  const trimmed = addr.trim()
  if (!trimmed) return undefined
  if (trimmed.startsWith("http://") || trimmed.startsWith("https://")) {
    return trimmed
  }
  return undefined
}

function parseHostPort(addr: string): { host?: string; port: string } {
  const trimmed = addr.trim()
  if (!trimmed) return { port: DEFAULT_PORT }
  if (trimmed.startsWith(":")) {
    return { port: trimmed.slice(1) || DEFAULT_PORT }
  }
  if (trimmed.startsWith("0.0.0.0:")) {
    return { host: "0.0.0.0", port: trimmed.slice("0.0.0.0:".length) || DEFAULT_PORT }
  }
  if (trimmed.startsWith("[::]:")) {
    return { host: "[::]", port: trimmed.slice("[::]:".length) || DEFAULT_PORT }
  }
  const parts = trimmed.split(":")
  if (parts.length >= 2) {
    const port = parts[parts.length - 1] || DEFAULT_PORT
    const host = parts.slice(0, -1).join(":")
    return { host, port }
  }
  return { host: trimmed, port: DEFAULT_PORT }
}

function dedupe(items: string[]): string[] {
  const seen = new Set<string>()
  const out: string[] = []
  for (const item of items) {
    if (!item) continue
    if (seen.has(item)) continue
    seen.add(item)
    out.push(item)
  }
  return out
}

async function canReach(base: string): Promise<boolean> {
  const controller = new AbortController()
  const timeout = setTimeout(() => controller.abort(), 500)
  try {
    const res = await fetch(new URL(HEALTH_PATH, base).toString(), {
      signal: controller.signal,
    })
    return res.ok
  } catch {
    return false
  } finally {
    clearTimeout(timeout)
  }
}

function cacheBase(base: string) {
  const state = (globalThis as { state?: Record<string, unknown> }).state
  if (state && typeof state === "object") {
    state.api_base = base
  }
}

export async function getAPIBase(): Promise<string> {
  const state = (globalThis as { state?: Record<string, unknown> }).state
  const cached = state?.api_base
  if (typeof cached === "string" && cached.trim()) {
    return cached
  }

  const cfg = await loadConfig()
  const direct = cfg.http_addr ? normalizeHTTPAddr(cfg.http_addr) : undefined
  if (direct) {
    cacheBase(direct)
    return direct
  }

  const { host, port } = parseHostPort(cfg.http_addr || "")
  const docker = inDocker()
  const candidates: string[] = []

  if (host && host !== "0.0.0.0" && host !== "[::]") {
    candidates.push(`http://${host}:${port}`)
  }
  if (docker) {
    candidates.push(`http://agentd:${port}`)
  }
  candidates.push(`http://localhost:${port}`)
  if (!docker) {
    candidates.push(`http://agentd:${port}`)
  }

  const list = dedupe(candidates)
  for (const candidate of list) {
    if (await canReach(candidate)) {
      cacheBase(candidate)
      return candidate
    }
  }

  const fallback = list[0] || `http://localhost:${DEFAULT_PORT}`
  cacheBase(fallback)
  return fallback
}

export async function apiFetch(path: string, init?: RequestInit): Promise<Response> {
  const url = path.startsWith("http://") || path.startsWith("https://")
    ? path
    : new URL(path, await getAPIBase()).toString()
  return fetch(url, init)
}

export async function apiJSON<T = unknown>(path: string, init?: RequestInit): Promise<T> {
  const res = await apiFetch(path, init)
  if (!res.ok) {
    const text = await res.text().catch(() => "")
    throw new Error(`api request failed: ${res.status} ${text}`)
  }
  return (await res.json()) as T
}

export async function apiPostJSON<T = unknown>(path: string, body: unknown): Promise<T> {
  return apiJSON<T>(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  })
}
