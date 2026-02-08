import { JSDOM } from "jsdom"
import { Readability } from "@mozilla/readability"
import TurndownService from "turndown"
import { chromium, type BrowserContext, type Page, type Response as PWResponse } from "rebrowser-playwright"
import { join } from "path"
import { homedir } from "os"
import { mkdir } from "fs/promises"
import crypto from "crypto"

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const CHROME_VERSION = "133"
const USER_AGENT = `Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/${CHROME_VERSION}.0.0.0 Safari/537.36`

const DEFAULT_TIMEOUT_MS = 20_000
const DEFAULT_MAX_IMAGES = 3
const DEFAULT_MAX_ELEMENTS = 40
const DEFAULT_MAX_SECTIONS = 50
const DEFAULT_SECTION_EXCERPT_CHARS = 280
const DEFAULT_SECTION_MARKDOWN_CHARS = 6000
const DEFAULT_MAX_MARKDOWN_CHARS = 20_000
const DEFAULT_MAX_SECTION_LINKS = 25

const DEFAULT_LOCALE = "en-US"
const DEFAULT_TIMEZONE = "America/Los_Angeles"
const DEFAULT_VIEWPORT = { width: 1440, height: 900 }

const DEFAULT_HEADERS: Record<string, string> = {
  accept: "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8",
  "accept-language": "en-US,en;q=0.9",
  "cache-control": "max-age=0",
  "upgrade-insecure-requests": "1",
  "sec-ch-ua": `"Chromium";v="${CHROME_VERSION}", "Google Chrome";v="${CHROME_VERSION}", "Not-A.Brand";v="99"`,
  "sec-ch-ua-mobile": "?0",
  "sec-ch-ua-platform": `"macOS"`,
}

const BLOCKED_MARKERS = [
  "verify you are human",
  "unusual traffic",
  "access denied",
  "request blocked",
  "cloudflare",
  "please enable javascript",
  "enable javascript",
  "bot detection",
]

const PORT = Number.parseInt(process.env.BROWSE_PORT || "3211", 10)
const SESSION_TTL_MS = 120_000
const CLEANUP_INTERVAL_MS = 30_000

const SCREENSHOT_DIR = join(homedir(), ".go-agents", "screenshots")

// ---------------------------------------------------------------------------
// Stealth init script
// ---------------------------------------------------------------------------

const STEALTH_SCRIPT = `
  // 1. Remove webdriver flag
  Object.defineProperty(navigator, 'webdriver', { get: () => undefined });

  // 2. Add chrome.runtime (missing in automation)
  if (!window.chrome) window.chrome = {};
  if (!window.chrome.runtime) {
    window.chrome.runtime = {
      connect: () => {},
      sendMessage: () => {},
      id: undefined,
    };
  }

  // 3. Fix permissions API
  const origQuery = navigator.permissions.query.bind(navigator.permissions);
  navigator.permissions.query = (params) =>
    params.name === 'notifications'
      ? Promise.resolve({ state: Notification.permission })
      : origQuery(params);

  // 4. Spoof plugins (headless Chrome has empty plugins array)
  Object.defineProperty(navigator, 'plugins', {
    get: () => [
      { name: 'Chrome PDF Plugin', filename: 'internal-pdf-viewer' },
      { name: 'Chrome PDF Viewer', filename: 'mhjfbmdgcfjbbpaeojofohoefgiehjai' },
      { name: 'Native Client', filename: 'internal-nacl-plugin' },
    ],
  });

  // 5. Spoof languages & platform
  Object.defineProperty(navigator, 'languages', { get: () => ['en-US', 'en'] });
  Object.defineProperty(navigator, 'platform', { get: () => 'MacIntel' });

  // 6. Fix WebGL fingerprint
  const getParam = WebGLRenderingContext.prototype.getParameter;
  WebGLRenderingContext.prototype.getParameter = function(p) {
    if (p === 37445) return 'Google Inc. (Apple)';
    if (p === 37446) return 'ANGLE (Apple, Apple M1 Pro, OpenGL 4.1)';
    return getParam.call(this, p);
  };
`

// ---------------------------------------------------------------------------
// Global state
// ---------------------------------------------------------------------------

type Session = {
  id: string
  context: BrowserContext
  page: Page
  elementMap: Map<string, string>
  response: PWResponse | null
  createdAt: number
  lastUsed: number
  expiresAt: number
}

let browser: Awaited<ReturnType<typeof chromium.launch>> | null = null
const sessions = new Map<string, Session>()

// ---------------------------------------------------------------------------
// Utility helpers (ported from karna)
// ---------------------------------------------------------------------------

function normalizeUrl(url: string | null | undefined, baseUrl: string): string | null {
  if (!url || typeof url !== "string") return null
  const trimmed = url.trim()
  if (!trimmed || trimmed.startsWith("data:")) return null
  try {
    return new URL(trimmed, baseUrl).toString()
  } catch {
    return null
  }
}

function parseIntSafe(value: string | null | undefined): number | null {
  if (!value) return null
  const parsed = parseInt(value, 10)
  return Number.isFinite(parsed) ? parsed : null
}

