#!/usr/bin/env bun

import { join, resolve } from "path"
import { tmpdir } from "os"

type Task = {
  id: string
  type: string
  status: string
  payload?: Record<string, unknown>
}

const API_URL = process.env.GO_AGENTS_API_URL || "http://localhost:8080"
const SNAPSHOT_DIR = process.env.GO_AGENTS_SNAPSHOT_DIR || "data/exec-snapshots"
const POLL_MS = parseInt(process.env.GO_AGENTS_POLL_MS || "1000", 10)
const bootstrapPath = resolve(import.meta.dir, "bootstrap.ts")
const once = process.argv.includes("--once") || process.env.GO_AGENTS_ONCE === "1"
const webhookAddr = process.env.GO_AGENTS_WEBHOOK_ADDR

async function sleep(ms: number) {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

async function claimTasks(): Promise<Task[]> {
  const url = `${API_URL}/api/tasks/queue?type=exec&limit=1`
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
  await Bun.mkdir(execDir, { recursive: true })

  const codeFile = join(execDir, "task.ts")
  await Bun.write(codeFile, code)

  let snapshotPath = ""
  if (sessionId) {
    snapshotPath = join(SNAPSHOT_DIR, `${sessionId}.json`)
  }
  const resultPath = join(execDir, "result.json")

  await sendUpdate(task.id, "start", { session_id: sessionId })

  const cmd = [
    "bun",
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
    stdout: "pipe",
    stderr: "pipe",
    env: {
      ...process.env,
    },
  })

  const stdoutPromise = forwardStream(task.id, "stdout", proc.stdout)
  const stderrPromise = forwardStream(task.id, "stderr", proc.stderr)

  const exitCode = await proc.exited
  await Promise.all([stdoutPromise, stderrPromise])

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

async function runOnceCycle() {
  const tasks = await claimTasks()
  if (tasks && tasks.length > 0) {
    for (const task of tasks) {
      await runTask(task)
    }
  }
  return tasks
}

async function main() {
  startWebhookServer()
  while (true) {
    try {
      const tasks = await runOnceCycle()
      if (once) return
      if (!tasks || tasks.length === 0) {
        await sleep(POLL_MS)
      }
    } catch (err) {
      console.error(`execd error: ${err}`)
      if (once) return
      await sleep(POLL_MS)
    }
  }
}

await main()
