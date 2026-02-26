#!/usr/bin/env bun

import { dirname, join, resolve } from "path"
import { homedir, tmpdir } from "os"
import { existsSync, readdirSync, readFileSync } from "fs"
import { appendFile, mkdir, readdir, stat, symlink, copyFile } from "fs/promises"
import { startSupervisor, stopSupervisor } from "./service-supervisor.ts"

type Task = {
  id: string
  type: string
  status: string
  owner?: string
  metadata?: Record<string, unknown>
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
const pollArg = getArg("--poll-ms")
const webhookArg = getArg("--webhook-addr")
const parallelArg = getArg("--parallel")

const API_URL = normalizeHTTPAddr(apiURLArg || config.http_addr || ":8080")
const POLL_MS = parseInt(pollArg || "1000", 10)
const PARALLEL = Math.max(1, parseInt(parallelArg || Bun.env.EXEC_PARALLEL || "2", 10))
const webhookAddr = webhookArg || config.webhook_addr
const MAX_AGENT_STDERR_CHARS = 16 * 1024
const STREAM_FLUSH_INTERVAL_MS = 120
const STREAM_MAX_EVENT_CHARS = 8 * 1024
const STREAM_INLINE_BYTE_LIMIT = 64 * 1024
const RESULT_INLINE_BYTE_LIMIT = 64 * 1024
const MAX_BINDINGS_HISTORY = 200

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

function ensureToolDeps() {
  const toolsDir = join(GO_AGENTS_HOME, "tools")
  if (!existsSync(toolsDir)) return
  for (const entry of readdirSync(toolsDir, { withFileTypes: true })) {
    if (!entry.isDirectory()) continue
    const dir = join(toolsDir, entry.name)
    if (!existsSync(join(dir, "package.json"))) continue
    if (existsSync(join(dir, "node_modules"))) continue
    const result = Bun.spawnSync(["bun", "install"], {
      cwd: dir,
      stdout: "inherit",
      stderr: "inherit",
    })
    if (result.exitCode !== 0) {
      console.error(`[execd] bun install failed in ${dir}`)
    }
  }
}

function loadDotEnv(path: string): Record<string, string> {
  const vars: Record<string, string> = {}
  if (!existsSync(path)) return vars
  try {
    const text = readFileSync(path, "utf-8")
    for (const line of text.split("\n")) {
      const trimmed = line.trim()
      if (!trimmed || trimmed.startsWith("#")) continue
      const cleaned = trimmed.startsWith("export ") ? trimmed.slice(7).trim() : trimmed
      const eqIdx = cleaned.indexOf("=")
      if (eqIdx < 0) continue
      const key = cleaned.slice(0, eqIdx).trim()
      let value = cleaned.slice(eqIdx + 1).trim()
      if (
        (value.startsWith('"') && value.endsWith('"')) ||
        (value.startsWith("'") && value.endsWith("'"))
      ) {
        value = value.slice(1, -1)
      }
      if (key) vars[key] = value
    }
  } catch {
    // read error — that's fine
  }
  return vars
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

async function sendAssistantOutput(
  taskId: string,
  payload: {
    text: string
    source?: string
    from_task_id?: string
    request_id?: string
    service_id?: string
    context?: Record<string, unknown>
  },
) {
  await fetch(`${API_URL}/api/tasks/${taskId}/assistant_output`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
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

function taskNotifyTarget(task: Task): string {
  const owner = String(task.owner || "").trim()
  if (owner !== "") return owner
  const metadata = task.metadata && typeof task.metadata === "object" ? task.metadata : {}
  const notifyTarget = typeof metadata.notify_target === "string" ? metadata.notify_target.trim() : ""
  return notifyTarget
}

function sanitizePathKey(raw: string, fallback = "global"): string {
  raw = String(raw || "").trim().toLowerCase()
  if (raw === "") return fallback
  let out = ""
  for (const ch of raw) {
    const code = ch.charCodeAt(0)
    if ((code >= 97 && code <= 122) || (code >= 48 && code <= 57) || ch === "-" || ch === "_") {
      out += ch
    } else {
      out += "_"
    }
  }
  out = out.replace(/^_+|_+$/g, "")
  return out || fallback
}

function execBindingsPath(task: Task): string {
  const owner = taskNotifyTarget(task)
  const safeOwner = sanitizePathKey(owner, "global")
  return join(GO_AGENTS_HOME, "exec", "bindings", `${safeOwner}.bin`)
}

type FileArtifact = {
  abs: string
  rel: string
}

type TaskArtifacts = {
  dir: FileArtifact
  stdout: FileArtifact
  stderr: FileArtifact
  result: FileArtifact
}

function taskArtifacts(taskId: string): TaskArtifacts {
  const safeTaskID = sanitizePathKey(taskId, "task")
  const relDir = join("exec", "artifacts", safeTaskID)
  const absDir = join(GO_AGENTS_HOME, relDir)
  return {
    dir: { abs: absDir, rel: relDir },
    stdout: {
      abs: join(absDir, "stdout.log"),
      rel: join(relDir, "stdout.log"),
    },
    stderr: {
      abs: join(absDir, "stderr.log"),
      rel: join(relDir, "stderr.log"),
    },
    result: {
      abs: join(absDir, "result.json"),
      rel: join(relDir, "result.json"),
    },
  }
}

function textByteLength(text: string): number {
  return Buffer.byteLength(text, "utf8")
}

function clipTextByBytes(raw: string, maxBytes: number): { text: string; truncated: boolean } {
  const text = String(raw || "")
  if (text === "") return { text: "", truncated: false }
  if (maxBytes <= 0) return { text: "", truncated: text !== "" }
  if (textByteLength(text) <= maxBytes) return { text, truncated: false }
  let lo = 0
  let hi = text.length
  while (lo < hi) {
    const mid = Math.ceil((lo + hi) / 2)
    if (textByteLength(text.slice(0, mid)) <= maxBytes) {
      lo = mid
    } else {
      hi = mid - 1
    }
  }
  const clipped = text.slice(0, lo)
  return { text: clipped, truncated: clipped.length < text.length }
}

type StreamForwardResult = {
  captured: string
  truncated: boolean
  total_bytes: number
  overflowed: boolean
  output_file?: string
  output_file_abs?: string
}

async function forwardStream(
  taskId: string,
  kind: "stdout" | "stderr",
  stream: ReadableStream<Uint8Array> | null,
  opts?: {
    captureLimit?: number
    inlineByteLimit?: number
    outputFile?: FileArtifact
  },
): Promise<StreamForwardResult> {
  const outputFile = opts?.outputFile
  if (!stream) {
    return {
      captured: "",
      truncated: false,
      total_bytes: 0,
      overflowed: false,
      output_file: outputFile?.rel,
      output_file_abs: outputFile?.abs,
    }
  }
  const reader = stream.getReader()
  const decoder = new TextDecoder()
  const captureLimit = opts?.captureLimit || 0
  const inlineByteLimit = opts?.inlineByteLimit || STREAM_INLINE_BYTE_LIMIT
  let capture = ""
  let captureTruncated = false
  let pending = ""
  let lastFlushAt = 0
  let totalBytes = 0
  let emittedBytes = 0
  let overflowed = false
  let overflowNoticeSent = false
  let outputWriteFailed = false

  const writeOutput = async (text: string) => {
    if (!outputFile?.abs || outputWriteFailed || text === "") return
    try {
      await appendFile(outputFile.abs, text)
    } catch {
      outputWriteFailed = true
    }
  }

  const sendOverflowNotice = async () => {
    if (!overflowed || overflowNoticeSent) return
    overflowNoticeSent = true
    const payload: Record<string, unknown> = {
      truncated: true,
      overflow_to_file: !outputWriteFailed && !!outputFile?.rel,
      byte_limit: inlineByteLimit,
      bytes_seen: totalBytes,
    }
    if (!outputWriteFailed && outputFile?.rel) {
      payload.output_file = outputFile.rel
    }
    if (!outputWriteFailed && outputFile?.abs) {
      payload.output_file_abs = outputFile.abs
    }
    payload.text = outputWriteFailed
      ? `${kind} exceeded ${inlineByteLimit} bytes and inline streaming was truncated.`
      : `${kind} exceeded ${inlineByteLimit} bytes. Full output is available in ${outputFile?.rel}.`
    await sendUpdate(taskId, kind, payload)
  }

  const flush = async (force: boolean) => {
    if (pending === "") return
    while (pending !== "") {
      if (overflowed) {
        pending = ""
        await sendOverflowNotice()
        return
      }
      if (pending.trim() === "") {
        pending = ""
        return
      }
      const now = Date.now()
      if (!force && pending.length < STREAM_MAX_EVENT_CHARS && now - lastFlushAt < STREAM_FLUSH_INTERVAL_MS) {
        return
      }
      const chunk = pending.slice(0, STREAM_MAX_EVENT_CHARS)
      pending = pending.slice(chunk.length)
      if (chunk === "") {
        continue
      }
      const chunkBytes = textByteLength(chunk)
      if (inlineByteLimit > 0) {
        const remaining = inlineByteLimit - emittedBytes
        if (remaining <= 0) {
          overflowed = true
          pending = ""
          await sendOverflowNotice()
          return
        }
        if (chunkBytes > remaining) {
          const { text } = clipTextByBytes(chunk, remaining)
          if (text !== "") {
            await sendUpdate(taskId, kind, { text })
            emittedBytes += textByteLength(text)
            lastFlushAt = now
          }
          overflowed = true
          pending = ""
          await sendOverflowNotice()
          return
        }
      }
      await sendUpdate(taskId, kind, { text: chunk })
      emittedBytes += chunkBytes
      lastFlushAt = now
    }
  }

  while (true) {
    const { value, done } = await reader.read()
    if (done) break
    if (!value) continue
    const text = decoder.decode(value, { stream: true })
    if (text === "") continue
    totalBytes += textByteLength(text)
    await writeOutput(text)

    if (captureLimit > 0 && !captureTruncated) {
      const remaining = captureLimit - capture.length
      if (remaining <= 0) {
        captureTruncated = true
      } else if (text.length > remaining) {
        capture += text.slice(0, remaining)
        captureTruncated = true
      } else {
        capture += text
      }
    }
    if (!overflowed) {
      pending += text
      await flush(false)
    }
  }
  const tail = decoder.decode()
  if (tail !== "") {
    totalBytes += textByteLength(tail)
    await writeOutput(tail)
    if (captureLimit > 0 && !captureTruncated) {
      const remaining = captureLimit - capture.length
      if (remaining <= 0) {
        captureTruncated = true
      } else if (tail.length > remaining) {
        capture += tail.slice(0, remaining)
        captureTruncated = true
      } else {
        capture += tail
      }
    }
    if (!overflowed) {
      pending += tail
    }
  }
  await flush(true)
  await sendOverflowNotice()
  return {
    captured: capture.trim(),
    truncated: captureTruncated,
    total_bytes: totalBytes,
    overflowed,
    output_file: outputFile?.rel,
    output_file_abs: outputFile?.abs,
  }
}

async function runTask(task: Task) {
  const payload = task.payload || {}
  const code = typeof payload.code === "string" ? payload.code : ""

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

  const resultPath = join(execDir, "result.json")
  const userMessagesPath = join(execDir, "user_messages.json")
  const bindingsPath = execBindingsPath(task)
  const artifacts = taskArtifacts(task.id)
  await mkdir(dirname(bindingsPath), { recursive: true })
  await mkdir(artifacts.dir.abs, { recursive: true })
  await Bun.write(artifacts.stdout.abs, "")
  await Bun.write(artifacts.stderr.abs, "")

  await sendUpdate(task.id, "start", {})

  const cmd = [
    bunBin,
    bootstrapPath,
    "--code-file",
    codeFile,
    "--result-path",
    resultPath,
    "--bindings-path",
    bindingsPath,
    "--bindings-max-history",
    `${MAX_BINDINGS_HISTORY}`,
    "--user-messages-path",
    userMessagesPath,
  ]

  const dotEnvVars = loadDotEnv(join(GO_AGENTS_HOME, ".env"))
  const proc = Bun.spawn({
    cmd,
    cwd: GO_AGENTS_HOME,
    stdout: "pipe",
    stderr: "pipe",
    stdin: "pipe",
    env: {
      ...process.env,
      ...dotEnvVars,
      GO_AGENTS_HOME,
    },
  })

  const stdoutPromise = forwardStream(task.id, "stdout", proc.stdout, {
    inlineByteLimit: STREAM_INLINE_BYTE_LIMIT,
    outputFile: artifacts.stdout,
  })
  const stderrPromise = forwardStream(task.id, "stderr", proc.stderr, {
    captureLimit: MAX_AGENT_STDERR_CHARS,
    inlineByteLimit: STREAM_INLINE_BYTE_LIMIT,
    outputFile: artifacts.stderr,
  })
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
  const [stdoutCapture, stderrCapture] = await Promise.all([stdoutPromise, stderrPromise, inputPromise])

  await sendUpdate(task.id, "exit", { exit_code: exitCode })

  const notifyTarget = taskNotifyTarget(task)
  if (notifyTarget !== "") {
    try {
      const parsed = await Bun.file(userMessagesPath).json().catch(() => ({})) as {
        messages?: Array<{ text?: string }>
      }
      const source = task.id ? `exec:${task.id}` : "exec"
      const messages = Array.isArray(parsed.messages) ? parsed.messages : []
      for (const msg of messages) {
        const text = typeof msg?.text === "string" ? msg.text.trim() : ""
        if (text === "") continue
        await sendAssistantOutput(notifyTarget, {
          text,
          source,
          from_task_id: task.id,
        })
      }
    } catch (err) {
      await sendUpdate(task.id, "send_to_user_error", { error: String(err) })
    }
  }

  if (exitCode !== 0) {
    let error = `exec failed with exit code ${exitCode}`
    if (stderrCapture.captured) {
      error = `${error}\n${stderrCapture.captured}`.trim()
    }
    if (stderrCapture.overflowed && stderrCapture.output_file) {
      error = `${error}\nFull stderr: ${stderrCapture.output_file}`.trim()
    }
    if (stdoutCapture.overflowed && stdoutCapture.output_file) {
      error = `${error}\nFull stdout: ${stdoutCapture.output_file}`.trim()
    }
    await sendFail(task.id, error)
    return
  }

  let result: Record<string, unknown> = {}
  try {
    const resultInfo = await stat(resultPath)
    if (resultInfo.size > RESULT_INLINE_BYTE_LIMIT) {
      try {
        await copyFile(resultPath, artifacts.result.abs)
        result = {
          result_too_large: true,
          message: `Result exceeded ${RESULT_INLINE_BYTE_LIMIT} bytes. Read ${artifacts.result.rel}.`,
          result_file: artifacts.result.rel,
          result_file_abs: artifacts.result.abs,
          result_bytes: resultInfo.size,
          result_byte_limit: RESULT_INLINE_BYTE_LIMIT,
        }
      } catch (copyErr) {
        result = {
          error: `result exceeded inline limit (${RESULT_INLINE_BYTE_LIMIT} bytes) and failed to spill: ${copyErr}`,
          result_bytes: resultInfo.size,
          result_byte_limit: RESULT_INLINE_BYTE_LIMIT,
        }
      }
      await sendComplete(task.id, result)
      return
    }
    const data = await Bun.file(resultPath).text()
    if (data.trim() !== "") {
      const parsed = JSON.parse(data)
      if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
        if (
          Object.keys(parsed).length === 1 &&
          Object.prototype.hasOwnProperty.call(parsed, "result") &&
          parsed.result &&
          typeof parsed.result === "object" &&
          !Array.isArray(parsed.result)
        ) {
          result = parsed.result as Record<string, unknown>
        } else {
          result = parsed as Record<string, unknown>
        }
      } else {
        result = { result: parsed }
      }
    }
  } catch (err) {
    result = { error: `failed to read result: ${err}` }
  }

  await sendComplete(task.id, result)
}

async function main() {
  await ensureGoAgentsHome()
  ensureToolDeps()
  startWebhookServer()
  startSupervisor(GO_AGENTS_HOME)

  const shutdown = () => {
    stopSupervisor()
    process.exit(0)
  }
  process.on("SIGINT", shutdown)
  process.on("SIGTERM", shutdown)

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
