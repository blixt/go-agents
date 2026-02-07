#!/usr/bin/env bun

import { join, resolve } from "path"
import { homedir, tmpdir } from "os"
import { mkdir, readdir, stat, symlink, copyFile } from "fs/promises"

type Task = {
  id: string
  type: string
  status: string
  payload?: Record<string, unknown>
}

type TaskUpdate = {
  id: string
  task_id: string
  kind: string
  payload?: Record<string, unknown>
}

type ConfigFile = {
  http_addr?: string
  snapshot_dir?: string
  webhook_addr?: string
}

const bootstrapPath = resolve(import.meta.dir, "bootstrap.ts")
const bunBin = process.execPath || "bun"
const GO_AGENTS_HOME = Bun.env.GO_AGENTS_HOME || join(homedir(), ".go-agents")
const TEMPLATE_ROOT = resolve(import.meta.dir, "..", "template")
const once = process.argv.includes("--once")

function getArg(name: string): string | undefined {
  const direct = process.argv.find((arg) => arg.startsWith(`${name}=`))
  if (direct) {
    return direct.split("=", 2)[1]
  }
  const idx = process.argv.indexOf(name)
  if (idx >= 0 && idx + 1 < process.argv.length) {
    return process.argv[idx + 1]
  }
  return undefined
}

function normalizeHTTPAddr(addr: string): string {
  if (addr.startsWith("http://") || addr.startsWith("https://")) {
    return addr
  }
  if (addr.startsWith(":")) {
    return `http://localhost${addr}`
  }
  if (addr.startsWith("0.0.0.0:")) {
    return `http://localhost:${addr.split(":")[1]}`
  }
  if (addr.startsWith("[::]:")) {
    return `http://localhost:${addr.split(":")[2]}`
  }
  return `http://${addr}`
}

async function loadConfig(): Promise<ConfigFile> {
  const paths = ["config.json", "data/config.json"]
  for (const path of paths) {
    try {
      const text = await Bun.file(path).text()
      if (!text.trim()) continue
      return JSON.parse(text) as ConfigFile
    } catch {
      // ignore missing/invalid
    }
  }
  return {}
}

const config = await loadConfig()
const apiURLArg = getArg("--api-url")
const snapshotArg = getArg("--snapshot-dir")
const pollArg = getArg("--poll-ms")
const webhookArg = getArg("--webhook-addr")
const parallelArg = getArg("--parallel")

const API_URL = normalizeHTTPAddr(apiURLArg || config.http_addr || ":8080")
const SNAPSHOT_DIR = snapshotArg || config.snapshot_dir || "data/exec-snapshots"
const POLL_MS = parseInt(pollArg || "1000", 10)
const PARALLEL = Math.max(1, parseInt(parallelArg || Bun.env.EXEC_PARALLEL || "2", 10))
const webhookAddr = webhookArg || config.webhook_addr

process.env.GO_AGENTS_API_URL = API_URL

function startWebhookServer() {
  if (!webhookAddr) return
  const [host, portStr] = webhookAddr.split(":")
  const port = parseInt(portStr || host, 10)
  const hostname = portStr ? host : "127.0.0.1"

  Bun.serve({
    hostname,
    port,
    async fetch(req) {
      if (req.method !== "POST" || new URL(req.url).pathname !== "/dispatch") {
        return new Response("not found", { status: 404 })
      }
      const body = (await req.json()) as Task
      await runTask(body)
      return new Response("ok")
    },
  })
}