function parseNumber(value: unknown): number | null {
  if (value === null || value === undefined) return null
  if (typeof value === "number" && Number.isFinite(value)) return value
  if (typeof value === "string" && value.trim() !== "") {
    const parsed = Number(value)
    return Number.isFinite(parsed) ? parsed : null
  }
  return null
}

function normalizeClip(payload: Record<string, unknown>): { clip: { x: number; y: number; width: number; height: number } | null; error?: string } {
  const x = parseNumber(payload.x)
  const y = parseNumber(payload.y)
  const width = parseNumber(payload.width)
  const height = parseNumber(payload.height)
  const values = [x, y, width, height]
  if (values.every((v) => v === null)) return { clip: null }
  if (values.some((v) => v === null)) return { clip: null, error: "invalid_clip" }
  if (width! <= 0 || height! <= 0 || x! < 0 || y! < 0) return { clip: null, error: "invalid_clip" }
  return { clip: { x: x!, y: y!, width: width!, height: height! } }
}

function parseSrcset(srcset: string | null | undefined, baseUrl: string): { url: string; width: number | null } | null {
  if (!srcset || typeof srcset !== "string") return null
  const entries = srcset
    .split(",")
    .map((item) => item.trim())
    .filter(Boolean)
    .map((item) => {
      const parts = item.split(/\s+/)
      const candidateUrl = normalizeUrl(parts[0], baseUrl)
      let width: number | null = null
      if (parts[1] && parts[1].endsWith("w")) {
        width = parseIntSafe(parts[1].slice(0, -1))
      }
      return { url: candidateUrl, width }
    })
    .filter((item): item is { url: string; width: number | null } => item.url !== null)
  if (entries.length === 0) return null
  entries.sort((a, b) => (b.width || 0) - (a.width || 0))
  return entries[0]
}

type ImageInfo = { url: string; source: string; width: number | null; height: number | null }

function addImage(images: ImageInfo[], url: string | null, source: string, width: number | null = null, height: number | null = null) {
  if (!url) return
  images.push({ url, source, width, height })
}

function cleanText(value: string | null | undefined): string {
  if (!value) return ""
  return value.replace(/\s+/g, " ").trim()
}

function cssEscape(value: string): string {
  return value.replace(/[^a-zA-Z0-9_-]/g, (char) => `\\${char}`)
}

function selectorPath(element: Element, root: Element | Document): string {
  const segments: string[] = []
  let current: Element | null = element

  while (current && current.nodeType === 1 && current !== root) {
    const tag = current.tagName.toLowerCase()
    if (current.id) {
      segments.unshift(`${tag}#${cssEscape(current.id)}`)
      return segments.join(" > ")
    }
    const parent = current.parentElement
    if (!parent) {
      segments.unshift(tag)
      break
    }
    const siblings = Array.from(parent.children).filter((child) => child.tagName === current!.tagName)
    if (siblings.length > 1) {
      const index = siblings.indexOf(current) + 1
      segments.unshift(`${tag}:nth-of-type(${index})`)
    } else {
      segments.unshift(tag)
    }
    current = parent
  }

  return segments.join(" > ")
}

// ---------------------------------------------------------------------------
// Image collection and ranking
// ---------------------------------------------------------------------------

function collectImages(document: Document, baseUrl: string): ImageInfo[] {
  const images: ImageInfo[] = []

  document.querySelectorAll('meta[property="og:image"], meta[name="og:image"]').forEach((meta) => {
    addImage(images, normalizeUrl(meta.getAttribute("content"), baseUrl), "og")
  })
  document.querySelectorAll('meta[property="twitter:image"], meta[name="twitter:image"]').forEach((meta) => {
    addImage(images, normalizeUrl(meta.getAttribute("content"), baseUrl), "twitter")
  })
  document.querySelectorAll('link[rel="image_src"]').forEach((link) => {
    addImage(images, normalizeUrl(link.getAttribute("href"), baseUrl), "link")
  })

  document.querySelectorAll("img").forEach((img) => {
    const width = parseIntSafe(img.getAttribute("width"))
    const height = parseIntSafe(img.getAttribute("height"))
    const src = normalizeUrl(img.getAttribute("src"), baseUrl)
    addImage(images, src, "img", width, height)
    const dataSrc = normalizeUrl(img.getAttribute("data-src") || img.getAttribute("data-original"), baseUrl)
    addImage(images, dataSrc, "img_data", width, height)
    const srcset = parseSrcset(img.getAttribute("srcset"), baseUrl)
    if (srcset) addImage(images, srcset.url, "srcset", srcset.width, height)
    const dataSrcset = parseSrcset(img.getAttribute("data-srcset") || img.getAttribute("data-lazy-srcset"), baseUrl)
    if (dataSrcset) addImage(images, dataSrcset.url, "data_srcset", dataSrcset.width, height)
  })

  const deduped = new Map<string, ImageInfo>()
  for (const img of images) {
    if (img.url && !deduped.has(img.url)) deduped.set(img.url, img)
  }
  return Array.from(deduped.values())
}

