export type EditResult = {
  replaced: number
}

export type PatchResult = {
  appliedHunks: number
  added: number
  removed: number
}

export type DiffResult = {
  diff: string
  firstChangedLine?: number
}

type Op = {
  type: "equal" | "insert" | "delete"
  line: string
}

function detectLineEnding(text: string) {
  return text.includes("\r\n") ? "\r\n" : "\n"
}

function normalizeNewlines(text: string) {
  return text.replace(/\r\n/g, "\n")
}

function splitLines(text: string) {
  const normalized = normalizeNewlines(text)
  if (normalized.endsWith("\n")) {
    return normalized.slice(0, -1).split("\n")
  }
  return normalized.split("\n")
}

function joinLines(lines: string[], hasTrailingNewline: boolean, lineEnding: "\n" | "\r\n") {
  let output = lines.join("\n") + (hasTrailingNewline ? "\n" : "")
  if (lineEnding === "\r\n") {
    output = output.replace(/\n/g, "\r\n")
  }
  return output
}

function normalizeForFuzzyMatch(line: string) {
  return line.replace(/\s+/g, " ").trim()
}

function countOccurrences(haystack: string, needle: string) {
  if (!needle) {
    return 0
  }
  return haystack.split(needle).length - 1
}

export async function replaceText(path: string, oldText: string, newText: string): Promise<EditResult> {
  const file = Bun.file(path)
  if (!(await file.exists())) {
    throw new Error(`File not found: ${path}`)
  }
  const content = await file.text()
  if (!content.includes(oldText)) {
    throw new Error(`Old text not found in ${path}`)
  }
  const count = content.split(oldText).length - 1
  if (count > 1) {
    throw new Error(`Old text occurs ${count} times in ${path}; provide a unique match`)
  }
  const updated = content.replace(oldText, newText)
  await Bun.write(path, updated)
  return { replaced: 1 }
}

export async function replaceAllText(path: string, oldText: string, newText: string): Promise<EditResult> {
  const file = Bun.file(path)
  if (!(await file.exists())) {
    throw new Error(`File not found: ${path}`)
  }
  const content = await file.text()
  if (!content.includes(oldText)) {
    throw new Error(`Old text not found in ${path}`)
  }
  const count = content.split(oldText).length - 1
  const updated = content.split(oldText).join(newText)
  await Bun.write(path, updated)
  return { replaced: count }
}

export async function replaceTextFuzzy(path: string, oldText: string, newText: string): Promise<EditResult> {
  const file = Bun.file(path)
  if (!(await file.exists())) {
    throw new Error(`File not found: ${path}`)
  }

  const content = await file.text()
  const lineEnding = detectLineEnding(content)
  const hasTrailingNewline = content.endsWith("\n")
  const normalizedContent = normalizeNewlines(content)
  const normalizedOld = normalizeNewlines(oldText)
  const normalizedNew = normalizeNewlines(newText)

  const exactCount = countOccurrences(normalizedContent, normalizedOld)
  if (exactCount === 1) {
    const updated = normalizedContent.replace(normalizedOld, normalizedNew)
    const output = joinLines(splitLines(updated), hasTrailingNewline, lineEnding)
    await Bun.write(path, output)
    return { replaced: 1 }
  }
  if (exactCount > 1) {
    throw new Error(`Old text occurs ${exactCount} times in ${path}; provide a unique match`)
  }

  const fileLines = splitLines(content)
  const oldLines = splitLines(oldText)
  const newLines = splitLines(newText)

  if (oldLines.length === 0) {
    throw new Error("Old text is empty")
  }

  const normalizedOldLines = oldLines.map(normalizeForFuzzyMatch)
  const normalizedOldBlock = normalizedOldLines.join("\n")

  let matchIndex = -1
  let matches = 0

  for (let i = 0; i <= fileLines.length - oldLines.length; i++) {
    const candidate = fileLines.slice(i, i + oldLines.length)
    const normalizedCandidate = candidate.map(normalizeForFuzzyMatch).join("\n")
    if (normalizedCandidate === normalizedOldBlock) {
      matches++
      matchIndex = i
    }
  }

  if (matches === 0) {
    throw new Error(`Old text not found in ${path} (fuzzy match failed)`)
  }
  if (matches > 1) {
    throw new Error(`Old text matches ${matches} locations in ${path}; provide more context`)
  }

  fileLines.splice(matchIndex, oldLines.length, ...newLines)
  const output = joinLines(fileLines, hasTrailingNewline, lineEnding)
  await Bun.write(path, output)
  return { replaced: 1 }
}