async function sleep(ms: number) {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

async function copyDir(src: string, dest: string) {
  await mkdir(dest, { recursive: true })
  const entries = await readdir(src, { withFileTypes: true })
  for (const entry of entries) {
    const from = join(src, entry.name)
    const to = join(dest, entry.name)
    if (entry.isDirectory()) {
      await copyDir(from, to)
    } else if (entry.isFile()) {
      if (entry.name.endsWith(".go")) {
        continue
      }
      await mkdir(join(to, ".."), { recursive: true })
      await copyFile(from, to)
    }
  }
}

async function ensureGoAgentsHome() {
  try {
    const info = await stat(GO_AGENTS_HOME)
    if (!info.isDirectory()) {
      throw new Error(`${GO_AGENTS_HOME} exists and is not a directory`)
    }
    return
  } catch (err: any) {
    if (err?.code !== "ENOENT") throw err
  }
  await copyDir(TEMPLATE_ROOT, GO_AGENTS_HOME)
}

async function claimTasks(limit: number): Promise<Task[]> {
  const url = `${API_URL}/api/tasks/queue?type=exec&limit=${limit}`
  const res = await fetch(url)
  if (!res.ok) {
    throw new Error(`queue request failed: ${res.status}`)
  }
  return (await res.json()) as Task[]
}

async function sendUpdate(taskId: string, kind: string, payload: Record<string, unknown>) {
  await fetch(`${API_URL}/api/tasks/${taskId}/updates`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ kind, payload }),
  })
}

async function sendComplete(taskId: string, result: Record<string, unknown>) {
  await fetch(`${API_URL}/api/tasks/${taskId}/complete`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ result }),
  })
}

async function sendFail(taskId: string, error: string) {
  await fetch(`${API_URL}/api/tasks/${taskId}/fail`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ error }),
  })
}

async function fetchInputs(taskId: string, afterId: string) {
  const params = new URLSearchParams()
  params.set("kind", "input")
  params.set("limit", "50")
  if (afterId) {
    params.set("after_id", afterId)
  }
  const res = await fetch(`${API_URL}/api/tasks/${taskId}/updates?${params.toString()}`)
  if (!res.ok) {
    throw new Error(`inputs request failed: ${res.status}`)
  }
  return (await res.json()) as TaskUpdate[]
}

async function forwardStream(taskId: string, kind: string, stream: ReadableStream<Uint8Array> | null) {
  if (!stream) return
  const reader = stream.getReader()
  const decoder = new TextDecoder()
  while (true) {
    const { value, done } = await reader.read()
    if (done) break
    const text = decoder.decode(value)
    if (text.trim() === "") continue
    await sendUpdate(taskId, kind, { text })
  }
}