function rankImages(images: ImageInfo[]): (ImageInfo & { score: number })[] {
  const sourceWeight: Record<string, number> = {
    srcset: 3, data_srcset: 3, img: 2.5, img_data: 2, og: 1.5, twitter: 1.5, link: 1,
  }
  return images
    .map((img) => {
      const base = sourceWeight[img.source] || 1
      const sizeScore = img.width && img.height ? Math.min((img.width * img.height) / 1_000_000, 6) : 0
      return { ...img, score: base + sizeScore }
    })
    .sort((a, b) => b.score - a.score)
}

// ---------------------------------------------------------------------------
// Metadata & link extraction
// ---------------------------------------------------------------------------

function extractMetadata(document: Document, baseUrl: string) {
  return {
    canonical: normalizeUrl(document.querySelector('link[rel="canonical"]')?.getAttribute("href") ?? null, baseUrl),
    og_image: normalizeUrl(
      document.querySelector('meta[property="og:image"], meta[name="og:image"]')?.getAttribute("content") ?? null,
      baseUrl,
    ),
    twitter_image: normalizeUrl(
      document.querySelector('meta[property="twitter:image"], meta[name="twitter:image"]')?.getAttribute("content") ?? null,
      baseUrl,
    ),
  }
}

function extractLinks(root: Element | Document, baseUrl: string, maxLinks: number): string[] {
  const links: string[] = []
  const seen = new Set<string>()
  root.querySelectorAll("a[href]").forEach((anchor) => {
    if (links.length >= maxLinks) return
    const url = normalizeUrl(anchor.getAttribute("href"), baseUrl)
    if (!url || seen.has(url)) return
    seen.add(url)
    links.push(url)
  })
  return links
}

// ---------------------------------------------------------------------------
// Actionable elements
// ---------------------------------------------------------------------------

function collectActionableElements(document: Document, baseUrl: string, maxElements: number) {
  const elements: { id: string; tag: string; type: string | null; label: string; href: string | null; selector: string }[] = []
  const elementMap = new Map<string, string>()
  let truncated = false

  const candidates = Array.from(
    document.querySelectorAll("a[href], button, input, select, textarea, [role='button'], [role='link']"),
  ).filter((el) => !el.closest("nav[aria-hidden='true']"))

  for (const el of candidates) {
    if (elements.length >= maxElements) { truncated = true; break }
    const tag = el.tagName.toLowerCase()
    const type = el.getAttribute("type") || null
    if (tag === "input" && type === "hidden") continue
    const label =
      cleanText(el.textContent) ||
      cleanText(el.getAttribute("aria-label")) ||
      cleanText(el.getAttribute("placeholder")) ||
      cleanText(el.getAttribute("value"))
    const href = tag === "a" ? normalizeUrl(el.getAttribute("href"), baseUrl) : null
    const selector = selectorPath(el, document.body)
    const id = `el_${elements.length + 1}`
    elements.push({ id, tag, type, label, href, selector })
    elementMap.set(id, selector)
  }

  return { elements, elementMap, truncated }
}

// ---------------------------------------------------------------------------
// Section extraction
// ---------------------------------------------------------------------------

function clampText(text: string, maxChars: number): string {
  if (!text) return ""
  return text.length <= maxChars ? text : text.slice(0, maxChars)
}

function clampMarkdown(markdown: string, maxChars: number): { markdown: string; truncated: boolean } {
  if (!markdown) return { markdown: "", truncated: false }
  if (markdown.length <= maxChars) return { markdown, truncated: false }
  return { markdown: markdown.slice(0, maxChars) + "\n[...content truncated...]", truncated: true }
}

function createSectionContainer(document: Document, heading: Element, nextHeading: Element | null, root: Element) {
  const range = document.createRange()
  range.setStartAfter(heading)
  if (nextHeading) {
    range.setEndBefore(nextHeading)
  } else if (root.lastChild) {
    range.setEndAfter(root.lastChild)
  } else {
    range.setEndAfter(heading)
  }
  const fragment = range.cloneContents()
  const container = document.createElement("div")
  container.appendChild(fragment)
  container.querySelectorAll("script, style, noscript").forEach((n) => n.remove())
  return container
}

type SectionOpts = {
  maxSections: number
  excerptChars: number
  sectionMarkdownChars: number
  maxImages: number
  maxSectionLinks: number
  targetIndex: number | null
  targetSelector: string | null
}

