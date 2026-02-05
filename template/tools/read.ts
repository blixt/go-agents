export type ReadOptions = {
  encoding?: "utf-8" | "utf8"
}

export async function readText(path: string, options: ReadOptions = {}) {
  const encoding = options.encoding ?? "utf-8"
  return await Bun.file(path).text()
}

export async function readJSON<T = any>(path: string): Promise<T> {
  return await Bun.file(path).json()
}
