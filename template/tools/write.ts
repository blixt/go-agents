export async function writeText(path: string, content: string) {
  await Bun.write(path, content)
}

export async function appendText(path: string, content: string) {
  const file = Bun.file(path)
  const existing = (await file.exists()) ? await file.text() : ""
  await Bun.write(path, existing + content)
}

export async function writeJSON(path: string, value: any) {
  await Bun.write(path, JSON.stringify(value, null, 2))
}