function extractSections(root: Element | null, baseUrl: string, options: SectionOpts) {
  if (!root) return { sections: [] as Record<string, unknown>[], truncated: false, selected: null as Record<string, unknown> | null }
  const document = root.ownerDocument!
  const headings = Array.from(root.querySelectorAll("h1, h2, h3")).filter(
    (h) => !h.closest("nav, header, footer, aside"),
  )

  const sections: Record<string, unknown>[] = []
  let selected: Record<string, unknown> | null = null
  let truncated = false

  for (let i = 0; i < headings.length; i++) {
    const heading = headings[i]
    const subject = cleanText(heading.textContent || "")
    if (!subject) continue
    const level = parseInt(heading.tagName.slice(1), 10) || null

    let nextHeading: Element | null = null
    for (let j = i + 1; j < headings.length; j++) {
      const nextLevel = parseInt(headings[j].tagName.slice(1), 10) || null
      if (nextLevel && level && nextLevel <= level) {
        nextHeading = headings[j]
        break
      }
    }

    const container = createSectionContainer(document, heading, nextHeading, root)
    const text = cleanText(container.textContent || "")
    const excerpt = clampText(text, options.excerptChars)
    const selector = selectorPath(heading, root)

    const sectionIndex = sections.length
    sections.push({ index: sectionIndex, subject, excerpt, level, selector })

    const matchesIndex = options.targetIndex === sectionIndex
    const matchesSelector = options.targetSelector && options.targetSelector === selector
    if ((matchesIndex || matchesSelector) && !selected) {
      const turndown = new TurndownService({ codeBlockStyle: "fenced" })
      const sectionHtml = heading.outerHTML + container.innerHTML
      const sectionMarkdown = turndown.turndown(sectionHtml)
      const { markdown } = clampMarkdown(sectionMarkdown, options.sectionMarkdownChars)
      const sectionImages = rankImages(collectImages(container as unknown as Document, baseUrl)).slice(0, options.maxImages)
      const sectionLinks = extractLinks(container, baseUrl, options.maxSectionLinks)

      selected = { index: sectionIndex, subject, excerpt, level, selector, markdown, images: sectionImages, links: sectionLinks }
    }

    if (options.maxSections && sections.length >= options.maxSections) { truncated = true; break }
  }

  return { sections, truncated, selected }
}

// ---------------------------------------------------------------------------
// Error & challenge detection
// ---------------------------------------------------------------------------

type ChallengeType = "cloudflare" | "turnstile" | "recaptcha" | "hcaptcha" | null

function detectErrorType(html: string, status: number | null): string | null {
  if (status === 401) return "login_required"
  if (status === 403 || status === 429) return "blocked"
  if (status === 451) return "geo_blocked"
  if (status && status >= 400) return "http_error"
  if (!html) return null
  const lower = html.toLowerCase()
  const captchaWord = /\bcaptcha\b/.test(lower)
  const captchaWidget = lower.includes("g-recaptcha") || lower.includes("hcaptcha") || lower.includes("data-sitekey")
  if (captchaWord || captchaWidget) {
    if (lower.includes("verify you are human") || lower.includes("unusual traffic") || lower.includes("request blocked") || lower.includes("access denied")) {
      return "captcha"
    }
  }
  if (BLOCKED_MARKERS.some((marker) => lower.includes(marker))) return "blocked"
  return null
}

function detectChallenge(_page: Page, html: string, title: string): ChallengeType {
  if (title.includes("Just a moment")) return "cloudflare"
  const lower = html.toLowerCase()
  if (lower.includes("cf-turnstile") || lower.includes("challenges.cloudflare.com")) return "turnstile"
  if (lower.includes("g-recaptcha") || lower.includes("google.com/recaptcha")) return "recaptcha"
  if (lower.includes("h-captcha") || lower.includes("hcaptcha.com")) return "hcaptcha"
  return null
}

// ---------------------------------------------------------------------------
// Browser management
// ---------------------------------------------------------------------------

async function launchBrowser() {
  try {
    return await chromium.launch({
      channel: "chrome",
      headless: true,
      args: ["--disable-blink-features=AutomationControlled"],
    })
  } catch {
    return await chromium.launch({
      headless: true,
      args: ["--disable-blink-features=AutomationControlled"],
    })
  }
}

async function ensureBrowser() {
  if (!browser) {
    browser = await launchBrowser()
  }
  return browser
}

async function newContext(): Promise<BrowserContext> {
  const b = await ensureBrowser()
  const context = await b.newContext({
    userAgent: USER_AGENT,
    locale: DEFAULT_LOCALE,
    timezoneId: DEFAULT_TIMEZONE,
    viewport: DEFAULT_VIEWPORT,
    deviceScaleFactor: 2,
    extraHTTPHeaders: DEFAULT_HEADERS,
  })
  await context.addInitScript(STEALTH_SCRIPT)
  return context
}

async function waitForSettled(page: Page, timeoutMs: number) {
  const waitTimeout = Math.min(timeoutMs, 5000)
  try {
    await page.waitForLoadState("networkidle", { timeout: waitTimeout })
  } catch (error: unknown) {
    if ((error as { name?: string })?.name !== "TimeoutError") throw error
  }
  await page.waitForTimeout(250)
}