type Hunk = {
  oldStart: number
  oldCount: number
  newStart: number
  newCount: number
  lines: string[]
}

function parseUnifiedDiff(diff: string): Hunk[] {
  const text = diff.replace(/\r\n/g, "\n")
  const lines = text.split("\n")
  const hunks: Hunk[] = []
  const headerRe = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/
  let i = 0
  while (i < lines.length) {
    const line = lines[i]
    const match = headerRe.exec(line)
    if (!match) {
      i++
      continue
    }
    const oldStart = Number(match[1])
    const oldCount = match[2] ? Number(match[2]) : 1
    const newStart = Number(match[3])
    const newCount = match[4] ? Number(match[4]) : 1
    i++
    const hunkLines: string[] = []
    while (i < lines.length && !headerRe.test(lines[i])) {
      hunkLines.push(lines[i])
      i++
    }
    hunks.push({ oldStart, oldCount, newStart, newCount, lines: hunkLines })
  }
  return hunks
}

export async function applyUnifiedDiff(path: string, diff: string): Promise<PatchResult> {
  const file = Bun.file(path)
  if (!(await file.exists())) {
    throw new Error(`File not found: ${path}`)
  }
  const rawContent = await file.text()
  const hasTrailingNewline = rawContent.endsWith("\n")
  const lineEnding = detectLineEnding(rawContent)
  const normalized = normalizeNewlines(rawContent)
  let lines = normalized.split("\n")
  if (hasTrailingNewline) {
    lines = lines.slice(0, -1)
  }

  const hunks = parseUnifiedDiff(diff)
  if (hunks.length === 0) {
    throw new Error("No hunks found in diff")
  }

  let offset = 0
  let added = 0
  let removed = 0

  for (const hunk of hunks) {
    let idx = Math.max(0, hunk.oldStart - 1 + offset)
    for (const hunkLine of hunk.lines) {
      if (hunkLine.startsWith(" ")) {
        const expected = hunkLine.slice(1)
        if (lines[idx] !== expected) {
          throw new Error(`Context mismatch applying diff at ${path}`)
        }
        idx++
        continue
      }
      if (hunkLine.startsWith("-")) {
        const expected = hunkLine.slice(1)
        if (lines[idx] !== expected) {
          throw new Error(`Remove mismatch applying diff at ${path}`)
        }
        lines.splice(idx, 1)
        removed++
        offset--
        continue
      }
      if (hunkLine.startsWith("+")) {
        const value = hunkLine.slice(1)
        lines.splice(idx, 0, value)
        idx++
        added++
        offset++
        continue
      }
      if (hunkLine.startsWith("\\")) {
        continue
      }
      throw new Error(`Invalid diff line: ${hunkLine}`)
    }
  }

  const output = joinLines(lines, hasTrailingNewline, lineEnding)
  await Bun.write(path, output)
  return { appliedHunks: hunks.length, added, removed }
}

