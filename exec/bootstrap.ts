#!/usr/bin/env bun

import { mkdir } from "node:fs/promises"
import { dirname } from "node:path"
import { deserialize, serialize } from "node:v8"

function getArg(name: string): string | undefined {
  const idx = process.argv.indexOf(name)
  if (idx === -1) return undefined
  return process.argv[idx + 1]
}

const codeFile = getArg("--code-file") || process.env.EXEC_CODE_FILE
const resultPath = getArg("--result-path") || process.env.EXEC_RESULT_PATH
const bindingsPath = getArg("--bindings-path") || process.env.EXEC_BINDINGS_PATH
const userMessagesPath =
  getArg("--user-messages-path") || process.env.EXEC_USER_MESSAGES_PATH
const maxHistoryRaw =
  getArg("--bindings-max-history") || process.env.EXEC_BINDINGS_MAX_HISTORY
const maxHistory = Math.max(
  1,
  Number.isFinite(Number(maxHistoryRaw)) ? Number(maxHistoryRaw) : 200,
)

if (!codeFile) {
  console.error("exec bootstrap: --code-file is required")
  process.exit(1)
}

type ResultBindingsStore = {
  version: number
  base_index: number
  next_index: number
  values: unknown[]
}

function defaultStore(): ResultBindingsStore {
  return {
    version: 1,
    base_index: 1,
    next_index: 1,
    values: [],
  }
}

function normalizeStore(raw: unknown): ResultBindingsStore {
  if (!raw || typeof raw !== "object") return defaultStore()
  const typed = raw as Record<string, unknown>
  const baseIndex = Number(typed.base_index)
  const nextIndex = Number(typed.next_index)
  const values = Array.isArray(typed.values) ? typed.values : []
  const normalizedBase = Number.isFinite(baseIndex) && baseIndex > 0 ? Math.trunc(baseIndex) : 1
  const normalizedNext =
    Number.isFinite(nextIndex) && nextIndex >= normalizedBase
      ? Math.trunc(nextIndex)
      : normalizedBase + values.length
  return {
    version: 1,
    base_index: normalizedBase,
    next_index: normalizedNext,
    values: values,
  }
}

async function loadStore(path: string): Promise<ResultBindingsStore> {
  try {
    const file = Bun.file(path)
    if (!(await file.exists())) return defaultStore()
    const bytes = new Uint8Array(await file.arrayBuffer())
    if (bytes.byteLength === 0) return defaultStore()

    try {
      return normalizeStore(deserialize(Buffer.from(bytes)))
    } catch {
      // Fall back to JSON payloads from older/newer formats.
    }

    try {
      return normalizeStore(JSON.parse(new TextDecoder().decode(bytes)))
    } catch {
      return defaultStore()
    }
  } catch {
    return defaultStore()
  }
}

async function saveStore(path: string, store: ResultBindingsStore): Promise<void> {
  await mkdir(dirname(path), { recursive: true })
  try {
    const buf = serialize(store)
    await Bun.write(path, buf)
    return
  } catch {
    // Fall back to JSON if V8 serialization is unavailable.
  }
  await Bun.write(path, JSON.stringify(store, null, 2))
}

function cloneForStore(value: unknown): unknown {
  const normalized = value === undefined ? null : value
  try {
    return structuredClone(normalized)
  } catch {
    // Fall back to JSON-compatible representation.
  }
  try {
    return JSON.parse(JSON.stringify(normalized))
  } catch {
    return String(normalized)
  }
}

function exposeBindings(store: ResultBindingsStore): void {
  const g = globalThis as any
  for (let i = 0; i < store.values.length; i++) {
    const idx = store.base_index + i
    g[`$result${idx}`] = store.values[i]
  }
  g.$results = [...store.values]
  g.$last = store.values.length > 0 ? store.values[store.values.length - 1] : undefined
}

function appendBinding(
  store: ResultBindingsStore,
  value: unknown,
  maxEntries: number,
): ResultBindingsStore {
  const next = normalizeStore(store)
  next.values.push(cloneForStore(value))
  next.next_index = next.next_index + 1
  while (next.values.length > maxEntries) {
    next.values.shift()
    next.base_index = next.base_index + 1
  }
  return next
}

type UserMessage = {
  text: string
}

function normalizeUserMessage(raw: unknown): string {
  if (typeof raw === "string") return raw.trim()
  if (raw === null || raw === undefined) return ""
  return String(raw).trim()
}

const userMessages: UserMessage[] = []

;(globalThis as any).sendToUser = (text: unknown) => {
  const normalized = normalizeUserMessage(text)
  if (normalized === "") return
  userMessages.push({ text: normalized })
}

let store = defaultStore()
if (bindingsPath) {
  store = await loadStore(bindingsPath)
  exposeBindings(store)
}

;(globalThis as any).result = undefined

let importError: unknown = undefined
try {
  await import(codeFile)
} catch (err) {
  importError = err
  console.error(`exec bootstrap: code error: ${err}`)
}

const result = (globalThis as any).result

if (!importError && bindingsPath) {
  try {
    store = appendBinding(store, result, maxHistory)
    await saveStore(bindingsPath, store)
  } catch (err) {
    console.error(`exec bootstrap: failed to save bindings: ${err}`)
  }
}

if (resultPath) {
  try {
    const payload = { result: result === undefined ? null : result }
    await Bun.write(resultPath, JSON.stringify(payload, null, 2))
  } catch (err) {
    console.error(`exec bootstrap: failed to save result: ${err}`)
  }
}

if (userMessagesPath) {
  try {
    const payload = { messages: userMessages }
    await Bun.write(userMessagesPath, JSON.stringify(payload, null, 2))
  } catch (err) {
    console.error(`exec bootstrap: failed to save user messages: ${err}`)
  }
}

if (importError) {
  process.exit(1)
}