async function navigateTo(page: Page, url: string, timeoutMs: number): Promise<{ response: PWResponse | null; challengeResult: ChallengeResult | null }> {
  const response = await page.goto(url, { waitUntil: "domcontentloaded", timeout: timeoutMs })
  await waitForSettled(page, timeoutMs)

  // Challenge detection & auto-resolution
  const html = await page.content()
  const title = await page.title()
  const challengeType = detectChallenge(page, html, title)

  if (challengeType) {
    // 1. Wait for auto-resolution (up to 10s)
    await page.waitForFunction(
      () => !document.title.includes("Just a moment"),
      { timeout: 10_000 },
    ).catch(() => {})

    // Re-check
    const html2 = await page.content()
    const title2 = await page.title()
    const still = detectChallenge(page, html2, title2)

    if (still) {
      // 2. Try clicking Turnstile checkbox
      try {
        const frame = page.frameLocator('iframe[src*="challenges.cloudflare.com"]')
        await frame.locator('input[type="checkbox"], .cb-lb').click({ timeout: 3000 })
        await page.waitForFunction(
          () => !document.title.includes("Just a moment"),
          { timeout: 10_000 },
        ).catch(() => {})
      } catch {}

      // 3. Final check — return challenge info if still blocked
      const html3 = await page.content()
      const title3 = await page.title()
      const finalChallenge = detectChallenge(page, html3, title3)
      if (finalChallenge) {
        await mkdir(SCREENSHOT_DIR, { recursive: true })
        const buffer = await page.screenshot({ type: "png" })
        const path = join(SCREENSHOT_DIR, `challenge-${Date.now()}.png`)
        await Bun.write(path, buffer)
        return { response, challengeResult: { challenge_type: finalChallenge, screenshot: path } }
      }
    }
  }

  return { response, challengeResult: null }
}

type ChallengeResult = { challenge_type: ChallengeType; screenshot: string }

// ---------------------------------------------------------------------------
// Session management
// ---------------------------------------------------------------------------

function touchSession(session: Session) {
  session.lastUsed = Date.now()
  session.expiresAt = session.lastUsed + SESSION_TTL_MS
}

async function createSession(url: string | null, timeoutMs: number): Promise<{ session: Session; challengeResult: ChallengeResult | null }> {
  const context = await newContext()
  const page = await context.newPage()

  await page.route("**/*", (route) => {
    const type = route.request().resourceType()
    if (type === "media") return route.abort()
    return route.continue()
  })

  let response: PWResponse | null = null
  let challengeResult: ChallengeResult | null = null
  if (url) {
    const nav = await navigateTo(page, url, timeoutMs)
    response = nav.response
    challengeResult = nav.challengeResult
  }

  const sessionId = crypto.randomUUID()
  const session: Session = {
    id: sessionId,
    context,
    page,
    createdAt: Date.now(),
    lastUsed: Date.now(),
    expiresAt: Date.now() + SESSION_TTL_MS,
    elementMap: new Map(),
    response,
  }
  sessions.set(sessionId, session)
  return { session, challengeResult }
}

async function getSession(sessionId: string | undefined, url: string | null, timeoutMs: number): Promise<{ session: Session | null; challengeResult: ChallengeResult | null }> {
  if (sessionId && sessions.has(sessionId)) {
    const session = sessions.get(sessionId)!
    touchSession(session)
    let challengeResult: ChallengeResult | null = null
    if (url) {
      const nav = await navigateTo(session.page, url, timeoutMs)
      session.response = nav.response
      challengeResult = nav.challengeResult
    }
    return { session, challengeResult }
  }
  if (!url) return { session: null, challengeResult: null }
  return await createSession(url, timeoutMs)
}

function cleanupSessions() {
  const now = Date.now()
  for (const [id, session] of sessions.entries()) {
    if (now >= session.expiresAt) {
      session.context.close().catch(() => {}).finally(() => sessions.delete(id))
    }
  }
}

setInterval(cleanupSessions, CLEANUP_INTERVAL_MS)

// ---------------------------------------------------------------------------
// Snapshot: summary
// ---------------------------------------------------------------------------

async function snapshotSummary(session: Session, options: {
  maxImages: number; maxElements: number; maxSections: number
  excerptChars: number; sectionMarkdownChars: number; maxSectionLinks: number
  sectionIndex: number | null; sectionSelector: string | null
}) {
  const page = session.page
  const finalUrl = page.url()
  const status = session.response ? session.response.status() : null
  const html = await page.content()
  const title = await page.title()
  const lang = await page.evaluate(() => document.documentElement.lang || null).catch(() => null)

  const dom = new JSDOM(html, { url: finalUrl })
  const document = dom.window.document
  const contentNode = document.querySelector("main") || document.body
  const contentClone = contentNode ? contentNode.cloneNode(true) as Element : null
  if (contentClone) {
    contentClone.querySelectorAll("script, style, noscript").forEach((n: Element) => n.remove())
  }

  const text = contentClone ? cleanText(contentClone.textContent || "") : ""
  const excerpt = clampText(text, options.excerptChars)
  const images = rankImages(collectImages(document, finalUrl)).slice(0, options.maxImages)
  const elementsResult = collectActionableElements(document, finalUrl, options.maxElements)
  session.elementMap = elementsResult.elementMap

  const sectionResult = extractSections(contentNode, finalUrl, {
    maxSections: options.maxSections,
    excerptChars: options.excerptChars,
    sectionMarkdownChars: options.sectionMarkdownChars,
    maxImages: options.maxImages,
    maxSectionLinks: options.maxSectionLinks,
    targetIndex: options.sectionIndex,
    targetSelector: options.sectionSelector,
  })

  const errorType = detectErrorType(html, status)
  const statusLabel = errorType ? (errorType === "blocked" || errorType === "captcha" ? "blocked" : "error") : "ok"

  return {
    status: statusLabel, error_type: errorType,
    url: finalUrl, title, language: lang, viewport: DEFAULT_VIEWPORT,
    excerpt, images,
    elements: elementsResult.elements, elements_truncated: elementsResult.truncated,
    sections: sectionResult.sections, sections_truncated: sectionResult.truncated,
    metadata: extractMetadata(document, finalUrl),
  }
}

