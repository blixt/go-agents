#!/usr/bin/env bun

function getArg(name: string): string | undefined {
  const idx = process.argv.indexOf(name)
  if (idx === -1) return undefined
  return process.argv[idx + 1]
}

const codeFile = getArg("--code-file") || process.env.EXEC_CODE_FILE
const resultPath = getArg("--result-path") || process.env.EXEC_RESULT_PATH

if (!codeFile) {
  console.error("exec bootstrap: --code-file is required")
  process.exit(1)
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

if (resultPath) {
  try {
    const payload = { result: result === undefined ? null : result }
    await Bun.write(resultPath, JSON.stringify(payload, null, 2))
  } catch (err) {
    console.error(`exec bootstrap: failed to save result: ${err}`)
  }
}

if (importError) {
  process.exit(1)
}
