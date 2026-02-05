export type EditResult = {
  replaced: number
}

export type PatchResult = {
  appliedHunks: number
  added: number
  removed: number
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
  const normalized = rawContent.replace(/\r\n/g, "\n")
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

  const output = lines.join("\n") + (hasTrailingNewline ? "\n" : "")
  await Bun.write(path, output)
  return { appliedHunks: hunks.length, added, removed }
}