// ---------------------------------------------------------------------------
// Snapshot: read (with Readability)
// ---------------------------------------------------------------------------

async function snapshotRead(session: Session, options: {
  maxImages: number; maxSections: number; maxMarkdownChars: number
  sectionExcerptChars: number; sectionMarkdownChars: number; maxSectionLinks: number
  sectionIndex: number | null; sectionSelector: string | null
}) {
  const page = session.page
  const finalUrl = page.url()
  const status = session.response ? session.response.status() : null
  const html = await page.content()
  const title = await page.title()
  const lang = await page.evaluate(() => document.documentElement.lang || null).catch(() => null)

  const dom = new JSDOM(html, { url: finalUrl })
  const document = dom.window.document

  // Try Readability extraction first
  const readabilityDom = new JSDOM(html, { url: finalUrl })
  const article = new Readability(readabilityDom.window.document).parse()

  let contentHtml: string
  if (article && article.content) {
    contentHtml = article.content
  } else {
    // Fallback: raw body minus script/style/noscript
    const contentNode = document.querySelector("main") || document.body
    const contentClone = contentNode ? contentNode.cloneNode(true) as Element : null
    if (contentClone) {
      contentClone.querySelectorAll("script, style, noscript").forEach((n: Element) => n.remove())
    }
    contentHtml = contentClone ? contentClone.innerHTML : ""
  }

  const turndown = new TurndownService({ codeBlockStyle: "fenced" })
  const markdown = turndown.turndown(contentHtml)
  const { markdown: finalMarkdown, truncated } = clampMarkdown(markdown, options.maxMarkdownChars)

  const contentNode = document.querySelector("main") || document.body
  const sectionResult = extractSections(contentNode, finalUrl, {
    maxSections: options.maxSections,
    excerptChars: options.sectionExcerptChars,
    sectionMarkdownChars: options.sectionMarkdownChars,
    maxImages: options.maxImages,
    maxSectionLinks: options.maxSectionLinks,
    targetIndex: options.sectionIndex,
    targetSelector: options.sectionSelector,
  })

  const images = rankImages(collectImages(document, finalUrl)).slice(0, options.maxImages)
  const errorType = detectErrorType(html, status)
  const statusLabel = errorType ? (errorType === "blocked" || errorType === "captcha" ? "blocked" : "error") : "ok"

  return {
    status: statusLabel, error_type: errorType,
    url: finalUrl, title, language: lang, viewport: DEFAULT_VIEWPORT,
    markdown: finalMarkdown, truncated,
    sections: sectionResult.sections, sections_truncated: sectionResult.truncated,
    selected_section: sectionResult.selected,
    images, metadata: extractMetadata(document, finalUrl),
  }
}

// ---------------------------------------------------------------------------
// Action execution
// ---------------------------------------------------------------------------

function resolveTarget(session: Session, target: string | undefined | null): string | null {
  if (!target) return null
  if (session.elementMap.has(target)) return session.elementMap.get(target)!
  return target
}

async function runActions(session: Session, actions: unknown[], timeoutMs: number) {
  if (!Array.isArray(actions) || actions.length === 0) return []
  const page = session.page
  const errors: { index: number; action: string; error: string }[] = []
  const actionTimeout = Math.min(timeoutMs, 10_000)

  for (let i = 0; i < actions.length; i++) {
    const action = actions[i] as Record<string, unknown>
    if (!action || typeof action !== "object") continue
    const actionType = action.action as string
    const selector = resolveTarget(session, action.target as string | undefined)

    try {
      switch (actionType) {
        case "click":
          if (!selector) throw new Error("click requires target")
          await page.click(selector, { timeout: actionTimeout })
          break
        case "double_click":
          if (!selector) throw new Error("double_click requires target")
          await page.dblclick(selector, { timeout: actionTimeout })
          break
        case "fill":
          if (!selector) throw new Error("fill requires target")
          await page.fill(selector, (action.value as string) ?? "", { timeout: actionTimeout })
          break
        case "type":
          if (!selector) throw new Error("type requires target")
          await page.type(selector, (action.value as string) ?? "", {
            timeout: actionTimeout,
            delay: (action.delay_ms as number) ?? 0,
          })
          break
        case "press":
          if (selector) {
            await page.press(selector, (action.key as string) ?? "", { timeout: actionTimeout })
          } else {
            await page.keyboard.press((action.key as string) ?? "")
          }
          break
        case "hover":
          if (!selector) throw new Error("hover requires target")
          await page.hover(selector, { timeout: actionTimeout })
          break
        case "focus":
          if (!selector) throw new Error("focus requires target")
          await page.focus(selector, { timeout: actionTimeout })
          break
        case "select":
          if (!selector) throw new Error("select requires target")
          await page.selectOption(selector, { value: action.value as string, label: action.label as string })
          break
        case "check":
          if (!selector) throw new Error("check requires target")
          await page.check(selector, { timeout: actionTimeout })
          break
        case "uncheck":
          if (!selector) throw new Error("uncheck requires target")
          await page.uncheck(selector, { timeout: actionTimeout })
          break
        case "scroll":
          if (selector) {
            await page.locator(selector).scrollIntoViewIfNeeded()
          } else {
            await page.evaluate(
              ({ x, y }: { x: number | null; y: number | null }) => {
                window.scrollTo(x ?? window.scrollX, y ?? window.scrollY)
              },
              { x: (action.x as number) ?? null, y: (action.y as number) ?? null },
            )
          }
          break
        case "wait":
          await page.waitForTimeout((action.wait_ms as number) ?? 1000)
          break
        default:
          throw new Error(`unsupported action: ${actionType}`)
      }
    } catch (error: unknown) {
      errors.push({ index: i, action: actionType, error: (error as Error)?.message || "action_failed" })
    }
  }

  return errors
}