async function runTask(task: Task) {
  const payload = task.payload || {}
  const code = typeof payload.code === "string" ? payload.code : ""
  const sessionId = typeof payload.id === "string" ? payload.id : ""

  if (!code.trim()) {
    await sendFail(task.id, "exec task missing code")
    return
  }

  const execDir = join(tmpdir(), `go-agents-${task.id}`)
  await mkdir(execDir, { recursive: true })
  const nodeModulesDir = join(execDir, "node_modules")
  await mkdir(nodeModulesDir, { recursive: true })
  const toolsSource = join(GO_AGENTS_HOME, "tools")
  const coreSource = join(GO_AGENTS_HOME, "core")
  const toolsTarget = join(nodeModulesDir, "tools")
  const coreTarget = join(nodeModulesDir, "core")
  try {
    await symlink(toolsSource, toolsTarget, "dir")
  } catch {
    // ignore if already exists or symlink not supported
  }
  try {
    await symlink(coreSource, coreTarget, "dir")
  } catch {
    // ignore if already exists or symlink not supported
  }

  const codeFile = join(execDir, "task.ts")
  await Bun.write(codeFile, code)

  let snapshotPath = ""
  if (sessionId) {
    snapshotPath = join(SNAPSHOT_DIR, `${sessionId}.json`)
  }
  const resultPath = join(execDir, "result.json")

  await sendUpdate(task.id, "start", { session_id: sessionId })

  const cmd = [
    bunBin,
    bootstrapPath,
    "--code-file",
    codeFile,
    "--result-path",
    resultPath,
  ]
  if (snapshotPath) {
    cmd.push("--snapshot-in", snapshotPath)
    cmd.push("--snapshot-out", snapshotPath)
  }

  const proc = Bun.spawn({
    cmd,
    cwd: GO_AGENTS_HOME,
    stdout: "pipe",
    stderr: "pipe",
    stdin: "pipe",
    env: {
      ...process.env,
      GO_AGENTS_HOME,
    },
  })

  const stdoutPromise = forwardStream(task.id, "stdout", proc.stdout)
  const stderrPromise = forwardStream(task.id, "stderr", proc.stderr)
  const inputPromise = (async () => {
    const stdin = proc.stdin as
      | undefined
      | { getWriter?: () => { write: (chunk: Uint8Array) => Promise<void>; close?: () => Promise<void> } }
      | { write?: (chunk: Uint8Array) => boolean; once?: (event: string, cb: () => void) => void; end?: () => void }
    if (!stdin) return

    let writeChunk: ((chunk: Uint8Array) => Promise<void>) | null = null
    let closeWriter: (() => Promise<void> | void) | null = null

    if (typeof stdin.getWriter === "function") {
      const writer = stdin.getWriter()
      writeChunk = (chunk) => writer.write(chunk)
      closeWriter = () => writer.close?.()
    } else if (typeof (stdin as any).write === "function") {
      const stream = stdin as any
      writeChunk = (chunk) =>
        new Promise<void>((resolve) => {
          const ok = stream.write(chunk)
          if (ok) {
            resolve()
            return
          }
          if (typeof stream.once === "function") {
            stream.once("drain", resolve)
          } else {
            resolve()
          }
        })
      closeWriter = () => stream.end?.()
    }

    if (!writeChunk) return

    const encoder = new TextEncoder()
    let afterId = ""
    let done = false
    proc.exited.then(() => {
      done = true
    })
    while (!done) {
      try {
        const updates = await fetchInputs(task.id, afterId)
        if (updates.length > 0) {
          afterId = updates[updates.length - 1].id
        }
        for (const upd of updates) {
          const payload = upd.payload || {}
          let text = ""
          if (typeof payload.text === "string") {
            text = payload.text
          } else if (payload !== null && payload !== undefined) {
            text = JSON.stringify(payload)
          }
          if (text !== "") {
            await writeChunk(encoder.encode(text + "\n"))
          }
        }
      } catch {
        // ignore transient fetch errors
      }
      await sleep(200)
    }
    try {
      await closeWriter?.()
    } catch {
      // ignore
    }
  })().catch(async (err) => {
    await sendUpdate(task.id, "input_error", { error: String(err) })
  })

  const exitCode = await proc.exited
  await Promise.all([stdoutPromise, stderrPromise, inputPromise])

  await sendUpdate(task.id, "exit", { exit_code: exitCode })

  if (exitCode !== 0) {
    await sendFail(task.id, `exec failed with exit code ${exitCode}`)
    return
  }

  let result: Record<string, unknown> = {}
  try {
    const data = await Bun.file(resultPath).text()
    if (data.trim() !== "") {
      result = JSON.parse(data)
    }
  } catch (err) {
    result = { error: `failed to read result: ${err}` }
  }

  await sendComplete(task.id, result)
}

async function main() {
  await ensureGoAgentsHome()
  startWebhookServer()
  const running = new Set<Promise<void>>()
  while (true) {
    try {
      while (running.size < PARALLEL) {
        const capacity = PARALLEL - running.size
        const tasks = await claimTasks(capacity)
        if (!tasks || tasks.length === 0) {
          break
        }
        for (const task of tasks) {
          const p = runTask(task).catch((err) => {
            console.error(`execd error: ${err}`)
          })
          running.add(p)
          p.finally(() => running.delete(p))
          if (once) break
        }
        if (once) break
      }
      if (once) {
        if (running.size === 0) return
      }
      if (running.size === 0) {
        await sleep(POLL_MS)
        continue
      }
      await Promise.race([sleep(POLL_MS), ...running])
    } catch (err) {
      console.error(`execd error: ${err}`)
      if (once && running.size === 0) return
      await sleep(POLL_MS)
    }
  }
}

await main()
