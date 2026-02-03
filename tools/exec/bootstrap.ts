#!/usr/bin/env bun

type ExecState = Record<string, unknown>

function getArg(name: string): string | undefined {
  const idx = process.argv.indexOf(name)
  if (idx === -1) return undefined
  return process.argv[idx + 1]
}

const codeFile = getArg("--code-file") || process.env.EXEC_CODE_FILE
const snapshotIn = getArg("--snapshot-in") || process.env.EXEC_SNAPSHOT_IN
const snapshotOut = getArg("--snapshot-out") || process.env.EXEC_SNAPSHOT_OUT
const resultPath = getArg("--result-path") || process.env.EXEC_RESULT_PATH

if (!codeFile) {
  console.error("exec bootstrap: --code-file is required")
  process.exit(1)
}

const state: ExecState = {}

if (snapshotIn) {
  try {
    const data = await Bun.file(snapshotIn).text()
    if (data.trim() !== "") {
      const parsed = JSON.parse(data)
      if (parsed && typeof parsed === "object") {
        Object.assign(state, parsed)
      }
    }
  } catch (err) {
    console.error(`exec bootstrap: failed to load snapshot: ${err}`)
  }
}

;(globalThis as any).state = state
;(globalThis as any).result = undefined

let importError: unknown = undefined
try {
  await import(codeFile)
} catch (err) {
  importError = err
  console.error(`exec bootstrap: code error: ${err}`)
}

const finalState = (globalThis as any).state ?? state
const result = (globalThis as any).result

if (snapshotOut) {
  try {
    await Bun.write(snapshotOut, JSON.stringify(finalState, null, 2))
  } catch (err) {
    console.error(`exec bootstrap: failed to save snapshot: ${err}`)
  }
}

if (resultPath) {
  try {
    await Bun.write(resultPath, JSON.stringify({ state: finalState, result }, null, 2))
  } catch (err) {
    console.error(`exec bootstrap: failed to save result: ${err}`)
  }
}

if (importError) {
  process.exit(1)
}