// ---------------------------------------------------------------------------
// Session meta helper
// ---------------------------------------------------------------------------

function sessionMeta(session: Session) {
  return { session_id: session.id, expires_at: session.expiresAt, ttl_ms: SESSION_TTL_MS }
}

// ---------------------------------------------------------------------------
// Command handlers
// ---------------------------------------------------------------------------

async function handleBrowse(payload: Record<string, unknown>) {
  const timeoutMs = (payload.timeout_ms as number) || DEFAULT_TIMEOUT_MS
  const maxImages = (payload.max_images as number) ?? DEFAULT_MAX_IMAGES
  const maxElements = (payload.max_elements as number) ?? DEFAULT_MAX_ELEMENTS
  const maxSections = (payload.max_sections as number) ?? DEFAULT_MAX_SECTIONS
  const sectionExcerptChars = (payload.section_excerpt_chars as number) ?? DEFAULT_SECTION_EXCERPT_CHARS
  const sectionMarkdownChars = (payload.section_markdown_chars as number) ?? DEFAULT_SECTION_MARKDOWN_CHARS
  const maxSectionLinks = (payload.max_section_links as number) ?? DEFAULT_MAX_SECTION_LINKS

  const { session, challengeResult } = await getSession(payload.session_id as string | undefined, payload.url as string, timeoutMs)
  if (!session) return { status: "error", error_type: "invalid_session" }

  if (challengeResult) {
    return {
      status: "challenge",
      challenge_type: challengeResult.challenge_type,
      screenshot: challengeResult.screenshot,
      url: session.page.url(),
      ...sessionMeta(session),
    }
  }

  const summary = await snapshotSummary(session, {
    maxImages, maxElements, maxSections,
    excerptChars: sectionExcerptChars, sectionMarkdownChars, maxSectionLinks,
    sectionIndex: null, sectionSelector: null,
  })
  touchSession(session)
  return { ...summary, ...sessionMeta(session) }
}

async function handleRead(payload: Record<string, unknown>) {
  const timeoutMs = (payload.timeout_ms as number) || DEFAULT_TIMEOUT_MS
  const maxImages = (payload.max_images as number) ?? DEFAULT_MAX_IMAGES
  const maxSections = (payload.max_sections as number) ?? DEFAULT_MAX_SECTIONS
  const maxMarkdownChars = (payload.max_markdown_chars as number) ?? DEFAULT_MAX_MARKDOWN_CHARS
  const sectionExcerptChars = (payload.section_excerpt_chars as number) ?? DEFAULT_SECTION_EXCERPT_CHARS
  const sectionMarkdownChars = (payload.section_markdown_chars as number) ?? DEFAULT_SECTION_MARKDOWN_CHARS
  const maxSectionLinks = (payload.max_section_links as number) ?? DEFAULT_MAX_SECTION_LINKS

  const { session, challengeResult } = await getSession(payload.session_id as string | undefined, payload.url as string, timeoutMs)
  if (!session) return { status: "error", error_type: "invalid_session" }

  if (challengeResult) {
    return {
      status: "challenge",
      challenge_type: challengeResult.challenge_type,
      screenshot: challengeResult.screenshot,
      url: session.page.url(),
      ...sessionMeta(session),
    }
  }

  const sectionIndex = Number.isFinite(Number(payload.section_index)) ? Number(payload.section_index) : null

  const readResult = await snapshotRead(session, {
    maxImages, maxSections, maxMarkdownChars,
    sectionExcerptChars, sectionMarkdownChars, maxSectionLinks,
    sectionIndex, sectionSelector: (payload.section_selector as string) || null,
  })
  touchSession(session)
  return { ...readResult, ...sessionMeta(session) }
}