function diffLines(oldLines: string[], newLines: string[]): Op[] {
  const n = oldLines.length
  const m = newLines.length
  const maxCells = 2_000_000
  if (n * m > maxCells) {
    throw new Error("Diff too large to compute; reduce file size or use external diff tooling")
  }

  const dp: Uint32Array[] = new Array(n + 1)
  for (let i = 0; i <= n; i++) {
    dp[i] = new Uint32Array(m + 1)
  }

  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      if (oldLines[i] === newLines[j]) {
        dp[i][j] = dp[i + 1][j + 1] + 1
      } else {
        dp[i][j] = Math.max(dp[i + 1][j], dp[i][j + 1])
      }
    }
  }

  const ops: Op[] = []
  let i = 0
  let j = 0

  while (i < n && j < m) {
    if (oldLines[i] === newLines[j]) {
      ops.push({ type: "equal", line: oldLines[i] })
      i++
      j++
    } else if (dp[i + 1][j] >= dp[i][j + 1]) {
      ops.push({ type: "delete", line: oldLines[i] })
      i++
    } else {
      ops.push({ type: "insert", line: newLines[j] })
      j++
    }
  }

  while (i < n) {
    ops.push({ type: "delete", line: oldLines[i] })
    i++
  }
  while (j < m) {
    ops.push({ type: "insert", line: newLines[j] })
    j++
  }

  return ops
}

function buildUnifiedDiff(
  ops: Op[],
  options: { context?: number; path?: string } = {},
): DiffResult {
  const context = options.context ?? 3
  if (ops.length === 0) {
    return { diff: "" }
  }

  const oldLineAt: number[] = []
  const newLineAt: number[] = []
  let oldLine = 1
  let newLine = 1

  for (const op of ops) {
    oldLineAt.push(oldLine)
    newLineAt.push(newLine)
    if (op.type !== "insert") {
      oldLine++
    }
    if (op.type !== "delete") {
      newLine++
    }
  }

  const hunks: string[] = []
  let firstChangedLine: number | undefined
  let i = 0

  while (i < ops.length) {
    while (i < ops.length && ops[i].type === "equal") {
      i++
    }
    if (i >= ops.length) {
      break
    }

    const hunkStart = Math.max(0, i - context)
    let j = i
    let lastChange = i
    while (j < ops.length) {
      if (ops[j].type !== "equal") {
        lastChange = j
      }
      if (j - lastChange > context) {
        break
      }
      j++
    }

    const hunkEnd = j
    const oldStart = oldLineAt[hunkStart]
    const newStart = newLineAt[hunkStart]
    let oldCount = 0
    let newCount = 0

    for (let k = hunkStart; k < hunkEnd; k++) {
      if (ops[k].type !== "insert") {
        oldCount++
      }
      if (ops[k].type !== "delete") {
        newCount++
      }
    }

    hunks.push(`@@ -${oldStart},${oldCount} +${newStart},${newCount} @@`)
    for (let k = hunkStart; k < hunkEnd; k++) {
      const op = ops[k]
      if (op.type === "equal") {
        hunks.push(` ${op.line}`)
      } else if (op.type === "delete") {
        hunks.push(`-${op.line}`)
        if (firstChangedLine === undefined) {
          firstChangedLine = newLineAt[k]
        }
      } else {
        hunks.push(`+${op.line}`)
        if (firstChangedLine === undefined) {
          firstChangedLine = newLineAt[k]
        }
      }
    }

    i = hunkEnd
  }

  if (hunks.length === 0) {
    return { diff: "" }
  }

  const header = options.path
    ? [`--- a/${options.path}`, `+++ b/${options.path}`]
    : ["--- a/file", "+++ b/file"]

  return { diff: header.concat(hunks).join("\n"), firstChangedLine }
}

export function generateUnifiedDiff(
  oldText: string,
  newText: string,
  options: { context?: number; path?: string } = {},
): DiffResult {
  const oldLines = splitLines(oldText)
  const newLines = splitLines(newText)
  if (oldLines.length === 1 && oldLines[0] === "" && newLines.length === 1 && newLines[0] === "") {
    return { diff: "" }
  }
  const ops = diffLines(oldLines, newLines)
  return buildUnifiedDiff(ops, options)
}
