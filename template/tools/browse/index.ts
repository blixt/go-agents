import { join } from "path"
import { existsSync } from "fs"

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export type SearchResult = { title: string; url: string; snippet: string }

export type BrowseResult = {
  status: string
  error_type?: string | null
  challenge_type?: string | null
  screenshot?: string
  url: string
  title?: string
  language?: string | null
  viewport?: { width: number; height: number }
  excerpt?: string
  images?: { url: string; source: string; score?: number }[]
  elements?: { id: string; tag: string; type: string | null; label: string; href: string | null; selector: string }[]
  elements_truncated?: boolean
  sections?: { index: number; subject: string; excerpt: string; level: number | null; selector: string }[]
  sections_truncated?: boolean
  metadata?: Record<string, string | null>
  session_id: string
  expires_at?: number
  ttl_ms?: number
}

export type ReadResult = {
  status: string
  error_type?: string | null
  challenge_type?: string | null
  screenshot?: string
  url: string
  title?: string
  language?: string | null
  viewport?: { width: number; height: number }
  markdown: string
  truncated: boolean
  sections?: { index: number; subject: string; excerpt: string; level: number | null; selector: string }[]
  sections_truncated?: boolean
  selected_section?: Record<string, unknown> | null
  images?: { url: string; source: string; score?: number }[]
  metadata?: Record<string, string | null>
  session_id: string
  expires_at?: number
  ttl_ms?: number
}

export type Action = {
  action: "click" | "double_click" | "fill" | "type" | "press" | "hover" | "focus" | "select" | "check" | "uncheck" | "scroll" | "wait"
  target?: string
  value?: string
  label?: string
  key?: string
  delay_ms?: number
  wait_ms?: number
  x?: number
  y?: number
}

export type InteractResult = {
  status: string
  error_type?: string | null
  url: string
  new_url?: string | null
  action_errors?: { index: number; action: string; error: string }[] | null
  content?: BrowseResult | null
  session_id: string
}

export type ScreenshotResult = {
  status: string
  url: string
  path: string
  session_id: string
}

// ---------------------------------------------------------------------------
// search() — zero deps, works standalone
// ---------------------------------------------------------------------------