async function handleInteract(payload: Record<string, unknown>) {
  if (!payload.session_id) return { status: "error", error_type: "session_required" }

  const timeoutMs = (payload.timeout_ms as number) || DEFAULT_TIMEOUT_MS
  const maxImages = (payload.max_images as number) ?? DEFAULT_MAX_IMAGES
  const maxElements = (payload.max_elements as number) ?? DEFAULT_MAX_ELEMENTS
  const maxSections = (payload.max_sections as number) ?? DEFAULT_MAX_SECTIONS
  const sectionExcerptChars = (payload.section_excerpt_chars as number) ?? DEFAULT_SECTION_EXCERPT_CHARS
  const sectionMarkdownChars = (payload.section_markdown_chars as number) ?? DEFAULT_SECTION_MARKDOWN_CHARS
  const maxSectionLinks = (payload.max_section_links as number) ?? DEFAULT_MAX_SECTION_LINKS

  const { session } = await getSession(payload.session_id as string, null, timeoutMs)
  if (!session) return { status: "error", error_type: "invalid_session" }

  const beforeUrl = session.page.url()
  const actionErrors = await runActions(session, payload.actions as unknown[], timeoutMs)
  await waitForSettled(session.page, timeoutMs)
  const afterUrl = session.page.url()

  let content = null
  if (payload.return_content) {
    content = await snapshotSummary(session, {
      maxImages, maxElements, maxSections,
      excerptChars: sectionExcerptChars, sectionMarkdownChars, maxSectionLinks,
      sectionIndex: null, sectionSelector: null,
    })
  }

  touchSession(session)
  return {
    status: actionErrors.length ? "partial" : "ok",
    error_type: null, url: afterUrl,
    new_url: afterUrl !== beforeUrl ? afterUrl : null,
    action_errors: actionErrors.length ? actionErrors : null,
    content, viewport: DEFAULT_VIEWPORT,
    ...sessionMeta(session),
  }
}

async function handleScreenshot(payload: Record<string, unknown>) {
  if (!payload.session_id) return { status: "error", error_type: "session_required" }

  const timeoutMs = (payload.timeout_ms as number) || DEFAULT_TIMEOUT_MS
  const { clip, error } = normalizeClip(payload)
  if (error) return { status: "error", error_type: error }
  if (clip && payload.target) return { status: "error", error_type: "target_with_clip" }

  const { session } = await getSession(payload.session_id as string, null, timeoutMs)
  if (!session) return { status: "error", error_type: "invalid_session" }

  await waitForSettled(session.page, timeoutMs)

  const selector = resolveTarget(session, payload.target as string | undefined)
  const fullPage = payload.full_page === true && !clip
  const shotTimeout = Math.min(timeoutMs, 10_000)
  let buffer: Buffer

  if (selector) {
    buffer = await session.page.locator(selector).screenshot({ timeout: shotTimeout, type: "png" })
  } else if (clip) {
    buffer = await session.page.screenshot({ timeout: shotTimeout, type: "png", clip })
  } else {
    buffer = await session.page.screenshot({ timeout: shotTimeout, fullPage, type: "png" })
  }

  await mkdir(SCREENSHOT_DIR, { recursive: true })
  const path = join(SCREENSHOT_DIR, `${session.id}-${Date.now()}.png`)
  await Bun.write(path, buffer)
  touchSession(session)

  return { status: "ok", url: session.page.url(), path, ...sessionMeta(session) }
}

// ---------------------------------------------------------------------------
// HTTP server (Bun.serve)
// ---------------------------------------------------------------------------

console.error(`[browse-server] Starting on port ${PORT}...`)

Bun.serve({
  port: PORT,
  hostname: "127.0.0.1",
  async fetch(req) {
    const url = new URL(req.url)

    // Health check
    if (url.pathname === "/ping" && req.method === "GET") {
      return Response.json({ status: "ok" })
    }

    if (req.method !== "POST") {
      return Response.json({ status: "error", error_type: "method_not_allowed" }, { status: 405 })
    }

    let payload: Record<string, unknown>
    try {
      payload = (await req.json()) as Record<string, unknown>
    } catch {
      return Response.json({ status: "error", error_type: "invalid_json" }, { status: 400 })
    }

    const command = payload.command as string
    if (!command) {
      return Response.json({ status: "error", error_type: "missing_command" }, { status: 400 })
    }

    try {
      let result: Record<string, unknown>
      switch (command) {
        case "browse":
          result = await handleBrowse(payload)
          break
        case "read":
          result = await handleRead(payload)
          break
        case "interact":
          result = await handleInteract(payload)
          break
        case "screenshot":
          result = await handleScreenshot(payload)
          break
        case "close": {
          const session = sessions.get(payload.session_id as string)
          if (session) {
            await session.context.close().catch(() => {})
            sessions.delete(payload.session_id as string)
          }
          result = { status: "ok" }
          break
        }
        default:
          return Response.json({ status: "error", error_type: "unknown_command" }, { status: 400 })
      }
      return Response.json(result)
    } catch (error: unknown) {
      return Response.json(
        { status: "error", error_type: "server_error", message: (error as Error)?.message || "web browser failed" },
        { status: 500 },
      )
    }
  },
})

console.error(`[browse-server] Listening on http://127.0.0.1:${PORT}`)
