export type EditResult = {
  replaced: number
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