export async function search(query: string, opts?: { maxResults?: number }): Promise<SearchResult[]> {
  const maxResults = opts?.maxResults ?? 8
  const res = await fetch("https://lite.duckduckgo.com/lite/", {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: `q=${encodeURIComponent(query)}`,
  })
  const html = await res.text()

  const linkRe = /<a[^>]+class=['\"]result-link['\"][^>]*href=['\"]([^'\"]*)['\"][^>]*>([\s\S]*?)<\/a>/gi
  const snippetRe = /<td[^>]+class=['\"]result-snippet['\"][^>]*>([\s\S]*?)<\/td>/gi

  const links: { url: string; title: string }[] = []
  let m: RegExpExecArray | null
  while ((m = linkRe.exec(html))) {
    links.push({ url: m[1].trim(), title: stripHtml(m[2]).trim() })
  }

  const snippets: string[] = []
  while ((m = snippetRe.exec(html))) {
    snippets.push(stripHtml(m[1]).trim())
  }

  const seen = new Set<string>()
  const results: SearchResult[] = []
  for (let i = 0; i < links.length && results.length < maxResults; i++) {
    const { url, title } = links[i]
    if (!url || seen.has(url)) continue
    seen.add(url)
    results.push({ title, url, snippet: snippets[i] || "" })
  }
  return results
}

function stripHtml(s: string): string {
  return decodeEntities(s.replace(/<[^>]*>/g, ""))
}

function decodeEntities(s: string): string {
  return s
    .replace(/&amp;/g, "&")
    .replace(/&lt;/g, "<")
    .replace(/&gt;/g, ">")
    .replace(/&quot;/g, '"')
    .replace(/&#x27;/g, "'")
    .replace(/&#39;/g, "'")
    .replace(/&apos;/g, "'")
    .replace(/&#(\d+);/g, (_, n) => String.fromCharCode(Number(n)))
    .replace(/&#x([0-9a-fA-F]+);/g, (_, n) => String.fromCharCode(parseInt(n, 16)))
}

// ---------------------------------------------------------------------------
// Browser client — thin HTTP wrappers to server.ts
// ---------------------------------------------------------------------------

const BROWSE_PORT = 3211
const SERVER_URL = `http://127.0.0.1:${BROWSE_PORT}`

async function ensureBrowser(): Promise<void> {
  // 1. Check if server is already running
  try {
    const r = await fetch(`${SERVER_URL}/ping`, { signal: AbortSignal.timeout(1000) })
    if (r.ok) return
  } catch {}

  const toolDir = import.meta.dir  // resolves through symlinks to ~/.go-agents/tools/browse/

  // 2. Ensure Chromium is installed (lazy, idempotent via marker file)
  const chromiumMarker = join(toolDir, ".chromium-installed")
  if (!existsSync(chromiumMarker)) {
    console.error("[browse] Installing Chromium (one-time)...")
    // Use the local playwright-core CLI (installed as rebrowser-playwright-core).
    // bunx would create a temp install missing peer deps, so we run the local binary directly.
    const cli = join(toolDir, "node_modules", "playwright-core", "cli.js")
    const result = Bun.spawnSync(
      ["bun", cli, "install", "chromium"],
      { cwd: toolDir, stdout: "inherit", stderr: "inherit" },
    )
    if (result.exitCode !== 0) throw new Error("Failed to install Chromium")
    await Bun.write(chromiumMarker, new Date().toISOString())
  }

  // 3. Spawn server with CWD = tool directory (so it finds node_modules/)
  console.error("[browse] Starting browser server...")
  const proc = Bun.spawn(["bun", "run", join(toolDir, "server.ts")], {
    cwd: toolDir,
    stdout: "ignore",
    stderr: "ignore",
  })
  proc.unref()

  // 4. Wait for server to be ready (up to 15s)
  const deadline = Date.now() + 15_000
  while (Date.now() < deadline) {
    await Bun.sleep(500)
    try {
      const r = await fetch(`${SERVER_URL}/ping`, { signal: AbortSignal.timeout(1000) })
      if (r.ok) return
    } catch {}
  }
  throw new Error("Browser server failed to start within 15s")
}

async function serverPost(command: string, payload: Record<string, unknown>): Promise<Record<string, unknown>> {
  await ensureBrowser()
  const res = await fetch(SERVER_URL, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ command, ...payload }),
  })
  return (await res.json()) as Record<string, unknown>
}

export async function browse(url: string, opts?: { sessionId?: string }): Promise<BrowseResult> {
  return (await serverPost("browse", { url, session_id: opts?.sessionId })) as unknown as BrowseResult
}

export async function read(opts: { sessionId?: string; url?: string; sectionIndex?: number }): Promise<ReadResult> {
  return (await serverPost("read", {
    session_id: opts.sessionId,
    url: opts.url,
    section_index: opts.sectionIndex,
  })) as unknown as ReadResult
}

export async function interact(
  sessionId: string,
  actions: Action[],
  opts?: { returnContent?: boolean },
): Promise<InteractResult> {
  return (await serverPost("interact", {
    session_id: sessionId,
    actions,
    return_content: opts?.returnContent,
  })) as unknown as InteractResult
}

export async function screenshot(
  sessionId: string,
  opts?: { target?: string; fullPage?: boolean },
): Promise<ScreenshotResult> {
  return (await serverPost("screenshot", {
    session_id: sessionId,
    target: opts?.target,
    full_page: opts?.fullPage,
  })) as unknown as ScreenshotResult
}

export async function close(sessionId: string): Promise<void> {
  await serverPost("close", { session_id: sessionId })
}
